# Configuration Reference

All runtime configuration lives in environment files written by the installer. Re-run the installer to
change installer-managed values; it keeps credentials, settings and files.

## Purge settings (`/etc/transfercli.env`)

Loaded by `transfercli.service`. Edited by the admin panel's **Settings** page; saving restarts transfer.sh
automatically (`transfercli-config.path`).

| Variable | Default | Description |
|---|---|---|
| `PURGE_DAYS` | `30` | Auto-delete files older than N days. `0` disables purging. |
| `PURGE_INTERVAL` | `24` | Hours between purge runs (1–8760; required when `PURGE_DAYS` > 0). |

## transfer.sh settings (`/etc/transfercli/transfersh.env`)

Root-owned; loaded **after** `/etc/transfercli.env`, so the admin panel cannot override these.

| Variable | Installer option | Description |
|---|---|---|
| `HTTP_AUTH_HTPASSWD` | `TC_PUBLIC_UPLOADS=0` (default) | bcrypt htpasswd file required for uploads (empty = anyone can upload) |
| `MAX_UPLOAD_SIZE` | `TC_MAX_UPLOAD_MB` | Max upload size in **KiB** (the installer converts MiB) |
| `RATE_LIMIT` | `TC_RATE_LIMIT` | Requests per minute per client |

Any other transfer.sh setting can be added here as an environment variable (for example
`CORS_DOMAINS`, `RANDOM_TOKEN_LENGTH`, `IP_WHITELIST`). Reference: <https://github.com/dutchcoders/transfer.sh#usage>.
The listen address, storage path and `--force-https` are set on the `ExecStart` line of
`/etc/systemd/system/transfercli.service`.

## Admin panel (`/etc/transfercli-admin.env`)

Loaded by `transfercli-admin.service`.

| Variable | Default | Description |
|---|---|---|
| `TC_LISTEN` | `127.0.0.1:8082` | Listen address |
| `TC_UPLOADS_DIR` | `/var/lib/transfercli/uploads` | transfer.sh storage |
| `TC_ENV_FILE` | `/etc/transfercli.env` | Purge settings file the Settings page edits |
| `TC_ADMIN_HTPASSWD` | `/etc/transfercli/.htpasswd` | bcrypt htpasswd file; every request must authenticate. Empty = no authentication (only behind a proxy that authenticates) |
| `TC_BASE_URL` | *(from the request)* | Public URL of the file host, used for file links and the CSP |
| `TC_ADMIN_ALLOWED_ORIGINS` | *(empty)* | Extra origins (comma separated, `https://host`) allowed to submit admin forms |
| `TC_TITLE` | `TransferCLI` | Browser title and header text |

### Applying manual changes

```bash
sudo systemctl restart transfercli        # after editing /etc/transfercli/transfersh.env
sudo systemctl restart transfercli-admin  # after editing /etc/transfercli-admin.env
```

### Changing passwords

```bash
sudo htpasswd -iBC 12 /etc/transfercli/.htpasswd admin          # admin (picked up without a restart)
sudo htpasswd -iBC 10 /etc/transfercli/upload.htpasswd upload   # upload, then:
sudo systemctl restart transfercli
```
`-i` reads the password from standard input, so it does not end up in your shell history or process list.

## Per-upload headers

Upload clients can set these headers to override defaults:

| Header | Description |
|---|---|
| `Max-Days: N` | Delete this file after N days |
| `Max-Downloads: N` | Delete after N downloads |

```bash
curl -u upload:PASSWORD -H "Max-Days: 3" -H "Max-Downloads: 10" -T file.zip https://files.example.com/file.zip
```
