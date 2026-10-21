#!/usr/bin/env bash
# Starts the real app (no tray) against a vault in a folder, drives it
# from the command line and its settings API, then stops it. Runs on
# Linux and on Windows (Git Bash) in CI.
set -euo pipefail
exe="${1:?path to orosdrive binary}"
t="$(mktemp -d)"
if command -v cygpath >/dev/null; then t="$(cygpath -m "$t")"; fi # Windows: C:/... for the native exe
export OROS_DRIVE_CONFIG="$t/config" OROS_DRIVE_NO_KEYRING=1 OROS_DRIVE_STORE_DIR="$t/cloud"
mkdir -p "$t/Docs"
echo "hello" > "$t/Docs/a.txt"
touch -d '1 hour ago' "$t/Docs/a.txt"
"$exe" run --no-tray --background > "$t/run.log" 2>&1 &
pid=$!
for i in $(seq 1 50); do [ -f "$t/config/instance.json" ] && break; sleep 0.2; done
port=$(sed -E 's/.*"port":([0-9]+).*/\1/' "$t/config/instance.json")
tok=$(sed -E 's/.*"token":"([0-9a-f]+)".*/\1/' "$t/config/instance.json")
api() { curl -m 10 -fsS -H "X-OD: 1" -H "Content-Type: application/json" -b "orosdrive=$tok" "http://127.0.0.1:$port/api/$1" ${2:+-d "$2"}; }
test "$(curl -m 10 -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/api/status")" = 403
api unlock '{"passphrase":"smoke","create":true}' >/dev/null
"$exe" add "$t/Docs"
"$exe" sync
for i in $(seq 1 50); do api status | grep -q '"kind":"upload"' && break; sleep 0.2; done
api status | grep -q '"kind":"upload"' || { api status; cat "$t/run.log"; exit 1; }
"$exe" folders
# Never hang CI on a process that will not stop.
kill "$pid" 2>/dev/null || true
for i in $(seq 1 50); do kill -0 "$pid" 2>/dev/null || break; sleep 0.2; done
kill -9 "$pid" 2>/dev/null || true
wait "$pid" 2>/dev/null || true
echo "smoke ok"
