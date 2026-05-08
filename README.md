# df-web

Play Dwarf Fortress Classic in a browser with session persistence, multi-user support, and three auth methods.

> **Dwarf Fortress is the work of [Bay 12 Games](https://www.bay12games.com/dwarves/).**
> If you enjoy this wrapper, please buy the game on Steam or
> [donate directly to Bay 12](https://www.bay12games.com/support.html). Two
> decades of one of the deepest games ever made.

## Features

- **SDL render mode** — full graphics via noVNC (tilesets, mouse support).
- **Three auth methods** — string-key form, WebAuthn passkey/YubiKey, or OIDC (shown only when configured).
- **Session persistence** — saves survive browser closes; the game process stays alive between disconnects. On idle timeout the game is saved before the container stops.
- **Idle warning + keepalive** — `/play` shows a live "time until disconnect" chip and pops a warning dialog with a one-click "keep playing" button before the idle reaper fires.
- **Hot save snapshot** — `/account` has a "Snapshot saves" button that downloads a tar.gz of the user's save dir without stopping the running game container, alongside the existing stop-and-export flow.
- **Per-user isolation** — each player gets their own save directory; containers share no state.
- **DoS protection** — configurable concurrent session cap and per-container CPU/memory limits.
- **Admin-managed tilesets** — baked into the container image; no remote filesystem access for players.
- **Storyteller sidebar** — per-fortress markdown journal, live announcement feed (gamelog.txt), legends XML viewer. Always available, no third-party tools required.
- **DFHack integration** (optional) — in-game `manipulator` labor screen + in-browser labor panel. Opt-in at build time; vanilla DF works without it.

## Prerequisites

- Linux server with Docker ≥ 24 and Docker Compose v2
- [Dwarf Fortress Classic](https://www.bay12games.com/dwarves/) Linux build (not included — see below)
- A reverse proxy (nginx, Caddy, etc.) in front handling TLS — this service binds to `127.0.0.1:8080`

## Quick Start

### 1. Obtain Dwarf Fortress

Download the Linux classic build from [bay12games.com](https://www.bay12games.com/dwarves/) and extract it:

```bash
tar -xjf df_53.12_linux.tar.bz2 -C df-image-base/df --strip-components=1
```

`df-image-base/df/` must contain `dwarfort`, `data/`, `raw/`, etc.

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

### 4. Provision users

```bash
cp session-manager/users.yml.example session-manager/users.yml

# Creates /srv/df/users/<uid>/{data,config}/, appends the entry to users.yml, and prints the access key.
# Run on the host (not inside any container).
sudo ./scripts/provision-user.sh alice "Alice"

# Share the printed access key with the user out-of-band (treat it like a password)
```

### 5. Build images

**Vanilla (no DFHack):**
```bash
docker build -t df-image-base ./df-image-base
docker build -t df-image-sdl  ./df-image-sdl
```

**With DFHack** (optional — enables in-game `manipulator` + web labor panel):
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

After pulling new commits, rebuild and redeploy:

```bash
git pull
docker compose build --no-cache
docker compose up -d
```

Use `--no-cache` whenever Dockerfiles change (including base image layers). Running the old image after a fix to image-layer behaviour (e.g. save directory wiring) will silently keep the bug active.

## Configuration

`session-manager/config.yml`:

| Key | Default | Description |
|-----|---------|-------------|
| `listen` | `127.0.0.1:8080` | Address the session manager binds to |
| `web_dir` | `../web` | Path to the `web/` directory |
| `saves_root` | `/srv/df/users` | Root directory for per-user save volumes |
| `image_sdl` | `df-image-sdl` | Docker image name for SDL mode |
| `docker_network` | `df_internal` | Docker network for game containers |
| `idle_timeout` | `30m` | Inactivity time before container is stopped and game is saved |
| `max_sessions` | `5` | Maximum concurrent game containers |
| `cookie_key` | — | **Required.** 64 hex chars (32 bytes). Generate: `openssl rand -hex 32` |
| `rp_id` | — | WebAuthn relying party hostname, e.g. `df.example.com` |
| `rp_display_name` | — | Human name shown in passkey dialogs |
| `rp_origins` | — | List of allowed WebAuthn origins, e.g. `["https://df.example.com"]` |
| `oidc_issuer` | — | Nextcloud base URL, e.g. `https://cloud.example.com` |
| `oidc_client_id` | — | OIDC client ID from Nextcloud |
| `oidc_client_secret` | — | OIDC client secret |
| `oidc_redirect_uri` | — | Callback URL, e.g. `https://df.example.com/auth/oidc/callback` |
| `dfhack_enabled` | `false` | Set `true` only when `df-image-sdl` was built with `--build-arg DFHACK_VERSION=…`. Enables the web labor panel and manipulator hint. |

## User Management

`session-manager/users.yml` is the allowlist. Only users listed here can log in.

```yaml
- uid: "alice"
  display_name: "Alice"
  token_hash: "<hex SHA-256 of the raw access key>"  # from provision-user.sh
  oidc_sub: ""       # OIDC subject claim — leave blank if not using OIDC
  passkeys: []       # populated automatically when user self-enrolls at /account
```

To add a user: run `scripts/provision-user.sh <uid> "Display Name"` — it appends the entry to `users.yml` and prints the access key.

To rotate a forgotten/leaked key: run `scripts/provision-user.sh --rotate <uid>` — it replaces only the `token_hash` (passkeys and `oidc_sub` are preserved) and prints a fresh key. Then reload the session manager so the new hash takes effect:

```bash
docker compose kill -s SIGHUP session-manager
```

`SIGHUP` re-reads `users.yml` without bouncing live game sessions; use `docker compose restart session-manager` if you'd rather force everyone to re-auth.

To revoke access: remove or comment out the entry, then send `SIGHUP` (or restart) as above.

### Auth methods

**String-key** — the default. The user pastes their access key into the form on `/`. The key is hashed (SHA-256) before comparison; the hash is what's stored in `users.yml`. Treat the raw key like a password.

**Passkey / YubiKey** — self-enrollment flow:
1. User logs in once with their string-key.
2. User visits `/account` and clicks "Register a passkey or security key".
3. Browser/OS guides through the WebAuthn ceremony (platform authenticator, YubiKey, etc.).
4. `users.yml` is updated automatically with the new credential.
5. On future visits the user clicks "Login with Passkey / YubiKey" on the login page — no key entry needed.

**OIDC (single sign-on)** — set `oidc_issuer`, `oidc_client_id`, `oidc_client_secret`, and `oidc_redirect_uri` in `config.yml`. The SSO button appears on the login page automatically when `oidc_issuer` is non-empty. Pre-create users in `users.yml` with their `oidc_sub` claim populated.

**Session cookie** — the `dfsess` cookie is HMAC-SHA-256 signed with `cookie_key`. It is not encrypted; the UID is readable in the cookie value. Set `insecure_cookie: true` only for HTTP-only local development; never in production.

## Tilesets

Place tileset PNG files in `df-image-base/tilesets/` and add `sed` lines to `df-image-base/Dockerfile` to apply them, then rebuild:

```dockerfile
# in df-image-base/Dockerfile, after the existing sed block:
RUN sed -i 's/^\[FONT:.*\]/[FONT:MyTileset.png]/'         /opt/df/data/init/init_default.txt \
 && sed -i 's/^\[FULLFONT:.*\]/[FULLFONT:MyTileset.png]/' /opt/df/data/init/init_default.txt
```

```bash
docker build -t df-image-base ./df-image-base
docker build -t df-image-sdl  ./df-image-sdl
```

## Architecture

```
Browser → (your TLS reverse proxy) → 127.0.0.1:8080
                                           │
                                    Session Manager (Go)
                                    ├─ /             login page
                                    ├─ /account      passkey self-enrollment
                                    ├─ /auth/token   string-key → cookie
                                    ├─ /auth/passkey/* WebAuthn
                                    ├─ /auth/oidc/*  OIDC (when configured)
                                    └─ /play         websocket proxy → container
                                           │
                                     SDL container
                                     Xvfb + x11vnc
                                     websockify + noVNC
                                     DF (SDL2)
                                     DFHack (optional)
```

Each game container:
- Is started on demand, stopped after `idle_timeout` of inactivity
- Has its own bind-mounted save + config directories under `/srv/df/users/<uid>/`:
  - `data/`   → `~/.local/share/Bay 12 Games/Dwarf Fortress/` (saves, world data)
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
