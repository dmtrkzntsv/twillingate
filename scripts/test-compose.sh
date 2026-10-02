#!/usr/bin/env bash
# End-to-end test of the published compose file: build the image, start it
# with the API on, send a hit, read it back over REST, and load /app/. It
# needs docker, so it is manual — `make check` does not run it.
set -euo pipefail
cd "$(dirname "$0")/.."

command -v docker > /dev/null || { echo "docker is required"; exit 1; }

dir="$(mktemp -d)"
project="twillingate-composetest-$$"

compose() {
  docker compose -p "$project" -f "$dir/docker-compose.yml" "$@"
}

cleanup() {
  compose down -v > /dev/null 2>&1 || true
  rm -rf "$dir"
}
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

echo "building the image..."
docker build --target runtime -t twillingate:composetest . > /dev/null

# The compose file names the published image; the test substitutes the one it
# just built so it exercises this working tree rather than the registry.
# shellcheck disable=SC2016  # the ${TWILLINGATE_VERSION} text is matched, not expanded
sed -e 's|ghcr.io/dmtrkzntsv/twillingate:${TWILLINGATE_VERSION:-latest}|twillingate:composetest|' \
    -e 's|"8080:8080"|"18080:8080"|' \
    deploy/compose/docker-compose.yml > "$dir/docker-compose.yml"

# A bare token turns the API (and /app/) on without a login page; a short
# flush makes the hit readable within seconds.
token="ar_composetest"
printf 'API_AUTH_DSN=token://%s\nBUFFER_FLUSH_INTERVAL=1s\n' "$token" > "$dir/.env"

compose up -d > /dev/null

echo "waiting for ingestion..."
for _ in $(seq 1 30); do
  curl -fsS -o /dev/null "http://127.0.0.1:18080/healthz" 2>/dev/null && break
  sleep 1
done

# Projects live in the database now: seed one through the CLI inside the
# running container instead of mounting a projects.json. A fresh database's
# first project is id 1.
echo "creating project via the CLI..."
compose exec -T twillingate \
  /usr/local/bin/twillingate project create -name Dev -origin "http://localhost:18080" \
  || fail "project create failed"
key="$(compose exec -T twillingate \
  /usr/local/bin/twillingate key issue -project-id 1 -label web | grep -o 'ak_[0-9a-f]*' | head -1)"
[ -n "$key" ] || fail "key issue failed"

# curl's default User-Agent is classified as a bot and the pageview would be
# accepted but never stored, so the request has to look like a browser.
ua='Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36'
# shellcheck disable=SC2016  # the $-prefixed names are JSON keys, not shell
post_events() {
  curl -s -o "$dir/events.out" -w '%{http_code}' -A "$ua" -X POST "http://127.0.0.1:18080/ingest/events" \
    -H 'Origin: http://localhost:18080' -H 'Content-Type: application/json' \
    -H "X-Analytics-Key: $key" \
    -d '{"attributes":{"$os":"ios","$app_version":"1.0","$install_id":"install-1"},
         "events":[
           {"name":"$page_view","attributes":{"$host":"localhost","$path":"/pricing"}},
           {"name":"$screen_view","attributes":{"$screen":"/settings"}},
           {"name":"signup","attributes":{"plan":"pro"}}]}'
}
# The key was just written out-of-process (docker compose exec), and the
# server's in-memory registry only notices out-of-process writes via its
# config_version poll (internal/manage/registry.go: pollInterval = 1s), so
# the very first POST can race that poll and 401. Retry rather than sleep
# blindly: it self-documents the propagation window instead of hard-coding
# a guess at it.
code=""
for _ in $(seq 1 10); do
  code="$(post_events)"
  [ "$code" = "202" ] && break
  [ "$code" = "401" ] || break
  sleep 0.5
done
[ "$code" = "202" ] || fail "/ingest/events returned $code: $(cat "$dir/events.out")"

# The pageview must come back out of the API once the buffer has flushed: a
# 202 alone only says the collector accepted it.
echo "waiting for the hit to reach the API..."
today="$(date -u +%Y-%m-%d)"
overview=""
for _ in $(seq 1 20); do
  overview="$(curl -s -H "Authorization: Bearer $token" \
    "http://127.0.0.1:18080/api/projects/1/views/overview?from=$today&to=$today")"
  case "$overview" in *"\"$today\""*) break ;; esac
  sleep 1
done
case "$overview" in *"\"$today\""*) ;; *) fail "views overview never showed $today: $overview" ;; esac

# The dashboards app is embedded in the binary; a build without it serves 503.
status="$(curl -s -o "$dir/app.out" -w '%{http_code}' "http://127.0.0.1:18080/app/")"
[ "$status" = "200" ] || fail "/app/ returned $status"
grep -q "<title>" "$dir/app.out" || fail "/app/ is not the dashboards page"

echo "PASS: ingestion, API and dashboards on :18080"
