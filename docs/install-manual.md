# Manual Installation

For those who prefer to install TransferCLI step by step instead of running `install.sh`.

## Prerequisites

- Debian 12+ or Ubuntu 22.04+ with systemd
- Root or sudo access
- `curl`, `openssl`, `apache2-utils` (`htpasswd`); optional `nginx` + `certbot` for HTTPS

```bash
sudo apt update
sudo apt install -y curl openssl apache2-utils
```

## 1. System user and directories

```bash
sudo useradd -r -s /usr/sbin/nologin -d /var/lib/transfercli -M transfercli
sudo install -d -m 0750 -o transfercli -g transfercli /var/lib/transfercli /var/lib/transfercli/uploads /var/lib/transfercli/temp
sudo install -d -m 0750 -o root -g transfercli /etc/transfercli
sudo install -d -m 0755 /opt/transfercli
```

## 2. Binaries

**Option A — release binaries (recommended).** Download and verify:
```bash
V=v2.0.0; ARCH=amd64   # or arm64, arm
cd "$(mktemp -d)"
for f in SHA256SUMS transfercli-admin-linux-$ARCH transfersh-linux-$ARCH; do
  curl -fsSLO "https://github.com/fintechcoding/transfercli/releases/download/$V/$f"
done
grep -E " (transfercli-admin|transfersh)-linux-$ARCH\$" SHA256SUMS | sha256sum -c -
sudo install -m 0755 transfersh-linux-$ARCH /opt/transfercli/transfersh
sudo install -m 0755 transfercli-admin-linux-$ARCH /opt/transfercli/transfercli-admin
```

**Option B — build from source.** Needs Go 1.26+ from <https://go.dev/dl/> (distribution packages are
usually too old and would link a vulnerable standard library):
```bash
git clone --branch v2.0.0 https://github.com/fintechcoding/transfercli.git && cd transfercli
CGO_ENABLED=0 go build -C backend -trimpath -o ../transfersh .
CGO_ENABLED=0 go build -C admin   -trimpath -o ../transfercli-admin .
sudo install -m 0755 transfersh transfercli-admin /opt/transfercli/
```

## 3. Credentials

```bash
sudo htpasswd -iBC 12 -c /etc/transfercli/.htpasswd admin          # type the admin password
sudo htpasswd -iBC 10 -c /etc/transfercli/upload.htpasswd upload   # type the upload password
sudo chown root:transfercli /etc/transfercli/.htpasswd /etc/transfercli/upload.htpasswd
sudo chmod 0640 /etc/transfercli/.htpasswd /etc/transfercli/upload.htpasswd
```

## 4. Config files

```bash
printf 'PURGE_DAYS=30\nPURGE_INTERVAL=24\n' | sudo tee /etc/transfercli.env >/dev/null
sudo chown root:transfercli /etc/transfercli.env && sudo chmod 0660 /etc/transfercli.env

printf 'HTTP_AUTH_HTPASSWD=/etc/transfercli/upload.htpasswd\n' | sudo tee /etc/transfercli/transfersh.env >/dev/null
sudo chown root:transfercli /etc/transfercli/transfersh.env && sudo chmod 0640 /etc/transfercli/transfersh.env

sudo tee /etc/transfercli-admin.env >/dev/null <<'EOT'
TC_LISTEN=127.0.0.1:8082
TC_UPLOADS_DIR=/var/lib/transfercli/uploads
TC_ENV_FILE=/etc/transfercli.env
TC_ADMIN_HTPASSWD=/etc/transfercli/.htpasswd
TC_BASE_URL=https://files.example.com
TC_TITLE=TransferCLI
EOT
sudo chown root:transfercli /etc/transfercli-admin.env && sudo chmod 0640 /etc/transfercli-admin.env
```

## 5. systemd units

Copy the four files from [`systemd/`](../systemd/) to `/etc/systemd/system/`, then:
```bash
sudo systemctl daemon-reload
sudo systemctl enable --now transfercli-config.path transfercli transfercli-admin
curl -sf http://127.0.0.1:8082/healthz && echo admin ok
```

## 6. Reverse proxy + TLS

See [nginx.md](nginx.md).
