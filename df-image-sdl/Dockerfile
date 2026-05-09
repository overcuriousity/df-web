FROM df-image-base AS sdl

RUN apt-get update && apt-get install -y --no-install-recommends \
    xvfb \
    x11-xserver-utils \
    x11vnc \
    websockify \
    novnc \
    xdotool \
    netcat-openbsd \
    s6 \
    pulseaudio \
    pulseaudio-utils \
    ffmpeg \
    python3-minimal \
    libsdl2-2.0-0 \
    libsdl2-image-2.0-0 \
    libopengl0 \
    wget \
    bzip2 \
    && rm -rf /var/lib/apt/lists/*

# Optional: install DFHack. Set DFHACK_VERSION at build time to enable.
# DFHack is not included in this repository and is not required; leaving this
# arg empty produces a vanilla DF image. When set, the value must match the
# installed DF version exactly (e.g. 53.12-r1).
# DFHack is copyright its respective contributors; see https://github.com/DFHack/dfhack
ARG DFHACK_VERSION=""
RUN if [ -n "${DFHACK_VERSION}" ]; then \
      wget -q "https://github.com/DFHack/dfhack/releases/download/${DFHACK_VERSION}/dfhack-${DFHACK_VERSION}-Linux-64bit.tar.bz2" \
        -O /tmp/dfhack.tar.bz2 \
      && tar -xjf /tmp/dfhack.tar.bz2 -C /opt/df \
      && rm /tmp/dfhack.tar.bz2 \
      && sed -i \
           -e 's|setarch "\$setarch_arch" -R ||g' \
           -e 's|exec setarch "\$setarch_arch" -R ||g' \
           -e 's|strace -f setarch "\$setarch_arch" -R ||g' \
           /opt/df/dfhack; \
    fi

# PRINT_MODE:AUTO lets SDL2 auto-select the renderer (hardware or software).
# On headless Xvfb it falls back to software, which is fine for DF.
# No sed override needed — AUTO is already the default in init_default.txt.

# s6 service definitions.
COPY s6/ /etc/s6/

# Audio streaming helper.
COPY serve-audio.py /serve-audio.py

# Spawn-time hook: copies user-uploaded tilesets into data/art and patches
# init.txt with the active selection (DF_ACTIVE_TILESET env var). Invoked
# from s6/df/run before launching DF.
COPY apply-tilesets.sh /apply-tilesets.sh
RUN chmod +x /apply-tilesets.sh

# Symlink gamelog.txt into the bind-mounted save dir so the session-manager
# can tail it from the host without docker exec.
COPY link-gamelog.sh /link-gamelog.sh
RUN chmod +x /link-gamelog.sh

# DFHack autoload: enable manipulator (in-game Therapist) on every session.
COPY dfhack-config/ /opt/df/dfhack-config/

# Web-facing DFHack Lua scripts invoked via dfhack-run from the session-manager.
COPY hack/ /opt/df/hack/

# noVNC HTML5 client — Debian ships it at /usr/share/novnc.
RUN ln -sfn /usr/share/novnc /novnc

EXPOSE 6080
# DFHack remote Lua/protobuf API (accessed only within the Docker network).
EXPOSE 5000

ENV DISPLAY=:0
ENV XVFB_RESOLUTION=1280x800x24

CMD ["/usr/bin/s6-svscan", "/etc/s6"]
