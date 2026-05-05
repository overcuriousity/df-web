FROM debian:stable-slim AS base

RUN apt-get update && apt-get install -y --no-install-recommends \
    libsdl2-2.0-0 \
    libsdl2-image-2.0-0 \
    libsdl2-ttf-2.0-0 \
    libglu1-mesa \
    libgl1 \
    libc6 \
    && rm -rf /var/lib/apt/lists/*

# Dwarf Fortress is NOT downloaded here — you must supply the Linux build yourself.
# Place the contents of the DF tarball in df-image-base/df/ before building:
#
#   mkdir df-image-base/df
#   tar -xjf df_53.12_linux.tar.bz2 -C df-image-base/df --strip-components=1
#
# df-image-base/df/ should contain: dwarfort, data/, raw/, stonesense/ etc.
COPY df/ /opt/df/

# Save directory lives on a bind-mount; replace the in-image dir with a symlink.
# The mount point is always /save.
RUN rm -rf /opt/df/data/save && ln -s /save /opt/df/data/save \
 && chmod +x /opt/df/dwarfort

# Bake tilesets (admin-managed): copy PNGs into the art directory.
# Drop PNG files into df-image-base/tilesets/ to include them.
COPY tilesets/ /opt/df/data/art/

# Baseline config and keybinds — SDL/text variants override PRINT_MODE in their own layer.
COPY init.txt /opt/df/data/init/init.txt
COPY interface.txt /opt/df/data/init/interface.txt

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

VOLUME ["/save"]
WORKDIR /opt/df
