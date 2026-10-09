#!/bin/sh
# Print the action.yml for a release: where Probe downloads each executable
# from, and the SHA-256 digest it must have.
#
#   scripts/action-yml.sh <tag> <checksums.txt>
#
# It declares no guard: the action cannot tell what a page will do, so a
# step that uses it is refused under --read-only or --allow-host unless
# --allow-action names it.
set -eu

tag=$1
checksums=$2
repo=${GITHUB_REPOSITORY:-mozership/probe-browser}

cat <<YAML
name: browser
description: Drive a browser with chromedp
params: [actions, headless, window_w, window_h, evidence_dir, timeout]
runs:
  using: binary
  url: https://github.com/${repo}/releases/download/${tag}/probe-browser_{os}_{arch}
  checksums:
YAML
# A line of checksums.txt is "<digest>  probe-browser_<os>_<arch>".
awk '$2 ~ /^probe-browser_/ { sub(/^probe-browser_/, "", $2); printf "    %s: \"%s\"\n", $2, $1 }' "$checksums" | sort
