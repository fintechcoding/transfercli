# Changelog

## v2.0.0 — 2026-10-06 (security release)

### ⚠️ Breaking changes
- **Uploads require a password by default.** The installer generates an `upload` user and prints the
  password (also saved to `/root/transfercli-credentials.txt`). Upload with `curl -u upload:PASSWORD -T file …`.
  To keep anonymous uploads, re-run the installer with `TC_PUBLIC_UPLOADS=1` (not recommended on the internet).
- The admin panel authenticates requests itself (`TC_ADMIN_HTPASSWD`, bcrypt). The bundled nginx vhost no
  longer uses `auth_basic`; existing `/etc/transfercli/.htpasswd` files keep working.
- The installer now installs a **pinned release** and verifies `SHA256SUMS`. Install from a tag URL
  (`…/transfercli/v2.0.0/install.sh`), not from `main`.
- `TC_SERVICE_NAME` and the sudoers rule are gone: saving settings triggers `transfercli-config.path`,
  which restarts transfer.sh.

### Security fixes
- **transfer.sh is now built from source** (upstream commit `f040f28d`, 2026-09-28) with Go 1.27. The
  previously downloaded upstream v1.6.1 binary (Go 1.21.4, Dec 2023) is affected by **110 known
  vulnerabilities** reachable from its code (Go standard library, `golang.org/x/crypto`, `x/net`,
  `x/oauth2`, `x/text`, `grpc`, `protobuf`, `circl`). The new build: 0 (`govulncheck`).
- **Stored XSS in the admin panel**: the preview dialog inserted the uploaded file's name and
  content type (both chosen by the uploader) into the page with `innerHTML`. A crafted upload ran script in
  the admin's browser when the admin clicked *Preview*. The dialog now builds DOM nodes; file links are
  percent-encoded.
- **CSRF**: every admin action (delete, bulk delete, expiry, settings) could be triggered by any web page
  the admin visited, because browsers resend Basic-auth credentials cross-site. State-changing requests
  now require a same-origin `Origin`/`Referer` (and `Sec-Fetch-Site`).
- **Admin authentication**: the README promised in-app Basic auth, but the panel had none — without the
  bundled nginx it was unauthenticated. It now checks a bcrypt htpasswd file (weak hash types are refused),
  with a temporary failed-login throttle.
- **Upload endpoint was open to everyone** by default → password-protected by default.
- Admin security headers (strict CSP, `frame-ancestors 'none'`, `nosniff`, `no-store`), HTTP server
  timeouts, request body limit.
- Installer: passwords are no longer passed on the command line (`htpasswd -i`), downloads are pinned and
  checksum-verified, inputs are validated, `/etc/os-release` no longer overwrites the `VERSION` variable.
- systemd units are sandboxed (`NoNewPrivileges`, `ProtectSystem=strict`, empty capability set, syscall
  filter …); the admin service no longer needs `sudo`.
- nginx overwrites `X-Forwarded-For`/`X-Real-IP` instead of appending, so clients cannot spoof the address
  transfer.sh uses for rate limiting.

### Web UI
- transfer.sh's start page showed upload examples without credentials, which fail now that uploads need a
  password. Releases include `transfercli-web.tar.gz` — transfer.sh's own pages (extracted from the pinned
  `transfer.sh-web`, `backend/cmd/webdump`) with every upload example sending `-u <upload user>:PASSWORD`.
  The installer serves them with `WEB_PATH` whenever uploads are password-protected.

### Bug fixes
- Re-running the installer reset `TC_RATE_LIMIT`, `TC_MAX_UPLOAD_MB`, `TC_PUBLIC_URL`, `TC_TITLE` and
  `TC_PUBLIC_UPLOADS` to their defaults unless they were given again; it now keeps the previous values.
- The per-row **Delete** button sat inside the bulk-delete form (nested forms are invalid HTML), so it
  submitted the bulk form: it deleted the *checked* files, or nothing, instead of its own file.
- Saving settings failed with "permission denied" (the env file was root-owned) and, once writable, would
  have dropped every other line of the file. Values are now validated (purging enabled with a 0-hour
  interval is refused) and other lines are kept.
- `Max downloads = 0` made a file unavailable immediately; the form now accepts `-1` (unlimited) or ≥ 1.
- Editing expiry rounded very large `ContentLength` values (JSON numbers are now kept exact).
- With `TC_DOMAIN`, nginx could not read the admin password file (`/etc/transfercli` was `0700 root`) →
  every admin request returned 500.
- The installer summary printed the wrong admin URL without a domain.

## v1.0.0 — 2026-04-14
- Initial release.
