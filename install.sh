#!/usr/bin/env bash
#
# TransferCLI installer (Debian / Ubuntu, systemd)
#
# Review before running as root:
#   curl -fsSL https://raw.githubusercontent.com/fintechcoding/transfercli/v2.0.0/install.sh -o install.sh
#   less install.sh
#   sudo bash install.sh
#
# Re-running the installer upgrades an existing installation in place. Existing credentials, settings
# and uploaded files are kept.
#
# Optional environment variables:
#   TC_DOMAIN            domain for the bundled nginx + Let's Encrypt setup (e.g. files.example.com)
#   TC_EMAIL             e-mail for Let's Encrypt (required with TC_DOMAIN)
#   TC_PUBLIC_URL        public URL when you run your own reverse proxy instead (e.g. https://files.example.com)
#   TC_ADMIN_USER        admin user name (default: admin)
#   TC_ADMIN_PASSWORD    admin password (default: random, generated on first install)
#   TC_PUBLIC_UPLOADS    1 = anyone can upload without a password (NOT recommended on the internet). Default 0.
#   TC_UPLOAD_USER       upload user name (default: upload)
#   TC_UPLOAD_PASSWORD   upload password (default: random, generated on first install)
#   TC_MAX_UPLOAD_MB     maximum upload size in MiB (default 0 = unlimited)
#   TC_RATE_LIMIT        requests per minute per client for transfer.sh (default 0 = off)
#   TC_PURGE_DAYS        auto-delete uploads after N days on first install (default: 30; 0 = never)
#   TC_TITLE             admin panel title (default: TransferCLI)
#   TC_PORT_UPLOAD       transfer.sh port on 127.0.0.1 (default: 8081)
#   TC_PORT_ADMIN        admin panel port on 127.0.0.1 (default: 8082)
#   TC_REPO              GitHub repository to install from (default: fintechcoding/transfercli)
#   TC_VERSION           release tag to install (default: v2.0.0)
#   TC_LOCAL_DIST        directory that already contains the release binaries + SHA256SUMS (offline install)
#   TC_BUILD_FROM_SOURCE 1 = build from the tagged source with a local Go toolchain instead of downloading
#   TC_QUIET             1 = do not print generated passwords (they are always written to /root/transfercli-credentials.txt)

set -euo pipefail
umask 022

# ---------- config ----------
REPO="${TC_REPO:-fintechcoding/transfercli}"
VERSION="${TC_VERSION:-v2.0.0}"
PORT_UPLOAD="${TC_PORT_UPLOAD:-8081}"
PORT_ADMIN="${TC_PORT_ADMIN:-8082}"
PURGE_DAYS="${TC_PURGE_DAYS:-30}"
ADMIN_USER="${TC_ADMIN_USER:-admin}"
ADMIN_PASSWORD="${TC_ADMIN_PASSWORD:-}"
UPLOAD_USER="${TC_UPLOAD_USER:-upload}"
UPLOAD_PASSWORD="${TC_UPLOAD_PASSWORD:-}"
PUBLIC_UPLOADS="${TC_PUBLIC_UPLOADS:-0}"
MAX_UPLOAD_MB="${TC_MAX_UPLOAD_MB:-0}"
RATE_LIMIT="${TC_RATE_LIMIT:-0}"
TITLE="${TC_TITLE:-TransferCLI}"
DOMAIN="${TC_DOMAIN:-}"
EMAIL="${TC_EMAIL:-}"
PUBLIC_URL="${TC_PUBLIC_URL:-}"
LOCAL_DIST="${TC_LOCAL_DIST:-}"
BUILD_FROM_SOURCE="${TC_BUILD_FROM_SOURCE:-0}"
QUIET="${TC_QUIET:-0}"

PREFIX=/opt/transfercli
DATA=/var/lib/transfercli
CONF_DIR=/etc/transfercli
ENV_FILE=/etc/transfercli.env                 # admin-editable (purge settings)
TSH_ENV=$CONF_DIR/transfersh.env              # root-owned transfer.sh settings (auth, limits)
ADMIN_ENV=/etc/transfercli-admin.env
ADMIN_HTPASSWD=$CONF_DIR/.htpasswd
UPLOAD_HTPASSWD=$CONF_DIR/upload.htpasswd
CRED_FILE=/root/transfercli-credentials.txt
MIN_GO=1.26

# ---------- helpers ----------
log()  { printf '\033[1;34m[*]\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m[+]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[!]\033[0m %s\n' "$*"; }
err()  { printf '\033[1;31m[x]\033[0m %s\n' "$*" >&2; exit 1; }

require_root() { [ "$(id -u)" -eq 0 ] || err "Must run as root. Try: sudo bash $0"; }

detect_os() {
    [ -f /etc/os-release ] || err "Cannot detect OS (no /etc/os-release)"
    # Read in a subshell: os-release defines VERSION, which would overwrite the release tag to install.
    local os_id os_like
    os_id="$(. /etc/os-release && echo "${ID:-}")"
    os_like="$(. /etc/os-release && echo "${ID_LIKE:-}")"
    case "$os_id" in
        debian|ubuntu) ;;
        *) case " $os_like " in *" debian "*) ;; *) err "Unsupported OS: $os_id. Debian/Ubuntu are supported." ;; esac ;;
    esac
    command -v systemctl >/dev/null || err "systemd is required"
}

detect_arch() {
    case "$(uname -m)" in
        x86_64)  echo amd64 ;;
        aarch64) echo arm64 ;;
        armv7l)  echo arm ;;
        *) err "Unsupported architecture: $(uname -m)" ;;
    esac
}

validate_inputs() {
    [[ "$PORT_UPLOAD" =~ ^[0-9]{2,5}$ && "$PORT_ADMIN" =~ ^[0-9]{2,5}$ ]] || err "TC_PORT_UPLOAD / TC_PORT_ADMIN must be port numbers"
    [[ "$PURGE_DAYS" =~ ^[0-9]{1,4}$ ]] || err "TC_PURGE_DAYS must be a number of days"
    [[ "$MAX_UPLOAD_MB" =~ ^[0-9]{1,9}$ ]] || err "TC_MAX_UPLOAD_MB must be a number"
    [[ "$RATE_LIMIT" =~ ^[0-9]{1,6}$ ]] || err "TC_RATE_LIMIT must be a number"
    [[ "$PUBLIC_UPLOADS" =~ ^[01]$ ]] || err "TC_PUBLIC_UPLOADS must be 0 or 1"
    [[ "$ADMIN_USER" =~ ^[A-Za-z0-9._-]{1,64}$ && "$UPLOAD_USER" =~ ^[A-Za-z0-9._-]{1,64}$ ]] || err "user names may only contain A-Z a-z 0-9 . _ -"
    [[ "$VERSION" =~ ^[A-Za-z0-9._-]+$ ]] || err "TC_VERSION looks wrong: $VERSION"
    [[ "$REPO" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || err "TC_REPO must look like owner/name"
    if [ -n "$DOMAIN" ]; then
        [[ "$DOMAIN" =~ ^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$ ]] || err "TC_DOMAIN is not a host name: $DOMAIN"
        [ -n "$EMAIL" ] || err "TC_EMAIL is required when TC_DOMAIN is set"
        PUBLIC_URL="https://$DOMAIN"
    fi
    if [ -n "$PUBLIC_URL" ]; then
        local url_re='^https?://[][A-Za-z0-9.:-]+$'   # ']' first so the bracket expression accepts IPv6 brackets
        [[ "$PUBLIC_URL" =~ $url_re ]] || err "TC_PUBLIC_URL must be scheme://host[:port] without a path"
    fi
    case "$TITLE" in *$'\n'*|*'"'*|*'\'*|*'$'*|*'`'*) err "TC_TITLE must not contain quotes, backslashes, \$, \` or newlines" ;; esac
}

install_pkgs() {
    log "Installing prerequisites..."
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq
    apt-get install -y -qq curl ca-certificates openssl apache2-utils coreutils >/dev/null
    if [ -n "$DOMAIN" ]; then
        apt-get install -y -qq nginx certbot python3-certbot-nginx >/dev/null
    fi
}

ensure_user() {
    if ! id transfercli >/dev/null 2>&1; then
        log "Creating transfercli system user..."
        useradd -r -s /usr/sbin/nologin -d "$DATA" -M transfercli
    fi
}

TMP=""
cleanup() { [ -n "$TMP" ] && rm -rf "$TMP"; return 0; }
trap cleanup EXIT

version_ge() { [ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -1)" = "$2" ]; }

build_from_source() {
    command -v go >/dev/null || err "TC_BUILD_FROM_SOURCE=1 needs Go >= $MIN_GO (https://go.dev/dl/). Distribution packages are usually too old."
    local gv; gv="$(go env GOVERSION | sed 's/^go//')"
    version_ge "$gv" "$MIN_GO" || err "Go $gv is too old; TransferCLI needs Go >= $MIN_GO (https://go.dev/dl/)"
    log "Building TransferCLI $VERSION from source with Go $gv..."
    curl -fsSL "https://github.com/${REPO}/archive/refs/tags/${VERSION}.tar.gz" | tar -xz -C "$TMP/src" --strip-components=1
    (cd "$TMP/src" && CGO_ENABLED=0 GOFLAGS=-mod=readonly go build -C admin -trimpath -ldflags "-s -w" -o "$TMP/dist/transfercli-admin-linux-$ARCH" .)
    (cd "$TMP/src" && CGO_ENABLED=0 GOFLAGS=-mod=readonly go build -C backend -trimpath -ldflags "-s -w" -o "$TMP/dist/transfersh-linux-$ARCH" .)
    (cd "$TMP/dist" && sha256sum "transfercli-admin-linux-$ARCH" "transfersh-linux-$ARCH" > SHA256SUMS)
}

fetch_binaries() {
    ARCH="$(detect_arch)"
    TMP="$(mktemp -d)"; mkdir -p "$TMP/dist" "$TMP/src"
    local files=("transfercli-admin-linux-$ARCH" "transfersh-linux-$ARCH")
    if [ -n "$LOCAL_DIST" ]; then
        log "Using local release files from $LOCAL_DIST"
        cp "$LOCAL_DIST/SHA256SUMS" "${files[@]/#/$LOCAL_DIST/}" "$TMP/dist/" || err "missing files in $LOCAL_DIST"
    elif [ "$BUILD_FROM_SOURCE" = 1 ]; then
        build_from_source
    else
        local base="https://github.com/${REPO}/releases/download/${VERSION}"
        log "Downloading TransferCLI $VERSION ($ARCH) from github.com/$REPO..."
        for f in SHA256SUMS "${files[@]}"; do
            curl -fsSL --retry 3 -o "$TMP/dist/$f" "$base/$f" \
                || err "Download failed: $base/$f (set TC_BUILD_FROM_SOURCE=1 to build from source instead)"
        done
    fi
    log "Verifying checksums..."
    (cd "$TMP/dist" && grep -E "  (transfercli-admin|transfersh)-linux-$ARCH\$" SHA256SUMS | sha256sum -c --strict --quiet -) \
        || err "Checksum verification FAILED - refusing to install"
    [ "$(grep -cE "  (transfercli-admin|transfersh)-linux-$ARCH\$" "$TMP/dist/SHA256SUMS")" -eq 2 ] || err "SHA256SUMS does not list both binaries for $ARCH"
    ok "Checksums verified"
    install -d -m 0755 "$PREFIX"
    install -m 0755 "$TMP/dist/transfersh-linux-$ARCH" "$PREFIX/transfersh.new"
    install -m 0755 "$TMP/dist/transfercli-admin-linux-$ARCH" "$PREFIX/transfercli-admin.new"
    mv -f "$PREFIX/transfersh.new" "$PREFIX/transfersh"
    mv -f "$PREFIX/transfercli-admin.new" "$PREFIX/transfercli-admin"
    ok "Installed: $("$PREFIX/transfersh" --version 2>&1 | tail -1)"
}

create_dirs() {
    log "Creating directories..."
    install -d -m 0750 -o transfercli -g transfercli "$DATA" "$DATA/uploads" "$DATA/temp"
    install -d -m 0750 -o root -g transfercli "$CONF_DIR"
}

gen_password() { openssl rand -base64 24 | tr -d '/+=' | cut -c1-20; }

NEW_CREDS=""
setup_credentials() {
    # Admin panel: bcrypt htpasswd checked by the admin service itself.
    if [ -n "$ADMIN_PASSWORD" ] || [ ! -s "$ADMIN_HTPASSWD" ] || ! grep -q '^[^:]*:\$2[aby]\$' "$ADMIN_HTPASSWD"; then
        [ -n "$ADMIN_PASSWORD" ] || ADMIN_PASSWORD="$(gen_password)"
        NEW_CREDS+="admin:   user=$ADMIN_USER password=$ADMIN_PASSWORD"$'\n'
        printf '%s\n' "$ADMIN_PASSWORD" | htpasswd -iBC 12 -c "$ADMIN_HTPASSWD" "$ADMIN_USER" 2>/dev/null
    fi
    chown root:transfercli "$ADMIN_HTPASSWD"; chmod 0640 "$ADMIN_HTPASSWD"

    if [ "$PUBLIC_UPLOADS" = 1 ]; then
        warn "TC_PUBLIC_UPLOADS=1: anyone who can reach the server can upload files."
        rm -f "$UPLOAD_HTPASSWD"
        return
    fi
    if [ -n "$UPLOAD_PASSWORD" ] || [ ! -s "$UPLOAD_HTPASSWD" ]; then
        [ -n "$UPLOAD_PASSWORD" ] || UPLOAD_PASSWORD="$(gen_password)"
        NEW_CREDS+="upload:  user=$UPLOAD_USER password=$UPLOAD_PASSWORD"$'\n'
        printf '%s\n' "$UPLOAD_PASSWORD" | htpasswd -iBC 10 -c "$UPLOAD_HTPASSWD" "$UPLOAD_USER" 2>/dev/null
    fi
    chown root:transfercli "$UPLOAD_HTPASSWD"; chmod 0640 "$UPLOAD_HTPASSWD"
}

write_env_files() {
    log "Writing configuration..."
    # Admin-editable purge settings: created once, later only the admin panel changes them.
    if [ ! -f "$ENV_FILE" ]; then
        cat > "$ENV_FILE" <<EOF
# transfer.sh purge settings - edited by the TransferCLI admin panel (Settings page)
PURGE_DAYS=${PURGE_DAYS}
PURGE_INTERVAL=24
EOF
    fi
    chown root:transfercli "$ENV_FILE"; chmod 0660 "$ENV_FILE"

    # Root-owned transfer.sh settings. Loaded after $ENV_FILE, so the admin panel cannot override them.
    {
        echo "# transfer.sh settings managed by the TransferCLI installer (re-run install.sh to change)"
        if [ "$PUBLIC_UPLOADS" = 1 ]; then echo "HTTP_AUTH_HTPASSWD="; else echo "HTTP_AUTH_HTPASSWD=$UPLOAD_HTPASSWD"; fi
        if [ "$MAX_UPLOAD_MB" != 0 ]; then echo "MAX_UPLOAD_SIZE=$((MAX_UPLOAD_MB * 1024))"; fi
        if [ "$RATE_LIMIT" != 0 ]; then echo "RATE_LIMIT=$RATE_LIMIT"; fi
    } > "$TSH_ENV"
    chown root:transfercli "$TSH_ENV"; chmod 0640 "$TSH_ENV"

    cat > "$ADMIN_ENV" <<EOF
# TransferCLI admin panel settings (managed by the installer)
TC_LISTEN=127.0.0.1:${PORT_ADMIN}
TC_UPLOADS_DIR=$DATA/uploads
TC_ENV_FILE=$ENV_FILE
TC_TITLE="${TITLE}"
TC_ADMIN_HTPASSWD=$ADMIN_HTPASSWD
TC_BASE_URL=${PUBLIC_URL}
EOF
    chown root:transfercli "$ADMIN_ENV"; chmod 0640 "$ADMIN_ENV"
}

SANDBOX='NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
ProtectProc=invisible
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
SystemCallArchitectures=native
SystemCallFilter=@system-service
SystemCallErrorNumber=EPERM
CapabilityBoundingSet=
AmbientCapabilities=
UMask=0027'

write_systemd_units() {
    log "Installing systemd units..."
    cat > /etc/systemd/system/transfercli.service <<EOF
[Unit]
Description=TransferCLI upload server (transfer.sh)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=transfercli
Group=transfercli
WorkingDirectory=$DATA
EnvironmentFile=-$ENV_FILE
EnvironmentFile=-$TSH_ENV
ExecStart=$PREFIX/transfersh --listener 127.0.0.1:${PORT_UPLOAD} --force-https --provider local --basedir $DATA/uploads --temp-path $DATA/temp
Restart=on-failure
RestartSec=5
LimitNOFILE=65536
ReadWritePaths=$DATA
$SANDBOX

[Install]
WantedBy=multi-user.target
EOF

    cat > /etc/systemd/system/transfercli-admin.service <<EOF
[Unit]
Description=TransferCLI admin panel
After=network-online.target transfercli.service
Wants=network-online.target

[Service]
Type=simple
User=transfercli
Group=transfercli
WorkingDirectory=$DATA
EnvironmentFile=-$ADMIN_ENV
ExecStart=$PREFIX/transfercli-admin
Restart=on-failure
RestartSec=5
ReadWritePaths=$DATA $ENV_FILE
$SANDBOX

[Install]
WantedBy=multi-user.target
EOF

    # The admin panel saves purge settings to $ENV_FILE; this path unit restarts transfer.sh when the
    # file changes. It replaces the old sudoers rule, so the admin service needs no privileges at all.
    cat > /etc/systemd/system/transfercli-config.path <<EOF
[Unit]
Description=Restart transfer.sh when TransferCLI settings change

[Path]
PathChanged=$ENV_FILE
Unit=transfercli-config.service

[Install]
WantedBy=multi-user.target
EOF
    cat > /etc/systemd/system/transfercli-config.service <<EOF
[Unit]
Description=Apply TransferCLI settings (restart transfer.sh)

[Service]
Type=oneshot
ExecStart=/bin/systemctl try-restart transfercli.service
EOF
    rm -f /etc/sudoers.d/transfercli   # used by TransferCLI < 2.0
}

setup_nginx() {
    [ -n "$DOMAIN" ] || return 0
    log "Configuring nginx for ${DOMAIN}..."
    cat > /etc/nginx/sites-available/transfercli <<EOF
server {
    listen 80;
    listen [::]:80;
    server_name ${DOMAIN};

    access_log off;
    client_max_body_size 0;
    proxy_request_buffering off;
    proxy_buffering off;
    proxy_read_timeout 3600s;
    proxy_send_timeout 3600s;
    server_tokens off;

    # Overwrite (never append) client-address headers: transfer.sh trusts them for rate limiting.
    proxy_set_header Host \$host;
    proxy_set_header X-Real-IP \$remote_addr;
    proxy_set_header X-Forwarded-For \$remote_addr;
    proxy_set_header X-Forwarded-Proto \$scheme;

    # Admin panel: authenticates every request itself (bcrypt), see $ADMIN_HTPASSWD
    location = /admin { proxy_pass http://127.0.0.1:${PORT_ADMIN}; }
    location /admin/  { proxy_pass http://127.0.0.1:${PORT_ADMIN}; }

    location / {
        proxy_pass http://127.0.0.1:${PORT_UPLOAD};
        add_header X-Content-Type-Options nosniff always;
        add_header Referrer-Policy same-origin always;
    }
}
EOF
    ln -sf /etc/nginx/sites-available/transfercli /etc/nginx/sites-enabled/transfercli
    nginx -t
    systemctl reload nginx
    log "Obtaining Let's Encrypt certificate..."
    certbot --nginx -d "$DOMAIN" -m "$EMAIL" --agree-tos --non-interactive --redirect
}

start_services() {
    log "Starting services..."
    systemctl daemon-reload
    systemctl enable --now transfercli-config.path >/dev/null
    systemctl enable transfercli transfercli-admin >/dev/null
    systemctl restart transfercli transfercli-admin
    for _ in $(seq 1 20); do
        if curl -fsS -o /dev/null "http://127.0.0.1:${PORT_ADMIN}/healthz" 2>/dev/null \
           && curl -sS -o /dev/null -H 'X-Forwarded-Proto: https' "http://127.0.0.1:${PORT_UPLOAD}/health.html" 2>/dev/null; then
            ok "Services are up"
            return
        fi
        sleep 1
    done
    systemctl --no-pager status transfercli transfercli-admin | tail -20 || true
    err "Services did not come up. Check: journalctl -u transfercli -u transfercli-admin"
}

print_summary() {
    local base="${PUBLIC_URL:-http://127.0.0.1:${PORT_UPLOAD}}"
    local admin_url="${PUBLIC_URL:+${PUBLIC_URL}/admin/}"
    admin_url="${admin_url:-http://127.0.0.1:${PORT_ADMIN}/admin/}"
    if [ -n "$NEW_CREDS" ]; then
        { echo "# TransferCLI credentials set on $(date -u +%FT%TZ) - keep this file private (the newest entry wins)"; printf '%s' "$NEW_CREDS"; } >> "$CRED_FILE"
        chmod 0600 "$CRED_FILE"
    fi
    printf '\n\033[1;32m  TransferCLI %s installed\033[0m\n\n' "$VERSION"
    echo "  Upload:      ${base}"
    echo "  Admin:       ${admin_url}"
    if [ -n "$NEW_CREDS" ]; then
        if [ "$QUIET" = 1 ]; then
            echo "  Credentials: written to $CRED_FILE (root only)"
        else
            echo "  Credentials (also saved to $CRED_FILE):"
            printf '%s' "$NEW_CREDS" | sed 's/^/    /'
        fi
    else
        echo "  Credentials: unchanged (see $CRED_FILE from the first install)"
    fi
    echo
    echo "  Config:      $ENV_FILE (purge), $TSH_ENV (auth/limits), $ADMIN_ENV"
    echo "  Data:        $DATA/uploads"
    echo "  Logs:        journalctl -u transfercli -u transfercli-admin"
    echo
    if [ "$PUBLIC_UPLOADS" = 1 ]; then
        echo "  Try it:      curl -T hello.txt ${base}/hello.txt"
    else
        echo "  Try it:      curl -u ${UPLOAD_USER}:<password> -T hello.txt ${base}/hello.txt"
    fi
    if [ -z "$DOMAIN" ] && [ -z "$PUBLIC_URL" ]; then
        echo
        echo "  Services listen on 127.0.0.1 only. For HTTPS either re-run with"
        echo "    TC_DOMAIN=files.example.com TC_EMAIL=me@example.com bash install.sh"
        echo "  or put your own reverse proxy in front and set TC_PUBLIC_URL (see docs/nginx.md)."
    fi
    echo
}

main() {
    require_root
    detect_os
    validate_inputs
    install_pkgs
    ensure_user
    fetch_binaries
    create_dirs
    setup_credentials
    write_env_files
    write_systemd_units
    start_services
    setup_nginx
    print_summary
}

main "$@"
