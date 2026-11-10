#!/bin/sh
# SPDX-FileCopyrightText: 2026 trippwill
# SPDX-License-Identifier: MPL-2.0
set -eu

fail() {
    printf 'wake install: %s\n' "$1" >&2
    exit 1
}

[ "$#" -eq 1 ] || fail 'usage: sh install.sh /path/to/wake'
[ "$(uname -s)" = Linux ] || fail 'this installer requires Linux'
[ "$(id -u)" -ne 0 ] || fail 'run this installer as the user who will run Wake'
[ -f "$1" ] && [ -r "$1" ] && [ -x "$1" ] || fail "expected a readable executable file: $1"

case ${HOME:-} in
    /*) ;;
    *) fail 'HOME must be an absolute path' ;;
esac
data_dir=${XDG_DATA_HOME:-$HOME/.local/share}
case $data_dir in
    /*) ;;
    *) fail 'XDG_DATA_HOME must be an absolute path' ;;
esac

binary=$HOME/.local/bin/wake
unit=$data_dir/systemd/user/wake.service
unit_marker='# Managed by the Wake installer.'

for destination in "$binary" "$unit"; do
    if [ -L "$destination" ] || { [ -e "$destination" ] && [ ! -f "$destination" ]; }; then
        fail "refusing to replace $destination"
    fi
done
if [ -f "$unit" ] && ! grep -Fxq -- "$unit_marker" "$unit"; then
    fail "refusing to replace an unmanaged unit: $unit"
fi

mkdir -p -- "${binary%/*}" "${unit%/*}"
binary_tmp=''
unit_tmp=''
trap 'rm -f -- "$binary_tmp" "$unit_tmp"' EXIT
trap 'exit 1' HUP INT TERM
binary_tmp=$(mktemp "${binary%/*}/.wake.XXXXXX")
unit_tmp=$(mktemp "${unit%/*}/.wake.service.XXXXXX")

install -m 0755 -- "$1" "$binary_tmp"
{
    printf '%s\n' "$unit_marker"
    cat <<'UNIT'
[Unit]
Description=Wake financial evidence warehouse
StartLimitIntervalSec=60s
StartLimitBurst=5

[Service]
Type=exec
ExecStart="%h/.local/bin/wake" serve
Environment="WAKE_CONFIG=%E/wake/config.toml"
Restart=on-failure
RestartSec=5s
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=default.target
UNIT
} > "$unit_tmp"
chmod 0644 -- "$unit_tmp"

mv -fT -- "$binary_tmp" "$binary"
mv -fT -- "$unit_tmp" "$unit"
printf 'Installed %s\nInstalled %s\n' "$binary" "$unit"
