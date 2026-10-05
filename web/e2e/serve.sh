#!/usr/bin/env bash
# Builds twillingate, seeds one demo project, and runs `serve` for the
# Playwright e2e suite (see playwright.config.ts, which runs this as its
# webServer and waits on /healthz). Everything lives under a scratch
# directory so runs never collide with a developer's local/twillingate.db.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$script_dir/../.." && pwd)"

# go build needs a real toolchain on PATH; some local setups keep it at
# /usr/local/go/bin without adding it to PATH (CI's setup-go already does).
if ! command -v go >/dev/null 2>&1 && [ -x /usr/local/go/bin/go ]; then
  export PATH="$PATH:/usr/local/go/bin"
fi

scratch="$(mktemp -d -t twillingate-e2e-XXXXXX)"
# Removed on the way out, binary and database with it. playwright.config.ts
# stops this script with SIGTERM (gracefulShutdown) rather than a SIGKILL
# no trap survives, so serve runs as a child, not exec'd over this shell.
trap 'rm -rf "$scratch"' EXIT
trap 'if [ -n "${pid:-}" ]; then kill -TERM "$pid" 2>/dev/null; wait "$pid"; fi; exit 143' TERM INT
bin="$scratch/twillingate"
db="$scratch/e2e.db"
env_file="$scratch/e2e.env"

(cd "$root" && make ui && go build -o "$bin" ./cmd/twillingate)

cat >"$env_file" <<EOF
DATABASE_DSN='sqlite://$db'
GEO_DSN='none://'
INGEST_ADDR='127.0.0.1:18080'
CONSOLE_AUTH_DSN='token://e2e-token?password=e2e-pass&resource=http://127.0.0.1:18080'
PUBLIC_URL='http://127.0.0.1:18080'
EOF

set -a
# shellcheck disable=SC1090
. "$env_file"
set +a

"$bin" migrate
# team_size and role, two of the custom attributes seed-demo.py puts on
# product events, fill the Product dashboard's attribute table; plan stays
# undeclared for projects.spec.ts to add as a breakdown.
"$bin" project create -name dev -attr team_size -attr role
python3 "$root/scripts/seed-demo.py" "$db"

"$bin" serve &
pid=$!
wait "$pid"
