# Reverse proxy + HTTPS

transfer.sh and the admin panel only listen on `127.0.0.1`. Put a reverse proxy with TLS in front of them.

## Automatic (installer)

The installer configures nginx and Let's Encrypt when you pass `TC_DOMAIN`:
```bash
sudo TC_DOMAIN=files.example.com TC_EMAIL=me@example.com bash install.sh
```

## Manual nginx

```bash
sudo apt install -y nginx certbot python3-certbot-nginx
sudo cp nginx/transfercli.conf.example /etc/nginx/sites-available/transfercli   # edit server_name
sudo ln -s /etc/nginx/sites-available/transfercli /etc/nginx/sites-enabled/
sudo nginx -t && sudo systemctl reload nginx
sudo certbot --nginx -d files.example.com -m me@example.com --agree-tos --redirect
```
Then set `TC_BASE_URL=https://files.example.com` in `/etc/transfercli-admin.env` (or re-run the installer
with `TC_PUBLIC_URL=https://files.example.com`). No `auth_basic` is needed: the admin panel authenticates
requests itself.

## Required proxy settings

- **No buffering and no body limit** — otherwise the proxy buffers whole uploads to disk:
  `client_max_body_size 0; proxy_request_buffering off; proxy_buffering off; proxy_read_timeout 3600s;`
- **`X-Forwarded-Proto`** — transfer.sh runs with `--force-https` and redirects requests that do not say `https`.
- **Overwrite `X-Forwarded-For` / `X-Real-IP`** with the real client address. Appending (nginx
  `$proxy_add_x_forwarded_for`) lets clients choose the address transfer.sh rate-limits on.
- Route `/admin` and `/admin/` to the admin port (8082) and everything else to transfer.sh (8081).

## Caddy

```caddyfile
files.example.com {
	@admin path /admin /admin/*
	reverse_proxy @admin 127.0.0.1:8082 {
		header_up X-Real-IP {remote_host}
		header_up X-Forwarded-For {remote_host}
	}
	reverse_proxy 127.0.0.1:8081 {
		flush_interval -1
		header_up X-Real-IP {remote_host}
		header_up X-Forwarded-For {remote_host}
		transport http {
			read_timeout 1h
			write_timeout 1h
		}
	}
}
```
Behind Cloudflare use `{client_ip}` with `trusted_proxies` set to Cloudflare's ranges and
`client_ip_headers CF-Connecting-IP` instead of `{remote_host}`.

## Cloudflare

**Cloudflare's Free and Pro plans limit request bodies to 100 MB**, so larger uploads through a proxied
(orange cloud) record fail with HTTP 413. Downloads are not limited. For large uploads use a second,
**DNS only** (grey cloud) host name for uploading, or upgrade the plan.
