FROM df-image-base AS sdl

RUN apt-get update && apt-get install -y --no-install-recommends \
    xvfb \
    x11vnc \
    websockify \
    novnc \
    xdotool \
    s6 \
    && rm -rf /var/lib/apt/lists/*

# Override PRINT_MODE to STANDARD for SDL rendering.
RUN sed -i 's/^\[PRINT_MODE:.*\]/[PRINT_MODE:STANDARD]/' /opt/df/data/init/init.txt

# s6 service definitions.
COPY s6/ /etc/s6/

# Wrapper that sends DF a quit-save via xdotool when the container stops.
COPY quit-sdl.sh /quit-sdl.sh
RUN chmod +x /quit-sdl.sh

# noVNC HTML5 client — Debian ships it at /usr/share/novnc; symlink for convenience.
RUN ln -sfn /usr/share/novnc /novnc

EXPOSE 6080

ENV DISPLAY=:0
ENV XVFB_RESOLUTION=1280x800x24

CMD ["/usr/bin/s6-svscan", "/etc/s6"]
