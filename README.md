# TransferCLI

> Self-hosted file sharing with curl-first uploads and a web admin panel.
> Built on top of [transfer.sh](https://github.com/dutchcoders/transfer.sh).

<p align="center">
  <img src="docs/images/dashboard.png" alt="TransferCLI Dashboard" width="800"/>
</p>

TransferCLI bundles [transfer.sh](https://github.com/dutchcoders/transfer.sh) with a web admin panel, an installer, hardened systemd units, and optional automatic HTTPS via Let's Encrypt. Upload files with `curl`, manage them from the browser.

> **Upgrading from 1.x?** Version 2.0 is a security release. Re-run the installer (see [Upgrading](#upgrading)) — read [CHANGELOG.md](CHANGELOG.md) first: uploads now require a password by default.

---

## Features

### Upload
- **`curl -T`** friendly — one-line upload from any terminal
- **Streaming** — no RAM buffering, handles 10 GB+ files
- **Direct download URLs** — `https://your.host/ABC123/file.iso` works with `wget`
- **Per-file expiry** via `Max-Days` / `Max-Downloads` headers
- **Global auto-purge** — delete files older than N days
- **Password-protected uploads by default** — downloads stay public, uploading needs credentials

### Admin Panel
- **📊 Live dashboard** — files, total size, downloads, disk usage with progress bar
- **📈 Charts** — 7-day upload trend + file type donut chart
- **🏆 Leaderboards** — top downloads + largest files (disk cleanup helper)
- **🔍 Search + sort** — instant filter, click column headers to sort
- **☑ Bulk actions** — multi-select + delete
- **👁 Preview** — images/videos/audio/PDF/text
- **📋 Copy link** — one-click share URL
- **✏ Edit expiry** — change max-days / max-downloads per file
- **⚙ Global settings** — update purge config from the UI (transfer.sh restarts automatically)

### Security
- **Built from source** — transfer.sh is compiled from a pinned upstream commit with a current Go toolchain (the last upstream release binary is affected by 100+ known vulnerabilities); every release is scanned with `govulncheck`
- **Verified installs** — the installer downloads a pinned release and refuses binaries whose SHA-256 does not match `SHA256SUMS`
- **Admin authentication in the app itself** (bcrypt), CSRF protection, strict security headers
- **Sandboxed services** — no root, no sudo, read-only system, `systemd-analyze security` score 1.5 ("OK")
- See [docs/security.md](docs/security.md)

### Privacy
- **No IP logging** (nginx `access_log off`; services only see the local proxy)
- **No analytics, no cookies, no telemetry**

### Deployment
- **One-command install** on Debian/Ubuntu (amd64, arm64, armv7)
- **Automatic HTTPS** with Let's Encrypt (when a domain is provided), or bring your own reverse proxy
- **Systemd-managed** — autorestart, enable-on-boot
- **Single static Go binaries**, no runtime dependencies

---

## Screenshots

| Dashboard | Files & Search |
|---|---|
| ![Dashboard](docs/images/dashboard.png) | ![Files](docs/images/files.png) |

| Edit Modal | Global Settings |
|---|---|
| ![Edit](docs/images/edit-modal.png) | ![Settings](docs/images/settings.png) |

---

## Quick install

Download the installer of a **tagged release**, read it, then run it:

```bash
curl -fsSL https://raw.githubusercontent.com/fintechcoding/transfercli/v2.0.0/install.sh -o install.sh
less install.sh
sudo bash install.sh                                                     # localhost only, no TLS
sudo TC_DOMAIN=files.example.com TC_EMAIL=me@example.com bash install.sh # nginx + Let's Encrypt
```

The installer prints the generated admin and upload passwords once and saves them to
`/root/transfercli-credentials.txt` (readable by root only). Re-running it keeps existing credentials.

Useful options (all optional): `TC_PUBLIC_URL` (own reverse proxy), `TC_MAX_UPLOAD_MB`, `TC_RATE_LIMIT`,
`TC_PURGE_DAYS`, `TC_PUBLIC_UPLOADS=1` (no upload password — not recommended on the internet),
`TC_LOCAL_DIST` (offline install), `TC_BUILD_FROM_SOURCE=1`. The full list is at the top of
[`install.sh`](install.sh).

---

## Usage

### Upload with curl
```bash
# Basic upload (credentials from the install output)
curl -u upload:PASSWORD -T myfile.zip https://files.example.com/myfile.zip

# With expiry (auto-delete after 7 days)
curl -u upload:PASSWORD -H "Max-Days: 7" -T myfile.zip https://files.example.com/myfile.zip

# With download limit
curl -u upload:PASSWORD -H "Max-Downloads: 5" -T myfile.zip https://files.example.com/myfile.zip
```

transfer.sh returns a URL like `https://files.example.com/AbCdEf/myfile.zip` which is directly `wget`-compatible.

### Download
```bash
wget https://files.example.com/AbCdEf/myfile.zip
```

### Admin panel
Open `https://files.example.com/admin/` in your browser and sign in with the admin credentials.

---

## Configuration

| File | Owner | Contents |
|---|---|---|
| `/etc/transfercli.env` | admin panel | `PURGE_DAYS`, `PURGE_INTERVAL` (edited from the Settings page) |
| `/etc/transfercli/transfersh.env` | installer | upload password file, upload size and rate limits |
| `/etc/transfercli-admin.env` | installer | admin panel listen address, title, public URL, password file |

See [docs/config.md](docs/config.md) for every variable.

---

## Manual install

Not a fan of running an installer as root? See [docs/install-manual.md](docs/install-manual.md).

---

## Upgrading

Re-run the installer of the new release. It upgrades binaries and units in place and keeps credentials, settings and files:
```bash
curl -fsSL https://raw.githubusercontent.com/fintechcoding/transfercli/v2.0.0/install.sh -o install.sh
sudo bash install.sh
```

---

## Uninstalling

```bash
curl -fsSL https://raw.githubusercontent.com/fintechcoding/transfercli/v2.0.0/uninstall.sh -o uninstall.sh
sudo bash uninstall.sh
```
Prompts before removing data, credentials, nginx config, and the system user (`TC_YES=1` answers yes to everything).

---

## Architecture

```
                    ┌────────────────────────────┐
  Internet  ────▶   │  nginx / your proxy (TLS)  │
                    │  - access_log off          │
                    └──┬─────────────────────┬───┘
                       │ /                   │ /admin/
                       ▼                     ▼
              ┌────────────────┐   ┌────────────────────┐
              │  transfer.sh   │   │  transfercli-admin │
              │  127.0.0.1:8081│   │  127.0.0.1:8082    │
              │  upload auth   │   │  bcrypt auth, CSRF │
              └────────┬───────┘   └─────────┬──────────┘
                       │                     │ reads, deletes, edits expiry
                       ▼                     │
              ┌───────────────────────┐◀─────┘
              │ /var/lib/transfercli  │      settings saved → transfercli-config.path
              │   /uploads/<id>/...   │      restarts transfer.sh (no sudo needed)
              └───────────────────────┘
```

---

## Development

```bash
cd admin && go test ./...                 # admin panel tests
scripts/build-release.sh v2.0.0 amd64     # release binaries + SHA256SUMS into dist/
```
CI runs the tests, `go vet`, `govulncheck` and `shellcheck` on every push.

---

## Credits

TransferCLI is built on the excellent [transfer.sh](https://github.com/dutchcoders/transfer.sh) by DutchCoders. The upload backend is compiled from unmodified upstream source; TransferCLI adds the admin panel, installer, and packaging. See [NOTICE](NOTICE) for attribution details.

## License

MIT. See [LICENSE](LICENSE).

## Contributing

Issues and PRs welcome. Please report security problems privately — see [SECURITY.md](SECURITY.md).
