#!/usr/bin/env bash
# Runs install.sh inside a throwaway Debian container (with systemctl
# stubbed — no systemd in a container) and asserts every artifact lands where
# the runbook says. Requires docker.
# The container-side assertions live in test-install-inner.sh.
set -euo pipefail
cd "$(dirname "$0")/.."

command -v docker > /dev/null || { echo "docker is required"; exit 1; }
# The binary from the image: built on Debian 12, as the release is built on
# its glibc floor; a host build may need a newer glibc than the container has.
docker build --target runtime -t twillingate:installtest . > /dev/null
bin="$(mktemp -d)"
trap 'rm -rf "$bin"' EXIT
docker run --rm --entrypoint cat twillingate:installtest /usr/local/bin/twillingate > "$bin/twillingate"
chmod +x "$bin/twillingate"
docker run --rm -v "$PWD:/src:ro" -v "$bin/twillingate:/src-bin/twillingate:ro" debian:bookworm-slim \
  bash /src/scripts/test-install-inner.sh

# The refusals name their reason, before any download or install, in remote
# mode and in local mode (a hand-unpacked tarball): a getconf that fails is a
# musl host, one that prints 2.31 is too old, armv7l is 32-bit ARM.
docker run --rm -v "$PWD/deploy/systemd/install.sh:/opt/a/b/install.sh:ro" debian:bookworm-slim bash -c '
  mkdir /fake && printf "#!/bin/sh\nexit 0\n" > /fake/curl && chmod +x /fake/curl
  refuses() { # <getconf script body> <expected text>
    printf "#!/bin/sh\n%s\n" "$1" > /fake/getconf && chmod +x /fake/getconf
    out="$(PATH=/fake:$PATH bash /opt/a/b/install.sh --yes 2>&1)" && { echo "FAIL: installer did not refuse"; exit 1; }
    case "$out" in *"$2"*) echo "ok: refuses with: $2" ;; *) echo "FAIL: wanted \"$2\", got: $out"; exit 1 ;; esac
  }
  refuses "exit 1" "needs glibc 2.35 or newer; this system has none"
  refuses "echo glibc 2.31" "found 2.31"
  printf "#!/bin/sh\nexit 0\n" > /fake/getconf && chmod +x /fake/getconf
  printf "#!/bin/sh\n[ \"\$1\" = -m ] && echo armv7l || echo Linux\n" > /fake/uname && chmod +x /fake/uname
  out="$(PATH=/fake:$PATH bash /opt/a/b/install.sh --yes 2>&1)" && { echo "FAIL: installer did not refuse armv7l"; exit 1; }
  case "$out" in *"32-bit ARM is no longer built"*) echo "ok: refuses 32-bit ARM" ;; *) echo "FAIL: got: $out"; exit 1 ;; esac
  rm /fake/uname
  # Local mode: the layout of an unpacked release tarball.
  mkdir -p /opt/pkg/deploy/systemd
  cp /opt/a/b/install.sh /opt/pkg/deploy/systemd/install.sh
  touch /opt/pkg/deploy/systemd/twillingate.service
  printf "#!/bin/sh\n" > /opt/pkg/twillingate && chmod +x /opt/pkg/twillingate
  printf "#!/bin/sh\nexit 1\n" > /fake/getconf
  out="$(PATH=/fake:$PATH bash /opt/pkg/deploy/systemd/install.sh --yes 2>&1)" && { echo "FAIL: local mode did not refuse"; exit 1; }
  case "$out" in *"needs glibc 2.35 or newer; this system has none"*) echo "ok: local mode refuses too" ;; *) echo "FAIL: got: $out"; exit 1 ;; esac
'
