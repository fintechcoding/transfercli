# Security & Privacy Notes

## What TransferCLI logs

By design, **TransferCLI does not log uploader IP addresses or any per-request metadata**. The defaults:

- `nginx access_log off` (in the example vhost)
- transfer.sh stores no uploader info in file metadata
- Admin panel has no uploader-tracking column
- No analytics, no cookies, no telemetry

## What TransferCLI *does* persist

- Uploaded file contents → `/var/lib/transfercli/uploads/`
- Per-file metadata (size, content type, download count, expiry) → JSON next to the file
- Admin basic auth credentials → `/etc/transfercli/.htpasswd` (bcrypt)
- Admin panel global purge setting → `/etc/transfercli.env`

## Threat model & recommendations

### Public upload endpoint
By default, anyone with network access to the upload port can upload files. If that's not desired:
- Put transfer.sh behind IP whitelisting (`ip_whitelist` flag)
- Or require basic auth at the nginx layer on `location /`
- Or only listen on localhost and expose via SSH tunnel

### Disk exhaustion
A malicious user could fill the disk. Mitigations:
- Set `PURGE_DAYS` to a reasonable value
- Monitor disk usage (the admin panel shows a progress bar)
- Use a dedicated filesystem/volume for uploads

### Abuse
- Consider nginx `limit_req` rate limiting
- Block known-bad IPs at the firewall
- For sensitive use, require auth for uploads too

### Admin panel
- Always behind basic auth (or another auth layer)
- Always behind TLS in production
- Default install binds to `127.0.0.1` — only accessible via nginx proxy

### Upgrading transfer.sh
TransferCLI pins to a specific transfer.sh version in `install.sh`. Upgrades are manual — re-run the installer to pull the pinned version, or edit `TRANSFERSH_VERSION` to select a newer release.

### Data at rest
File contents and metadata are plain on disk. If you need encrypted storage:
- Use `gocryptfs` to mount `/var/lib/transfercli` as an encrypted overlay
- Or use LUKS for the underlying block device
