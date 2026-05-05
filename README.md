# df-web

Play Dwarf Fortress Classic in a browser with session persistence, multi-user support, and three auth methods.

## Features

- **Two render modes** — SDL (tilesets, full graphics via noVNC) or Text (terminal via ttyd/xterm.js). Users choose on the landing page; SDL is the default.
- **Three auth methods** — secret token URL, WebAuthn passkey/YubiKey, or OIDC (Nextcloud).
- **Session persistence** — saves survive browser closes and idle timeouts. The game process stays alive between disconnects in text mode; SDL mode auto-saves seasonally.
- **Per-user isolation** — each player gets their own save directory; containers share no state.
- **DoS protection** — configurable concurrent session cap and per-container CPU/memory limits.
- **Admin-managed tilesets** — baked into the container image; no remote filesystem access for players.

## Prerequisites

- Linux server with Docker ≥ 24 and Docker Compose v2
- [Dwarf Fortress Classic](https://www.bay12games.com/dwarves/) Linux build (not included — see below)
- `novnc` package installed on the **host** (for SDL mode): `apt install novnc`
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
# Generate a cookie signing key
openssl rand -hex 32

# Edit session-manager/config.yml — fill in cookie_key, rp_id, rp_origins
# (and oidc_* fields if using Nextcloud auth)
$EDITOR session-manager/config.yml
```

### 3. Provision users

```bash
# Creates /srv/df/users/<uid>/save/ and prints a users.yml skeleton + secret token URL
sudo ./scripts/provision-user.sh alice "Alice"
# Add the printed entry to session-manager/users.yml
```

### 4. Build images

```bash
docker build -t df-image-base ./df-image-base
docker build -t df-image-sdl  ./df-image-sdl
docker build -t df-image-text ./df-image-text
```

### 5. Run

```bash
docker compose up -d
```

Point your reverse proxy at `http://127.0.0.1:8080`.

## Configuration

`session-manager/config.yml`:

| Key | Default | Description |
|-----|---------|-------------|
| `listen` | `127.0.0.1:8080` | Address the session manager binds to |
| `web_dir` | `../web` | Path to the `web/` directory |
| `novnc_dir` | `/usr/share/novnc` | Path to noVNC static files on the host |
| `saves_root` | `/srv/df/users` | Root directory for per-user save volumes |
| `image_sdl` | `df-image-sdl` | Docker image name for SDL mode |
| `image_text` | `df-image-text` | Docker image name for text mode |
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

## User Management

`session-manager/users.yml` is the allowlist. Only users listed here can log in.

```yaml
- uid: "alice"
  display_name: "Alice"
  token_hash: "<sha256 hex of raw token>"  # from provision-user.sh
  oidc_sub: ""                              # Nextcloud subject claim, if using OIDC
  passkeys: []                              # populated automatically on registration
  default_mode: "sdl"                       # "sdl" or "text"
```

To add a user: run `scripts/provision-user.sh <uid>`, copy the output into `users.yml`.
To revoke access: remove or comment out the entry. Changes are picked up on the next request.

### Registering a passkey / YubiKey

Passkey registration is admin-initiated to prevent self-registration:

```bash
# While logged in as the user (session cookie present), open in a browser:
https://df.example.com/auth/passkey/register/begin?uid=alice
# Then POST the response to /auth/passkey/register/finish?uid=alice
# The web UI's login page handles this flow automatically after first login via token.
```

## Tilesets

Place tileset PNG files in `df-image-base/tilesets/` and rebuild the base image:

```bash
cp MyTileset.png df-image-base/tilesets/
docker build -t df-image-base ./df-image-base
docker build -t df-image-sdl  ./df-image-sdl
```

Edit `df-image-base/init.txt` to set `[FONT:MyTileset.png]` and `[FULLFONT:MyTileset.png]`.

## Architecture

```
Browser → (your TLS reverse proxy) → 127.0.0.1:8080
                                           │
                                    Session Manager (Go)
                                    ├─ /auth/token      secret URL → cookie
                                    ├─ /auth/passkey/*  WebAuthn
                                    ├─ /auth/oidc/*     Nextcloud OIDC
                                    └─ /play            websocket proxy
                                           │
                           ┌───────────────┴───────────────┐
                     SDL container                    Text container
                     Xvfb + x11vnc                   ttyd + dtach
                     websockify + noVNC               xterm.js
                     DF (PRINT_MODE:STANDARD)         DF (PRINT_MODE:TEXT)
```

Each game container:
- Is started on demand, stopped after `idle_timeout` of inactivity
- Has its own bind-mounted save directory (`/srv/df/users/<uid>/save/`)
- Runs with a read-only root filesystem; only the save mount is writable
- Is attached only to the internal Docker network (no internet egress)
- Is limited to 1 CPU / 1 GB RAM / 256 PIDs

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

The code in this repository (session manager, Dockerfiles, web frontend) is MIT licensed. See [LICENSE](LICENSE).

Dwarf Fortress itself is **not included**. It is subject to [Bay 12 Games' license](https://www.bay12games.com/dwarves/). This infrastructure is intended for personal, non-commercial use.
