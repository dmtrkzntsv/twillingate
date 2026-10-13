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

# The glibc refusal names its reason (remote mode, before any download): a
# getconf that fails is a musl host, one that prints 2.31 is too old.
docker run --rm -v "$PWD/deploy/systemd/install.sh:/opt/a/b/install.sh:ro" debian:bookworm-slim bash -c '
  mkdir /fake && printf "#!/bin/sh\nexit 0\n" > /fake/curl && chmod +x /fake/curl
  refuses() { # <getconf script body> <expected text>
    printf "#!/bin/sh\n%s\n" "$1" > /fake/getconf && chmod +x /fake/getconf
    out="$(PATH=/fake:$PATH bash /opt/a/b/install.sh --yes 2>&1)" && { echo "FAIL: installer did not refuse"; exit 1; }
    case "$out" in *"$2"*) echo "ok: refuses with: $2" ;; *) echo "FAIL: wanted \"$2\", got: $out"; exit 1 ;; esac
  }
  refuses "exit 1" "needs glibc 2.35 or newer; this system has none"
  refuses "echo glibc 2.31" "found 2.31"
'
