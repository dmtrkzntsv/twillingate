#!/usr/bin/env bash
# Builds twillingate, seeds one demo project, and runs `serve` for the
# Playwright e2e suite (see playwright.config.ts, which runs this as its
# webServer and waits for the line it prints once the event files settle,
# below). Everything lives under a scratch
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
INGEST_URL='http://127.0.0.1:18080'
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
# Widget SQL reads the event files, not SQLite: the seed writes its events as
# closed logs under EVENTS_DIR (raw/ beside the database by default). Serve
# starts at once, as on upgrade day: its boot pass dumps and merges them while
# the suite already queries.
python3 "$root/scripts/seed-demo.py" "$db" --events-dir "${EVENTS_DIR:-$(dirname "$db")/raw}"

"$bin" serve &
pid=$!

# Settle. The first start after the seed runs the boot pass in the
# background: it dumps every seeded day into Parquet, merges the months
# and derives the tables, and until it ends every query over the files is
# slow (the seed's 180 raw days are far more than an upgrade's ~30). So the
# suite starts only when this script prints the line playwright.config.ts
# waits for. Assumes the files sit under EVENTS_DIR, which defaults to raw/
# beside the database, one numbered directory per project, and that the
# pass is done when
#   - no log (.log), closed log (.closed) or claimed log (.dumping) is left
#     for a day before today (UTC), and
#   - every project directory holds its derived identities.parquet and
#     aliases.parquet, and
#   - the listing held still between two polls.
events_dir="${EVENTS_DIR:-$(dirname "$db")/raw}"
today="$(date -u +%F)"
settled() {
  local dir day
  for dir in "$events_dir"/[0-9]*/; do
    [ -d "$dir" ] || continue
    [ -f "${dir}identities.parquet" ] && [ -f "${dir}aliases.parquet" ] || return 1
    for f in "$dir"*.log "$dir"*.closed "$dir"*.dumping; do
      [ -e "$f" ] || continue
      day="$(basename "$f")"
      [[ "${day:0:10}" < "$today" ]] && return 1
    done
  done
  return 0
}
listing() { find "$events_dir" -type f -printf '%P %s %T@\n' 2>/dev/null | sort || true; }

last=""
ok=""
for _ in $(seq 1 120); do
  kill -0 "$pid" 2>/dev/null || { echo "e2e: serve exited before the files settled" >&2; exit 1; }
  now="$(listing)"
  if settled && [ -n "$now" ] && [ "$now" = "$last" ]; then ok=1; break; fi
  last="$now"
  sleep 2
done
if [ -z "$ok" ]; then
  echo "e2e: the event files under $events_dir did not settle within 240s" >&2
  kill -TERM "$pid" 2>/dev/null || true
  wait "$pid" || true
  exit 1
fi
echo "e2e: event files settled"

wait "$pid"
