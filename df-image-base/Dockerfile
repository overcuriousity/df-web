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
# df-image-base/df/ should contain: dwarfort, data/, raw/, etc.
COPY df/ /opt/df/
RUN chmod +x /opt/df/dwarfort

# Save directory lives on a bind-mount at /save.
# Replace the in-image dir with a symlink so DF writes to the mount.
RUN rm -rf /opt/df/data/save && ln -s /save /opt/df/data/save

# Bake tilesets (admin-managed): copy PNGs into the art directory.
# Drop PNG files into df-image-base/tilesets/ to include them.
COPY tilesets/ /opt/df/data/art/

# Patch the shipped config files in place.
# DF 53.x reads init_default.txt and d_init_default.txt (not init.txt).
# WINDOWED:YES — needed for headless Xvfb rendering.
# SOUND:NO    — no audio hardware in the container.
RUN sed -i 's/^\[WINDOWED:.*\]/[WINDOWED:YES]/' /opt/df/data/init/init_default.txt \
 && sed -i 's/^\[SOUND:.*\]/[SOUND:NO]/'        /opt/df/data/init/init_default.txt

# Set seasonal autosave in d_init_default.txt.
RUN sed -i 's/^\[AUTOSAVE:.*\]/[AUTOSAVE:SEASONAL]/' /opt/df/data/init/d_init_default.txt

VOLUME ["/save"]
WORKDIR /opt/df
