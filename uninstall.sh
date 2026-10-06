#!/usr/bin/env bash
# TransferCLI uninstaller. Set TC_YES=1 to answer "yes" to every question (non-interactive).
set -euo pipefail

[ "$(id -u)" -eq 0 ] || { echo "Must run as root"; exit 1; }

ask() {
    [ "${TC_YES:-0}" = 1 ] && return 0
    local ans
    read -r -p "$1 [y/N] " ans
    [ "${ans,,}" = "y" ]
}

ask "This will remove TransferCLI, its services and configuration. Continue?" || exit 0

echo "Stopping services..."
systemctl disable --now transfercli-config.path transfercli transfercli-admin 2>/dev/null || true

echo "Removing files..."
rm -f /etc/systemd/system/transfercli.service /etc/systemd/system/transfercli-admin.service \
      /etc/systemd/system/transfercli-config.path /etc/systemd/system/transfercli-config.service
rm -f /etc/transfercli.env /etc/transfercli-admin.env
rm -f /etc/sudoers.d/transfercli   # TransferCLI < 2.0
rm -rf /etc/transfercli /opt/transfercli

if ask "Also remove /var/lib/transfercli (ALL uploaded files)?"; then
    rm -rf /var/lib/transfercli
fi

if [ -f /root/transfercli-credentials.txt ] && ask "Remove /root/transfercli-credentials.txt?"; then
    rm -f /root/transfercli-credentials.txt
fi

if [ -f /etc/nginx/sites-enabled/transfercli ] || [ -f /etc/nginx/sites-available/transfercli ]; then
    if ask "Remove nginx vhost /etc/nginx/sites-*/transfercli?"; then
        rm -f /etc/nginx/sites-enabled/transfercli /etc/nginx/sites-available/transfercli
        nginx -t && systemctl reload nginx
    fi
fi

if id transfercli >/dev/null 2>&1 && ask "Remove the transfercli system user?"; then
    userdel transfercli 2>/dev/null || true
fi

systemctl daemon-reload
echo "TransferCLI uninstalled"
