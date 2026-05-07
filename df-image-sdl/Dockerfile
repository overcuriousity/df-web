FROM df-image-base AS sdl

RUN apt-get update && apt-get install -y --no-install-recommends \
    xvfb \
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
    && rm -rf /var/lib/apt/lists/*

# PRINT_MODE:AUTO lets SDL2 auto-select the renderer (hardware or software).
# On headless Xvfb it falls back to software, which is fine for DF.
# No sed override needed — AUTO is already the default in init_default.txt.

# s6 service definitions.
COPY s6/ /etc/s6/

# Audio streaming helper.
COPY serve-audio.py /serve-audio.py

# Wrapper that sends DF a quit-save via xdotool when the container stops.
COPY quit-sdl.sh /quit-sdl.sh
RUN chmod +x /quit-sdl.sh

# Spawn-time hook: copies user-uploaded tilesets into data/art and patches
# init.txt with the active selection (DF_ACTIVE_TILESET env var). Invoked
# from s6/df/run before launching DF.
COPY apply-tilesets.sh /apply-tilesets.sh
RUN chmod +x /apply-tilesets.sh

# noVNC HTML5 client — Debian ships it at /usr/share/novnc.
RUN ln -sfn /usr/share/novnc /novnc

EXPOSE 6080

ENV DISPLAY=:0
ENV XVFB_RESOLUTION=1280x800x24

CMD ["/usr/bin/s6-svscan", "/etc/s6"]
