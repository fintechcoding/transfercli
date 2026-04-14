# Manual Installation

For those who prefer to install TransferCLI step-by-step instead of `curl | bash`.

## Prerequisites

- Debian 12+ or Ubuntu 22.04+ (other distros may work with minor tweaks)
- Root or sudo access
- `curl`, `tar`, `openssl`, `apache2-utils` (`htpasswd`)
- Optional: `nginx`, `certbot` for HTTPS

```bash
sudo apt update
sudo apt install -y curl tar openssl apache2-utils
```

## 1. Create system user

```bash
sudo useradd -r -s /usr/sbin/nologin -d /var/lib/transfercli transfercli
sudo mkdir -p /var/lib/transfercli/{uploads,temp} /etc/transfercli /opt/transfercli
sudo chown -R transfercli:transfercli /var/lib/transfercli
```

## 2. Install transfer.sh

```bash
TS_VERSION=v1.6.1
sudo curl -fsSL "https://github.com/dutchcoders/transfer.sh/releases/download/${TS_VERSION}/transfersh-${TS_VERSION}-linux-amd64" \
  -o /opt/transfercli/transfersh
sudo chmod +x /opt/transfercli/transfersh
```

## 3. Build transfercli-admin

Requires Go 1.22+:
```bash
sudo apt install -y golang-go
git clone https://github.com/codingsecurity/transfercli.git /tmp/transfercli
cd /tmp/transfercli/admin
go build -o /opt/transfercli/transfercli-admin main.go
```

## 4. Config files

```bash
sudo tee /etc/transfercli.env >/dev/null <<EOT
PURGE_DAYS=30
PURGE_INTERVAL=24
EOT

sudo tee /etc/transfercli-admin.env >/dev/null <<EOT
TC_LISTEN=127.0.0.1:8082
TC_UPLOADS_DIR=/var/lib/transfercli/uploads
TC_ENV_FILE=/etc/transfercli.env
TC_SERVICE_NAME=transfercli
TC_TITLE=TransferCLI
EOT
```

## 5. Systemd units

Copy the unit files from the repo:
```bash
sudo cp /tmp/transfercli/systemd/transfercli.service /etc/systemd/system/
sudo cp /tmp/transfercli/systemd/transfercli-admin.service /etc/systemd/system/
sudo systemctl daemon-reload
```

## 6. Sudoers (admin → service restart)

```bash
sudo tee /etc/sudoers.d/transfercli >/dev/null <<'EOT'
transfercli ALL=(ALL) NOPASSWD: /bin/systemctl restart transfercli
EOT
sudo chmod 440 /etc/sudoers.d/transfercli
sudo visudo -cf /etc/sudoers.d/transfercli
```

## 7. Basic auth

```bash
sudo htpasswd -cbB /etc/transfercli/.htpasswd admin 'your-password-here'
sudo chown root:transfercli /etc/transfercli/.htpasswd
sudo chmod 640 /etc/transfercli/.htpasswd
```

## 8. Start services

```bash
sudo systemctl enable --now transfercli transfercli-admin
sudo systemctl status transfercli transfercli-admin
```

## 9. Nginx + HTTPS (optional)

See [nginx.md](nginx.md).

## 10. Verify

```bash
# Upload test
curl -T /etc/hostname http://127.0.0.1:8081/hostname

# Admin panel
curl -u admin:your-password-here http://127.0.0.1:8082/admin/
```
