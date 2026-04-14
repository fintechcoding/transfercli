# Nginx + HTTPS Setup

TransferCLI runs perfectly fine without nginx (transfer.sh on a public port), but for production you'll usually want a reverse proxy with TLS and basic auth on the admin panel.

## Automatic (installer)

The installer handles all of this if you pass `TC_DOMAIN`:
```bash
curl -fsSL ... | sudo TC_DOMAIN=files.example.com TC_EMAIL=me@example.com bash
```

## Manual

### 1. Install nginx + certbot

```bash
sudo apt install -y nginx certbot python3-certbot-nginx
```

### 2. Create vhost

Copy [`nginx/transfercli.conf.example`](../nginx/transfercli.conf.example) from the repo to `/etc/nginx/sites-available/transfercli`, edit `server_name`, then enable:
```bash
sudo ln -s /etc/nginx/sites-available/transfercli /etc/nginx/sites-enabled/
sudo nginx -t && sudo systemctl reload nginx
```

### 3. Basic auth file

```bash
sudo htpasswd -cbB /etc/transfercli/.htpasswd admin 'your-password'
sudo chown root:transfercli /etc/transfercli/.htpasswd
sudo chmod 640 /etc/transfercli/.htpasswd
```

### 4. Let's Encrypt

```bash
sudo certbot --nginx -d files.example.com -m me@example.com --agree-tos --redirect
```

Certbot automatically sets up HTTP→HTTPS redirect and schedules renewal.

## Important nginx settings

The example config disables request buffering and body size limits, which is **critical** for large uploads:
```nginx
client_max_body_size 0;
proxy_request_buffering off;
proxy_buffering off;
proxy_read_timeout 3600s;
```

Without these, nginx buffers the entire upload to disk before forwarding, which breaks streaming for large files.

## Cloudflare

If you want to proxy through Cloudflare, note that **Cloudflare Free plan limits upload bodies to 100 MB**. For large files, set the DNS record to **DNS only** (grey cloud) or use a dedicated upload subdomain that bypasses the proxy.
