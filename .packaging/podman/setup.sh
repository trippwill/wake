#!/bin/sh
# SPDX-FileCopyrightText: 2026 trippwill
# SPDX-License-Identifier: MPL-2.0
set -eu

fail() {
    printf 'wake podman setup: %s\n' "$1" >&2
    exit 1
}

[ "$#" -eq 4 ] || fail 'usage: sh setup.sh OUTPUT_DIR CONFIG_DIR WAREHOUSE_DIR CREDENTIALS_DIR'
[ -t 0 ] || fail 'run from a terminal to choose the browser unlock'
output_dir=$1
config_dir=$2
warehouse_dir=$3
credentials_dir=$4
operator_uid=$(id -u)
operator_gid=$(id -g)

sh "$(dirname -- "$0")/render.sh" "$@"
config_tmp=''
trap 'rm -f -- "$config_tmp" "$output_dir/wake.pod" "$output_dir/wake.container" "$output_dir/wake-admin-relay.container"; rmdir -- "$output_dir"' EXIT
trap 'exit 1' HUP INT TERM

if sudo podman pod exists wake; then
    fail 'stop the Wake pod before running setup'
else
    [ "$?" -eq 1 ] || fail 'could not check whether the Wake pod exists'
fi
sudo podman image exists localhost/wake:local || fail 'build the localhost/wake:local image first'

for directory in "$config_dir" "$warehouse_dir" "$credentials_dir"; do
    [ "$(realpath -m -- "$directory")" = "$directory" ] || fail "use a directory without symlinks: $directory"
    if [ ! -e "$directory" ]; then
        sudo install -d -m 0700 -o "$operator_uid" -g "$operator_gid" -- "$directory"
    fi
    [ -d "$directory" ] && [ "$(stat -c '%a %u' -- "$directory")" = "700 $operator_uid" ] ||
        fail "expected an operator-owned directory with mode 0700: $directory"
done

umask 077
[ ! -L "$config_dir/wake" ] || fail 'the Wake configuration directory must not be a symlink'
mkdir -p -- "$config_dir/wake"
[ "$(stat -c '%a %u' -- "$config_dir/wake")" = "700 $operator_uid" ] ||
    fail "expected an operator-owned directory with mode 0700: $config_dir/wake"
config_path=$config_dir/wake/config.toml
if [ ! -e "$config_path" ] && [ ! -L "$config_path" ]; then
    config_tmp=$(mktemp "$config_dir/wake/.wake-config.XXXXXX")
    cat > "$config_tmp" <<'TOML'
[storage]
warehouse_path = "/data/wake/wake.db"
connections_path = "/credentials/connections.db"

[paths]
runtime_dir = "/run/wake"

[admin]
port = 8787
TOML
    ln -T -- "$config_tmp" "$config_path"
    rm -- "$config_tmp"
    config_tmp=''
fi

sudo podman run --rm -it --pull=never --userns=host --user "$operator_uid:$operator_gid" \
    --cap-drop all --security-opt no-new-privileges \
    -v "$config_dir:/config:Z" \
    -v "$warehouse_dir:/data:Z" \
    -v "$credentials_dir:/credentials:Z" \
    localhost/wake:local setup --config /config/wake/config.toml --use-config

trap - EXIT HUP INT TERM
printf 'Wake Quadlets are ready in %s\n' "$output_dir"
