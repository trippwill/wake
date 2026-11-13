#!/bin/sh
# SPDX-FileCopyrightText: 2026 trippwill
# SPDX-License-Identifier: MPL-2.0
set -eu

fail() {
    printf 'wake quadlets: %s\n' "$1" >&2
    exit 1
}

[ "$#" -eq 4 ] || fail 'usage: sh render.sh OUTPUT_DIR CONFIG_DIR WAREHOUSE_DIR CREDENTIALS_DIR'
output_dir=$1
config_dir=$2
warehouse_dir=$3
credentials_dir=$4
operator_uid=$(id -u)
operator_gid=$(id -g)
[ "$operator_uid" -ne 0 ] || fail 'run as the SSH operator, without sudo'

for directory in "$config_dir" "$warehouse_dir" "$credentials_dir"; do
    case $directory in
        /|*[!a-zA-Z0-9_./-]*|*//*|*/./*|*/../*|*/.|*/..|*/)
            fail "use a clean absolute path containing only letters, digits, _, ., / or -: $directory" ;;
        /*) ;;
        *) fail "expected an absolute path: $directory" ;;
    esac
done

template_dir=$(dirname -- "$0")
mkdir -- "$output_dir"
sed "s/ -o 1000 -g 1000 / -o $operator_uid -g $operator_gid /" \
    "$template_dir/wake.pod" > "$output_dir/wake.pod"
for unit in wake.container wake-admin-relay.container; do
    sed -e "s/^User=1000$/User=$operator_uid/" \
        -e "s/^Group=1000$/Group=$operator_gid/" \
        -e "s|^Volume=/var/lib/wake-podman/config:|Volume=$config_dir:|" \
        -e "s|^Volume=/var/lib/wake-podman/data:|Volume=$warehouse_dir:|" \
        -e "s|^Volume=/var/lib/wake-podman/credentials:|Volume=$credentials_dir:|" \
        "$template_dir/$unit" > "$output_dir/$unit"
done
