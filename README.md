# df-web

Play Dwarf Fortress Classic in a browser with session persistence, multi-user support, three auth methods, and an in-browser storyteller sidebar (journal, live announcements, optional DFHack labor panel).

> **Dwarf Fortress is the work of [Bay 12 Games](https://www.bay12games.com/dwarves/).**
> If you enjoy this wrapper, please buy the game on Steam or
> [donate directly to Bay 12](https://www.bay12games.com/support.html). Two
> decades of one of the deepest games ever made.

## Features

- **SDL render mode** — full graphics via noVNC (tilesets, mouse support).
- **Audio streaming** — DF's PulseAudio output is piped to the browser as Opus/WebM, started/stopped from the in-game sidebar.
- **Three auth methods** — string-key form, WebAuthn passkey/YubiKey, or OIDC SSO (each shown only when configured).
- **Session persistence** — saves survive browser closes; the game process stays alive between disconnects. The wrapper does not trigger in-game saves — save in DF yourself; on idle timeout or explicit stop, the container is stopped and only your last in-game save is preserved.
- **Idle warning + keepalive** — `/play` shows a live "time until disconnect" chip and pops a warning dialog with a one-click "keep playing" button before the idle reaper fires, giving you time to save in-game.
- **Stop on logout / explicit stop** — logging out or clicking *Stop session* shuts down the user's container. Same flow as idle timeout: only your last in-game save survives.
- **Hot save snapshot + import** — `/account` has a "Snapshot saves" button that downloads a tar.gz of the user's save dir without stopping the running game container, plus a "Stop and export" flow and an "Import saves" upload (tar.gz of region directories).
- **Per-user isolation** — each player gets their own save directory; containers share no state.
- **DoS protection** — configurable concurrent session cap and per-container CPU/memory/PID limits.
- **Per-user tilesets** — users can upload their own PNG tilesets at `/account` (validated, size- and count-limited) and choose an active one; applied at container spawn. Operators can still bake tilesets into the image as the default.
- **Storyteller sidebar** — per-fortress markdown journal, live announcement feed (gamelog.txt). Always available, no third-party tools required.
- **DFHack integration** (optional) — when the image is built with DFHack, the in-game `manipulator` labor screen autoloads and a "Dwarves" tab appears in the `/play` sidebar with a labor panel backed by `dfhack-run`. Vanilla DF works without it.
- **Admin web UI** — admins get an `/admin` page to list users, create accounts, rotate access keys, and delete users (which stops the running container and removes the save dir). The admin flag itself is set only via the host-side script — never via the web UI.

## Prerequisites

- Linux server with Docker ≥ 24 and Docker Compose v2
- `sqlite3` on the host (used by `provision-user.sh`)
- [Dwarf Fortress Classic](https://www.bay12games.com/dwarves/) Linux build (not included — see below)
- A reverse proxy (nginx, Caddy, etc.) in front handling TLS — this service binds to `127.0.0.1:8080`

## Quick Start

### 1. Obtain Dwarf Fortress

Download the Linux classic build from [bay12games.com](https://www.bay12games.com/dwarves/) and extract it:

```bash
mkdir -p df-image-base/df
tar -xjf df_53.12_linux.tar.bz2 -C df-image-base/df --strip-components=1
```

`tar -C` does not auto-create the destination, so the `mkdir` is required on a fresh clone. `df-image-base/df/` must end up containing `dwarfort`, `data/`, `raw/`, etc.

### 2. Configure

```bash
cp session-manager/config.yml.example session-manager/config.yml

# Fill in cookie_key (required), rp_id, rp_origins.
# Set oidc_* only if using OIDC single sign-on.
$EDITOR session-manager/config.yml
```

`cookie_key` must be 64 hex characters: `openssl rand -hex 32`

### 3. Create the saves root on the host

The directory must exist on a persistent filesystem **before** starting the stack. The session manager will refuse to start if it is missing.

```bash
sudo install -d -o root -g root -m 0755 /srv/df/users
```

> **Important:** always create this on the host directly, never via `docker exec` or `docker compose exec`. Commands run inside the session-manager container operate inside that container's filesystem; even with the bind-mount, getting the paths and ownership right is fragile that way.

### 4. Provision the bootstrap admin and any initial users

User records live in `session-manager/users.db` (SQLite). The provisioning script creates the on-disk directories, inserts the row, and prints the raw access key.

```bash
# Bootstrap admin (gets the is_admin flag — needed to use /admin).
sudo ./scripts/provision-user.sh --admin alice "Alice"

# Regular users.
sudo ./scripts/provision-user.sh bob "Bob"
```

Share each printed access key with the user out-of-band; treat it like a password. Subsequent users can also be created from the admin UI once the bootstrap admin can log in.

Changes take effect immediately — no daemon reload required.

### 5. Build images

**Vanilla (no DFHack):**
```bash
docker build -t df-image-base ./df-image-base
docker build -t df-image-sdl  ./df-image-sdl
```

**With DFHack** (optional — enables the in-game `manipulator` and unlocks the `/therapist` page):
```bash
docker build -t df-image-base ./df-image-base
docker build -t df-image-sdl --build-arg DFHACK_VERSION=53.12-r1 ./df-image-sdl
```

Then set `dfhack_enabled: true` in `session-manager/config.yml`.

> DFHack is not part of this repository and is not required. When `DFHACK_VERSION` is
> set, the Dockerfile downloads the matching release tarball from
> [github.com/DFHack/dfhack](https://github.com/DFHack/dfhack) at build time.
> DFHack is copyright its respective contributors under the zlib license.
> See [DFHack's license](https://github.com/DFHack/dfhack/blob/develop/LICENSE)
> for terms of use.

### 6. Run

```bash
docker compose up -d
```

Point your reverse proxy at `http://127.0.0.1:8080`.

## Updating

`docker-compose.yml` only owns the `session-manager` image. The two game images (`df-image-base`, `df-image-sdl`) are built outside compose and are therefore **not** rebuilt by `docker compose build`. After pulling, rebuild whichever images the new commits touched:

```bash
git pull

# Always: rebuild and restart the session manager.
docker compose build --no-cache session-manager
docker compose up -d

# Only if df-image-base/ or its tilesets changed:
docker build -t df-image-base ./df-image-base

# Only if df-image-sdl/ changed (Dockerfile, s6/, hack/scripts/, dfhack-config/, …).
# Use the --build-arg you used at install time, or drop it for vanilla.
docker build -t df-image-sdl --build-arg DFHACK_VERSION=53.12-r1 ./df-image-sdl
```

Use `--no-cache` whenever Dockerfiles change (including base image layers). Running the old image after a fix to image-layer behaviour (e.g. save directory wiring or DFHack scripts) will silently keep the bug active.

Already-running game containers continue with the **old** `df-image-sdl` until the user stops their session and starts a new one. To force everyone onto the new image immediately, stop their containers (`docker rm -f $(docker ps -q --filter name=df-)`) — the next `/play` will spawn fresh; in-game saves persist via the bind-mounted data dir.

## Configuration

`session-manager/config.yml`:

| Key | Default | Description |
|-----|---------|-------------|
| `listen` | `127.0.0.1:8080` | Address the session manager binds to |
| `web_dir` | `../web` | Path to the `web/` directory |
| `saves_root` | `/srv/df/users` | Root directory for per-user save volumes |
| `image_sdl` | `df-image-sdl` | Docker image name for SDL mode |
| `docker_network` | `df_internal` | Docker network for game containers |
| `idle_timeout` | `30m` | Inactivity time before the container is stopped (only the last in-game save is preserved) |
| `max_sessions` | `5` | Maximum concurrent game containers |
| `cookie_key` | — | **Required.** 64 hex chars (32 bytes). Generate: `openssl rand -hex 32` |
| `rp_id` | — | WebAuthn relying party hostname, e.g. `df.example.com` |
| `rp_display_name` | — | Human name shown in passkey dialogs |
| `rp_origins` | — | List of allowed WebAuthn origins, e.g. `["https://df.example.com"]` |
| `oidc_issuer` | — | Issuer base URL (e.g. Nextcloud `https://cloud.example.com`); leave blank to disable OIDC |
| `oidc_client_id` | — | OIDC client ID |
| `oidc_client_secret` | — | OIDC client secret |
| `oidc_redirect_uri` | — | Callback URL, e.g. `https://df.example.com/auth/oidc/callback` |
| `dfhack_enabled` | `false` | Set `true` only when `df-image-sdl` was built with `--build-arg DFHACK_VERSION=…`. Enables the web labor panel and manipulator hint. |
| `insecure_cookie` | `false` | Set `true` only for HTTP-only local development; never in production |

## User Management

User accounts live in `session-manager/users.db` (SQLite). There are two ways to manage them: the host-side `provision-user.sh` script and the admin web UI. Both write to the same database; changes take effect immediately, no daemon reload required.

### Host-side script (`scripts/provision-user.sh`)

Required for anything that grants persistent privilege (the admin flag) — the web UI deliberately can't promote, demote, or create admins, so a compromised admin session can't escalate or hand out admin permanently.

```bash
# Create a regular user (prints the raw access key).
sudo ./scripts/provision-user.sh <uid> "Display Name"

# Create the bootstrap admin.
sudo ./scripts/provision-user.sh --admin <uid> "Display Name"

# Rotate a forgotten/leaked key (preserves passkeys, oidc_sub, display_name).
sudo ./scripts/provision-user.sh --rotate <uid>

# Toggle admin on an existing user.
sudo ./scripts/provision-user.sh --promote <uid>
sudo ./scripts/provision-user.sh --demote <uid>
```

UIDs must be 1-32 chars, lowercase alphanumeric / underscore / dash, starting alphanumeric (they become container names, filesystem paths, and SQL values).

### Admin web UI (`/admin`)

Available to any user whose row has `is_admin = 1`. From the page, an admin can:

- List all users (with their auth methods and whether they currently have an active container)
- Create a new regular user (no admin flag — script-only)
- Rotate any user's access key
- Delete a user — stops their running container, drops the DB row, and removes their save directory

You cannot delete your own account from the UI.

### Auth methods

**String-key** — the default. The user pastes their access key into the form on `/`. The key is hashed (SHA-256) before comparison; the hash is what's stored in `users.db`. Treat the raw key like a password.

**Passkey / YubiKey** — self-enrollment flow:
1. User logs in once with their string-key.
2. User visits `/account` and clicks "Register a passkey or security key".
3. Browser/OS guides through the WebAuthn ceremony (platform authenticator, YubiKey, etc.).
4. The credential is stored in `users.db` automatically.
5. On future visits the user clicks "Login with Passkey / YubiKey" on the login page — no key entry needed.

**OIDC (single sign-on)** — set `oidc_issuer`, `oidc_client_id`, `oidc_client_secret`, and `oidc_redirect_uri` in `config.yml`. The SSO button appears on the login page automatically when `oidc_issuer` is non-empty. Pre-create users with the `provision-user.sh` script, then have an admin populate the user's `oidc_sub` claim from the admin UI or directly in the DB.

**Session cookie** — the `dfsess` cookie is HMAC-SHA-256 signed with `cookie_key`. It is not encrypted; the UID is readable in the cookie value. Do not set `insecure_cookie: true` in production.

## Tilesets

Two paths, used together if you want:

**Operator-baked default.** Place tileset PNGs in `df-image-base/tilesets/` and add `sed` lines to `df-image-base/Dockerfile` to apply them, then rebuild. This sets the default font for everyone.

```dockerfile
# in df-image-base/Dockerfile, after the existing sed block:
RUN sed -i 's/^\[FONT:.*\]/[FONT:MyTileset.png]/'         /opt/df/data/init/init_default.txt \
 && sed -i 's/^\[FULLFONT:.*\]/[FULLFONT:MyTileset.png]/' /opt/df/data/init/init_default.txt
```

```bash
docker build -t df-image-base ./df-image-base
docker build -t df-image-sdl  ./df-image-sdl
```

**Per-user upload.** Each user can upload their own PNG tilesets at `/account` (max 4 MiB per file, 20 files per user, PNG signature checked, filenames restricted to a safe charset) and pick one as their active tileset. The selection is recorded on the user row; the spawn-time `apply-tilesets.sh` hook copies the user's tilesets into `data/art/` and patches `init.txt` before DF starts.

## Storyteller sidebar

Always available in `/play`, no DFHack required:

- **Journal** — per-fortress markdown notes, persisted in the user's save directory.
- **Announcements** — live tail of `gamelog.txt` from the running container.

For legends, use Dwarf Fortress's built-in legends viewer (Legends mode in DF) — the wrapper deliberately doesn't reimplement it.

## DFHack integration (optional)

DFHack is downloaded at image build time when `--build-arg DFHACK_VERSION=<ver>` is passed and is not redistributed by this repo. When enabled (config: `dfhack_enabled: true`), the SDL container:

- Autoloads `enable manipulator` (in-game Therapist-style labor screen) via `dfhack-config/init/dfhack.init`.
- Exposes the DFHack remote API on port 5000 inside the Docker network.
- Ships Lua scripts in `df-image-sdl/hack/scripts/` that the session-manager invokes via `dfhack-run`:
  - `web-units.lua` — slim citizen roster for the `/play` sidebar
  - `web-units-full.lua` — full Dwarf-Therapist payload (enums, roles, skills, attributes, labors, traits, needs, health, squad)
  - `web-animals.lua` — tame/owned creatures roster
  - `web-setlabor.lua` — toggle one labor on one dwarf
  - `web-commit.lua` — apply a batch of labor / nickname / custom-profession changes

The session-manager talks to DFHack with `docker exec <user-container> /opt/df/dfhack-run …` (`session-manager/dfhack_proxy.go`) and exposes:

- `GET /play/dfhack/units` · `GET /play/dfhack/units/full` · `GET /play/dfhack/animals`
- `POST /play/dfhack/labor` · `POST /play/dfhack/commit`
- `GET /therapist` — full-window Dwarf Therapist replica

The `/play` page shows a read-only "Dwarves" sidebar tab when `/session/capabilities` reports DFHack is on, with a link that opens `/therapist` in a new tab. The therapist page provides view tabs (Overview, Labors, Skills, Attributes, Roles, Social, Military, Health, Animals), grouping/sorting/filtering, a pending-changes queue with a single Commit step, custom-profession management (browser-side), a top-N optimizer, CSV export, and DT-style hotkeys (Ctrl+R refresh, Ctrl+T commit, Ctrl+E clear). DF 53.x's in-game **Work Details** system reasserts labor flags every tick, so per-labor toggles in the therapist may revert — use the page primarily for reading state, setting nicknames / custom professions, and informing in-game Work Details decisions.

## Architecture

```
Browser → (your TLS reverse proxy) → 127.0.0.1:8080
                                           │
                                    Session Manager (Go)
                                    ├─ /                    login page
                                    ├─ /account             passkey enrol, snapshot, export, import, tilesets
                                    ├─ /admin               admin UI (admins only)
                                    ├─ /auth/token          string-key → cookie
                                    ├─ /auth/passkey/*      WebAuthn
                                    ├─ /auth/oidc/*         OIDC (when configured)
                                    ├─ /auth/logout         clears cookie + stops user's container
                                    ├─ /play                websocket proxy → container (noVNC)
                                    ├─ /play/audio          audio stream from container
                                    ├─ /play/journal        per-fortress markdown notes
                                    ├─ /play/timeline       live gamelog.txt tail
                                    ├─ /play/dfhack/*       (when dfhack_enabled)
                                    ├─ /session/status      idle countdown
                                    ├─ /session/keepalive   reset idle timer
                                    ├─ /session/stop        explicit stop
                                    └─ /session/capabilities feature-flag probe for the frontend
                                           │
                                     SDL container (per user, on demand)
                                     ├─ Xvfb + x11vnc + websockify + noVNC
                                     ├─ PulseAudio + ffmpeg → /play/audio
                                     ├─ DF (SDL2)
                                     └─ DFHack (optional, port 5000 internal)
```

Each game container:

- Is started on demand, stopped after `idle_timeout` of inactivity, on explicit stop, or on logout
- Has its own bind-mounted save + config directories under `/srv/df/users/<uid>/`:
  - `data/`   → `~/.local/share/Bay 12 Games/Dwarf Fortress/` (saves, world data, gamelog.txt)
  - `config/` → `~/.config/Bay 12 Games/Dwarf Fortress/` (keybindings, init customisations)
- Is attached only to the internal Docker network (no internet egress)
- Is limited to 1 CPU / 4 GB RAM / 256 PIDs (DF worldgen on medium worlds can spike past 2 GB)

## Development

```bash
cd session-manager
go build ./...
go vet ./...
```

The session manager binary can be run directly (without Docker) for local development:

```bash
LISTEN=127.0.0.1:8080 ./session-manager
```

It will spawn real Docker containers when users connect, so Docker must be running and the images must be built.

## License

The code in this repository (session manager, Dockerfiles, web frontend, DFHack Lua scripts in `df-image-sdl/hack/scripts/`) is MIT licensed. See [LICENSE](LICENSE).

**Not included in this repository:**

- **Dwarf Fortress** — subject to [Bay 12 Games' license](https://www.bay12games.com/dwarves/). Must be downloaded separately (see Quick Start).
- **DFHack** — an optional third-party tool. When `DFHACK_VERSION` is passed at build time the Dockerfile downloads it from GitHub. DFHack is copyright its contributors; see its [license](https://github.com/DFHack/dfhack/blob/develop/LICENSE).

This infrastructure is intended for personal, non-commercial use.
