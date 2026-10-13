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
