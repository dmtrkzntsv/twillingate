#!/usr/bin/env bash
# Runs install.sh inside a throwaway Debian container (with systemctl
# stubbed — no systemd in a container) and asserts every artifact lands where
# the runbook says. Requires docker.
# The container-side assertions live in test-install-inner.sh.
set -euo pipefail
cd "$(dirname "$0")/.."

command -v docker > /dev/null || { echo "docker is required"; exit 1; }
# The binary from the image: the one the release ships, static, so it runs
# in the Debian container whatever this host is. The image has no shell or
# cat, so the file is copied out of a created (never started) container.
docker build --target runtime -t twillingate:installtest . > /dev/null
bin="$(mktemp -d)"
cid=""
trap 'rm -rf "$bin"; [ -z "$cid" ] || docker rm "$cid" > /dev/null' EXIT
cid="$(docker create twillingate:installtest)"
docker cp "$cid:/usr/local/bin/twillingate" "$bin/twillingate" > /dev/null
chmod +x "$bin/twillingate"
docker run --rm -v "$PWD:/src:ro" -v "$bin/twillingate:/src-bin/twillingate:ro" debian:bookworm-slim \
  bash /src/scripts/test-install-inner.sh

# The refusals name their reason, before any download or install, in remote
# mode and in local mode (a hand-unpacked tarball): armv7l is 32-bit ARM, and
# any other unknown machine is refused by name.
docker run --rm -v "$PWD/deploy/systemd/install.sh:/opt/a/b/install.sh:ro" debian:bookworm-slim bash -c '
  mkdir /fake && printf "#!/bin/sh\nexit 0\n" > /fake/curl && chmod +x /fake/curl
  refuses() { # <uname -m output> <expected text> <install.sh>
    printf "#!/bin/sh\n[ \"\$1\" = -m ] && echo %s || echo Linux\n" "$1" > /fake/uname && chmod +x /fake/uname
    out="$(PATH=/fake:$PATH bash "$3" --yes 2>&1)" && { echo "FAIL: installer did not refuse $1"; exit 1; }
    case "$out" in *"$2"*) echo "ok: refuses $1 with: $2" ;; *) echo "FAIL: wanted \"$2\", got: $out"; exit 1 ;; esac
  }
  refuses armv7l "32-bit ARM is no longer built" /opt/a/b/install.sh
  refuses riscv64 "unsupported architecture: riscv64" /opt/a/b/install.sh
  # Local mode: the layout of an unpacked release tarball.
  mkdir -p /opt/pkg/deploy/systemd
  cp /opt/a/b/install.sh /opt/pkg/deploy/systemd/install.sh
  touch /opt/pkg/deploy/systemd/twillingate.service
  printf "#!/bin/sh\n" > /opt/pkg/twillingate && chmod +x /opt/pkg/twillingate
  refuses armv7l "32-bit ARM is no longer built" /opt/pkg/deploy/systemd/install.sh
'
