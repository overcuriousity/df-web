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

# Save directory is bind-mounted directly at /opt/df/data/save by the session
# manager. Reset the in-image dir to a clean empty mount point — DF (or its
# rename/atomic-replace save flow) was bypassing a previous symlink approach
# and silently writing into the container's writable layer, which is lost on
# `docker rm`. Mounting at the canonical path eliminates the indirection.
RUN rm -rf /opt/df/data/save && mkdir -p /opt/df/data/save

# Bake tilesets (admin-managed): copy PNGs into the art directory.
# Drop PNG files into df-image-base/tilesets/ to include them.
COPY tilesets/ /opt/df/data/art/

# Patch the shipped config files in place.
# DF 53.x reads init_default.txt and d_init_default.txt (not init.txt).
# WINDOWED:YES — needed for headless Xvfb rendering.
# SOUND:YES   — audio is captured by PulseAudio null sink and streamed to the browser.
RUN sed -i 's/^\[WINDOWED:.*\]/[WINDOWED:YES]/' /opt/df/data/init/init_default.txt \
 && sed -i 's/^\[SOUND:.*\]/[SOUND:YES]/'       /opt/df/data/init/init_default.txt

# Persistence settings in d_init_default.txt.
# AUTOSAVE:SEASONAL  — write every season change.
# AUTOSAVE_PAUSE:YES — also write whenever the user pauses; cheap insurance
#                     against an OOM-kill or container restart wiping a fortress.
# INITIAL_SAVE:YES   — write the world to disk immediately after embark, so a
#                     crash before the first season change doesn't lose everything.
# AUTOBACKUP:YES     — keep a "-prev" copy of each save.
RUN sed -i 's/^\[AUTOSAVE:.*\]/[AUTOSAVE:SEASONAL]/'             /opt/df/data/init/d_init_default.txt \
 && sed -i 's/^\[AUTOSAVE_PAUSE:.*\]/[AUTOSAVE_PAUSE:YES]/'      /opt/df/data/init/d_init_default.txt \
 && sed -i 's/^\[INITIAL_SAVE:.*\]/[INITIAL_SAVE:YES]/'          /opt/df/data/init/d_init_default.txt \
 && sed -i 's/^\[AUTOBACKUP:.*\]/[AUTOBACKUP:YES]/'              /opt/df/data/init/d_init_default.txt

# DF Classic reads init.txt and d_init.txt; the _default files are only
# templates copied on first run. Bake the patched copies in directly so our
# settings are authoritative regardless of which file DF picks up.
RUN cp /opt/df/data/init/init_default.txt   /opt/df/data/init/init.txt \
 && cp /opt/df/data/init/d_init_default.txt /opt/df/data/init/d_init.txt

VOLUME ["/opt/df/data/save"]
WORKDIR /opt/df
