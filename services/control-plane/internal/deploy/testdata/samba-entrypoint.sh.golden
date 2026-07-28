#!/bin/sh
# Provision the single share user from the compose environment, then run smbd
# in the foreground. Idempotent: the user is created once, the password is set
# on every start so a changed SAMBA_PASSWORD takes effect on redeploy.
set -e
mkdir -p /run/samba /var/lib/samba/private
if ! id "$SMB_USER" >/dev/null 2>&1; then
    adduser -D -H -s /sbin/nologin -u "${SMB_UID:-1000}" "$SMB_USER"
fi
printf '%s\n%s\n' "$SMB_PASSWORD" "$SMB_PASSWORD" | smbpasswd -a -s "$SMB_USER"
exec smbd --foreground --no-process-group
