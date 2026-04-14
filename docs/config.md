# Configuration Reference

All runtime config via environment variables. The installer writes `/etc/transfercli.env` and `/etc/transfercli-admin.env`; edit them and restart the services to apply changes.

## transfer.sh backend (`/etc/transfercli.env`)

Loaded by `transfercli.service`.

| Variable | Default | Description |
|---|---|---|
| `PURGE_DAYS` | `30` | Auto-delete files older than N days. `0` disables. |
| `PURGE_INTERVAL` | `24` | Hours between purge checks. |
| `TC_PORT_UPLOAD` | `8081` | Listen port (used by the systemd unit template) |

Full transfer.sh flag reference: <https://github.com/dutchcoders/transfer.sh#usage>

## TransferCLI admin (`/etc/transfercli-admin.env`)

Loaded by `transfercli-admin.service`.

| Variable | Default | Description |
|---|---|---|
| `TC_LISTEN` | `127.0.0.1:8082` | HTTP listen address of the admin daemon |
| `TC_UPLOADS_DIR` | `/var/lib/transfercli/uploads` | Path to transfer.sh storage |
| `TC_ENV_FILE` | `/etc/transfercli.env` | transfer.sh env file (the admin panel reads/writes purge settings here) |
| `TC_SERVICE_NAME` | `transfercli` | Systemd unit name to restart when saving global settings |
| `TC_BASE_URL` | *(auto)* | URL prefix for file links. Empty = derive from request Host + Proto. Set explicitly if you use a different public URL. |
| `TC_TITLE` | `TransferCLI` | Browser title and header text |

### Applying changes

```bash
sudo systemctl restart transfercli        # after editing /etc/transfercli.env
sudo systemctl restart transfercli-admin  # after editing /etc/transfercli-admin.env
```

## Per-upload headers

Upload clients can set these headers to override defaults:

| Header | Description |
|---|---|
| `Max-Days: N` | Delete this file after N days |
| `Max-Downloads: N` | Delete after N downloads |

Example:
```bash
curl -H "Max-Days: 3" -H "Max-Downloads: 10" -T file.zip https://files.example.com/file.zip
```
