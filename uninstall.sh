#!/usr/bin/env bash
# TransferCLI uninstaller
set -euo pipefail

[ "$(id -u)" -eq 0 ] || { echo "Must run as root"; exit 1; }

read -r -p "This will remove TransferCLI, its services, config, and data. Continue? [y/N] " ans
[ "${ans,,}" = "y" ] || exit 0

echo "Stopping services..."
systemctl disable --now transfercli transfercli-admin 2>/dev/null || true

echo "Removing files..."
rm -f /etc/systemd/system/transfercli.service /etc/systemd/system/transfercli-admin.service
rm -f /etc/transfercli.env /etc/transfercli-admin.env
rm -f /etc/sudoers.d/transfercli
rm -rf /etc/transfercli /opt/transfercli

read -r -p "Also remove /var/lib/transfercli (all uploaded files)? [y/N] " ans
if [ "${ans,,}" = "y" ]; then
    rm -rf /var/lib/transfercli
fi

if [ -f /etc/nginx/sites-enabled/transfercli ]; then
    read -r -p "Remove nginx vhost /etc/nginx/sites-*/transfercli? [y/N] " ans
    if [ "${ans,,}" = "y" ]; then
        rm -f /etc/nginx/sites-enabled/transfercli /etc/nginx/sites-available/transfercli
        nginx -t && systemctl reload nginx
    fi
fi

read -r -p "Remove transfercli system user? [y/N] " ans
if [ "${ans,,}" = "y" ]; then
    userdel transfercli 2>/dev/null || true
fi

systemctl daemon-reload
echo "✓ TransferCLI uninstalled"
