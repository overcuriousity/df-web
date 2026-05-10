#!/usr/bin/env bash
# ==============================================================================
# Dwarf Fortress Classic Linux Installer
# Supports: Debian/Ubuntu/Mint/Pop!_OS, Fedora/RHEL/Rocky/Alma,
#           Arch/Manjaro, openSUSE, Void, Alpine, Gentoo, and derivatives
# Optional: DFHack (auto-detects latest compatible release from GitHub)
#
# Env vars:
#   DF_VERSION=50_11   override default version without interactive prompt
# ==============================================================================

set -euo pipefail

# --- Colors ---
RED='\e[1;31m'; GREEN='\e[1;32m'; YELLOW='\e[1;33m'; BLUE='\e[1;34m'; NC='\e[0m'

log()  { printf "${BLUE}[INFO]${NC}  %s\n"   "$*"; }
warn() { printf "${YELLOW}[WARN]${NC}  %s\n" "$*"; }
ok()   { printf "${GREEN}[OK]${NC}    %s\n"  "$*"; }
die()  { printf "${RED}[ERROR]${NC} %s\n"    "$*" >&2; exit 1; }

# ask <varname> <prompt> <default>  — reads into named variable, falls back to default
ask() {
    local -n _ask_ref="$1"
    local __tmp
    read -r -p "$2 [Default: $3]: " __tmp || true
    _ask_ref="${__tmp:-$3}"
}

# yesno <prompt> <default-char>  — returns 0 for yes, 1 for no
yesno() {
    local __ans
    read -r -p "$1 " __ans || true
    [[ "${__ans:-${2:0:1}}" =~ ^[Yy]$ ]]
}

# --- OS detection ---

os_family() {
    [[ -f /etc/os-release ]] || die "/etc/os-release not found — cannot detect OS."
    local id like
    id=$(. /etc/os-release  && echo "${ID:-unknown}")
    like=$(. /etc/os-release && echo "${ID_LIKE:-}")

    case "$id" in
        ubuntu|debian|linuxmint|pop-os|neon|elementary|zorin|kali|parrot|raspbian|deepin|trisquel)
                        echo debian; return ;;
        fedora)         echo fedora; return ;;
        rhel|centos|rocky|almalinux|ol|scientific)
                        echo rhel;   return ;;
        arch|manjaro|endeavouros|garuda|artix|cachyos)
                        echo arch;   return ;;
        opensuse*|sles|sled)
                        echo suse;   return ;;
        void)           echo void;   return ;;
        alpine)         echo alpine; return ;;
        gentoo)         echo gentoo; return ;;
    esac
    # Fallback: check ID_LIKE for derivatives not in the list above
    case "$like" in
        *debian*|*ubuntu*) echo debian ;;
        *fedora*)          echo fedora ;;
        *rhel*|*centos*)   echo rhel   ;;
        *arch*)            echo arch   ;;
        *suse*)            echo suse   ;;
        *)                 echo unknown;;
    esac
}

# --- Dependency installation ---

install_deps() {
    log "Installing dependencies (may require sudo)..."
    case "$1" in
        debian)
            sudo apt-get update -qq
            sudo apt-get install -y wget tar bzip2 \
                libsdl2-2.0-0 libsdl2-image-2.0-0 libglu1-mesa libgtk2.0-0
            ;;
        fedora)
            sudo dnf install -y wget tar bzip2 SDL2 SDL2_image libGLU gtk2
            ;;
        rhel)
            sudo dnf install -y epel-release 2>/dev/null || true
            sudo dnf install -y wget tar bzip2 SDL2 SDL2_image libGLU gtk2
            ;;
        arch)
            sudo pacman -Sy --noconfirm wget tar bzip2 sdl2 sdl2_image glu gtk2
            ;;
        suse)
            sudo zypper --non-interactive install wget tar bzip2 \
                libSDL2-2_0-0 libSDL2_image-2_0-0 libGLU1 gtk2
            ;;
        void)
            sudo xbps-install -Sy wget tar bzip2 SDL2 SDL2_image libglu gtk+2
            ;;
        alpine)
            sudo apk add --no-cache wget tar bzip2 sdl2 sdl2_image mesa-gl gtk+2.0
            ;;
        gentoo)
            sudo emerge --ask=n media-libs/libsdl2 media-libs/sdl2-image \
                media-libs/glu x11-libs/gtk+:2
            ;;
        *)
            warn "Unknown distro. Ensure SDL2, SDL2_image, GLU, and GTK2 are installed."
            ;;
    esac
}

# --- Checksum verification ---

verify_sha256() {
    local file="$1" expected="$2" actual
    if command -v sha256sum &>/dev/null; then
        actual=$(sha256sum "$file" | awk '{print $1}')
    elif command -v shasum &>/dev/null; then
        actual=$(shasum -a 256 "$file" | awk '{print $1}')
    else
        warn "No sha256sum/shasum available — skipping verification."
        return 0
    fi
    [[ "$actual" == "$expected" ]] \
        || die "Checksum mismatch!\n  Expected: $expected\n  Got:      $actual"
    ok "Checksum verified."
}

# --- DFHack helpers ---

# Convert underscore version format to dot format: 50_11 → 50.11
df_dotver() { echo "${1/_/.}"; }

# Query GitHub API for the latest DFHack Linux 64-bit asset URL for a given DF version.
# Tries python3 first, then falls back to curl+jq.
find_dfhack_url() {
    local df_ver="$1"
    local api="https://api.github.com/repos/DFHack/dfhack/releases?per_page=30"

    if command -v python3 &>/dev/null; then
        python3 - "$df_ver" "$api" <<'PYEOF'
import urllib.request, json, sys
df_ver, api_url = sys.argv[1], sys.argv[2]
req = urllib.request.Request(api_url, headers={"User-Agent": "df-installer/2.0"})
try:
    with urllib.request.urlopen(req, timeout=10) as r:
        releases = json.load(r)
except Exception:
    sys.exit(1)
for release in releases:
    tag = release.get("tag_name", "")
    if not (tag.startswith(df_ver + "-") or tag == df_ver):
        continue
    for asset in release.get("assets", []):
        n = asset["name"]
        if "Linux" in n and "64bit" in n and n.endswith(".tar.bz2"):
            print(asset["browser_download_url"])
            sys.exit(0)
sys.exit(1)
PYEOF
        return
    fi

    if command -v curl &>/dev/null && command -v jq &>/dev/null; then
        curl -fsSL -H "User-Agent: df-installer/2.0" "$api" 2>/dev/null \
        | jq -r --arg v "$df_ver" '
            .[] | select(.tag_name | startswith($v + "-") or . == $v)
            | .assets[] | select(
                .name | (test("Linux") and test("64bit") and endswith(".tar.bz2"))
              )
            | .browser_download_url' \
        | head -1
        return
    fi

    return 1
}

install_dfhack() {
    local install_dir="$1" df_ver_underscore="$2" tmp_dir="$3"
    local df_ver dfhack_url dfhack_archive

    df_ver=$(df_dotver "$df_ver_underscore")
    log "Looking up latest DFHack release for DF $df_ver..."

    dfhack_url=$(find_dfhack_url "$df_ver" 2>/dev/null) || true

    if [[ -z "${dfhack_url:-}" ]]; then
        warn "Could not auto-detect DFHack URL."
        warn "Download manually from: https://github.com/DFHack/dfhack/releases"
        warn "Extract into: $install_dir"
        return 0
    fi

    log "Downloading DFHack from $dfhack_url..."
    dfhack_archive="$tmp_dir/dfhack.tar.bz2"
    if ! wget --no-verbose --show-progress -O "$dfhack_archive" "$dfhack_url"; then
        warn "DFHack download failed. Install manually: https://github.com/DFHack/dfhack/releases"
        return 0
    fi

    log "Extracting DFHack into $install_dir..."
    tar -xjf "$dfhack_archive" -C "$install_dir" \
        || { warn "DFHack extraction failed. Try installing manually."; return 0; }

    ok "DFHack installed."
}

# =============================================================================
# MAIN
# =============================================================================

echo "======================================================="
echo " Dwarf Fortress Classic Linux Installer"
echo "======================================================="
echo ""

# --- Version ---
DEFAULT_VER="${DF_VERSION:-53_12}"
while true; do
    ask DF_VER "DF version (e.g., 50_11)" "$DEFAULT_VER"
    [[ "$DF_VER" =~ ^[0-9]+_[0-9]+$ ]] && break
    warn "Version must be in NN_NN format (e.g., 50_11). Got: '$DF_VER'"
done

# --- Install path ---
DEFAULT_PATH="$HOME/.local/share/dwarf-fortress"
ask INSTALL_DIR "Installation directory" "$DEFAULT_PATH"
INSTALL_DIR="${INSTALL_DIR/#\~/$HOME}"   # expand leading tilde

# --- Optional DFHack ---
INSTALL_DFHACK=false
echo ""
if yesno "Install DFHack? (recommended — adds interface improvements and bugfixes) [Y/n]:" "Y"; then
    INSTALL_DFHACK=true
fi
echo ""

# --- Re-install guard ---
if [[ -d "$INSTALL_DIR" && -n "$(ls -A "$INSTALL_DIR" 2>/dev/null)" ]]; then
    warn "'$INSTALL_DIR' already exists and is not empty."
    if yesno "Overwrite existing installation? [y/N]:" "N"; then
        log "Removing existing installation..."
        rm -rf "$INSTALL_DIR"
    else
        log "Aborting — existing installation preserved."
        exit 0
    fi
fi

# --- Detect OS and install dependencies ---
log "Detecting OS..."
FAMILY=$(os_family)
DETECTED_ID=$(. /etc/os-release 2>/dev/null && echo "${ID:-unknown}")
log "Detected: $DETECTED_ID (family: $FAMILY)"
install_deps "$FAMILY"

# --- Temp workspace (cleaned up on exit) ---
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

mkdir -p "$INSTALL_DIR"

# --- Download Dwarf Fortress ---
BASE_URL="https://www.bay12games.com/dwarves"
ARCHIVE_NAME="df_${DF_VER}_linux.tar.bz2"
URL="$BASE_URL/$ARCHIVE_NAME"
ARCHIVE="$TMP_DIR/$ARCHIVE_NAME"

log "Downloading Dwarf Fortress v${DF_VER} ..."
wget --no-verbose --show-progress -O "$ARCHIVE" "$URL" \
    || die "Download failed. Does version '${DF_VER}' exist at bay12games.com?"

# --- Checksum verification ---
SUM_FILE="$TMP_DIR/df.sha256"
if wget -q -O "$SUM_FILE" "${URL}.sha256" 2>/dev/null && [[ -s "$SUM_FILE" ]]; then
    EXPECTED_SUM=$(awk '{print $1}' "$SUM_FILE")
    verify_sha256 "$ARCHIVE" "$EXPECTED_SUM"
else
    warn "No checksum file available from bay12games.com — skipping verification."
fi

# --- Unpack ---
log "Unpacking archive..."
UNPACK_DIR="$TMP_DIR/unpack"
mkdir -p "$UNPACK_DIR"

# Peek at archive structure to determine top-level layout before extracting
TOP_ENTRY=$(tar -tjf "$ARCHIVE" | head -1 | cut -d/ -f1)
tar -xjf "$ARCHIVE" -C "$UNPACK_DIR" \
    || die "Failed to extract archive."

shopt -s dotglob nullglob
if [[ -d "$UNPACK_DIR/$TOP_ENTRY" ]]; then
    mv "$UNPACK_DIR/$TOP_ENTRY"/* "$INSTALL_DIR/"
else
    mv "$UNPACK_DIR"/* "$INSTALL_DIR/"
fi
shopt -u dotglob nullglob

# --- Detect game binary (name varies across releases) ---
DF_BINARY=""
for candidate in dwarfort df; do
    [[ -f "$INSTALL_DIR/$candidate" ]] && { DF_BINARY="$candidate"; break; }
done
[[ -n "$DF_BINARY" ]] \
    || die "Game binary not found in $INSTALL_DIR (expected 'dwarfort' or 'df')."
chmod +x "$INSTALL_DIR/$DF_BINARY"
log "Game binary: $DF_BINARY"

# --- Optional DFHack ---
if [[ "$INSTALL_DFHACK" == true ]]; then
    install_dfhack "$INSTALL_DIR" "$DF_VER" "$TMP_DIR"
fi

# --- Create launcher wrapper ---
BIN_DIR="$HOME/.local/bin"
mkdir -p "$BIN_DIR"
WRAPPER="$BIN_DIR/dwarf-fortress"

log "Creating launcher at $WRAPPER..."
cat > "$WRAPPER" <<WRAPPER_SCRIPT
#!/usr/bin/env bash
cd "$INSTALL_DIR"
exec env LD_LIBRARY_PATH="$INSTALL_DIR\${LD_LIBRARY_PATH:+:\$LD_LIBRARY_PATH}" \
    "./$DF_BINARY" "\$@"
WRAPPER_SCRIPT
chmod +x "$WRAPPER"
ok "Launcher created."

# --- PATH check — offer to add BIN_DIR if missing ---
if ! printf '%s\n' "${PATH//:/$'\n'}" | grep -qxF "$BIN_DIR"; then
    warn "$BIN_DIR is not in your PATH."
    SHELL_NAME=$(basename "${SHELL:-bash}")
    case "$SHELL_NAME" in
        bash) RC_FILE="$HOME/.bashrc" ;;
        zsh)  RC_FILE="$HOME/.zshrc"  ;;
        fish) RC_FILE="$HOME/.config/fish/config.fish" ;;
        *)    RC_FILE="$HOME/.profile" ;;
    esac
    if yesno "Add $BIN_DIR to PATH in $RC_FILE? [Y/n]:" "Y"; then
        mkdir -p "$(dirname "$RC_FILE")"
        if [[ "$SHELL_NAME" == fish ]]; then
            echo "fish_add_path '$BIN_DIR'" >> "$RC_FILE"
        else
            printf '\nexport PATH="%s:$PATH"\n' "$BIN_DIR" >> "$RC_FILE"
        fi
        ok "Added to $RC_FILE — restart your shell or: source $RC_FILE"
    else
        warn "Add manually: export PATH=\"$BIN_DIR:\$PATH\""
    fi
fi

# --- Summary ---
echo ""
echo "======================================================="
ok "Installation complete!"
echo ""
printf "  Installed to: ${YELLOW}%s${NC}\n"  "$INSTALL_DIR"
printf "  Launch with:  ${YELLOW}dwarf-fortress${NC}\n"
[[ "$INSTALL_DFHACK" == true ]] && printf "  DFHack:       ${GREEN}installed${NC}\n"
echo "======================================================="
