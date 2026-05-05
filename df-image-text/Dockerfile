FROM df-image-base AS text

RUN apt-get update && apt-get install -y --no-install-recommends \
    dtach \
    netcat-openbsd \
    && rm -rf /var/lib/apt/lists/*

# ttyd — single binary terminal-over-websocket, MIT licensed.
# Download pre-built static binary from GitHub releases.
ARG TTYD_VERSION=1.7.7
RUN curl -fSL "https://github.com/tsl0922/ttyd/releases/download/${TTYD_VERSION}/ttyd.x86_64" \
    -o /usr/local/bin/ttyd \
    && chmod +x /usr/local/bin/ttyd

# Override PRINT_MODE to TEXT for terminal rendering.
RUN sed -i 's/^\[PRINT_MODE:.*\]/[PRINT_MODE:TEXT]/' /opt/df/data/init/init.txt
# Fix terminal grid to match xterm.js client dimensions (cols x rows).
RUN sed -i 's/^\[WINDOWEDX:.*\]/[WINDOWEDX:160]/' /opt/df/data/init/init.txt \
 && sed -i 's/^\[WINDOWEDY:.*\]/[WINDOWEDY:50]/' /opt/df/data/init/init.txt

COPY start.sh /start.sh
COPY quit-text.sh /quit-text.sh
RUN chmod +x /start.sh /quit-text.sh

EXPOSE 7681

CMD ["/start.sh"]
