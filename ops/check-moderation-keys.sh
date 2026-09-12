#!/usr/bin/env bash
set -Eeuo pipefail

STATE_DIR=${SUB2API_MODERATION_KEY_STATE_DIR:-/home/fengyong/.local/state/sub2api-moderation-keys}
MIN_REMAINING_REQUESTS=${SUB2API_MODERATION_KEY_MIN_REMAINING_REQUESTS:-2}
mkdir -p "$STATE_DIR"
chmod 700 "$STATE_DIR"
REPORT="$STATE_DIR/latest.txt"
TMP=$(mktemp)
CONFIG_FILE=$(mktemp)
trap 'rm -f "$TMP" "$CONFIG_FILE"' EXIT

CONFIG=$(docker exec sub2api-postgres psql -U sub2api -d sub2api -Atc "select value from settings where key='content_moderation_config'")
printf '%s' "$CONFIG" >"$CONFIG_FILE"
chmod 600 "$CONFIG_FILE"
python3 - "$REPORT" "$MIN_REMAINING_REQUESTS" "$CONFIG_FILE" <<'PY' >"$TMP"
import hashlib
import json
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timezone

report_path = sys.argv[1]
min_remaining = int(sys.argv[2])
with open(sys.argv[3], encoding="utf-8") as config_file:
    config = json.load(config_file)
base_url = str(config.get("base_url") or "https://api.openai.com").rstrip("/")
keys = list(config.get("api_keys") or [])
if config.get("api_key"):
    keys.append(config["api_key"])
keys = [str(key).strip() for key in keys if str(key).strip()]

rows = []
failures = 0
low_remaining = 0
for index, key in enumerate(keys, 1):
    digest = hashlib.sha256(key.encode()).hexdigest()[:12]
    request = urllib.request.Request(base_url + "/v1/models", headers={
        "Authorization": "Bearer " + key,
        "User-Agent": "sub2api-moderation-key-probe/1",
    }, method="GET")
    status = 0
    error = ""
    headers = {}
    started = time.monotonic()
    try:
        with urllib.request.urlopen(request, timeout=8) as response:
            status = response.status
            headers = {k.lower(): v for k, v in response.headers.items()}
            response.read(64)
    except urllib.error.HTTPError as exc:
        status = exc.code
        headers = {k.lower(): v for k, v in exc.headers.items()}
        error = "rate_limit" if status == 429 else ("auth" if status in (401, 403) else "http_error")
    except Exception as exc:
        error = type(exc).__name__
    latency_ms = int((time.monotonic() - started) * 1000)
    remaining = headers.get("x-ratelimit-remaining-requests", "not_exposed")
    limit = headers.get("x-ratelimit-limit-requests", "not_exposed")
    reset = headers.get("x-ratelimit-reset-requests", "not_exposed")
    token_remaining = headers.get("x-ratelimit-remaining-tokens", "not_exposed")
    if remaining.isdigit() and int(remaining) <= min_remaining:
        low_remaining += 1
    if status < 200 or status >= 300:
        failures += 1
    rows.append((index, digest, status, latency_ms, limit, remaining, reset, token_remaining, error or "ok"))

now = datetime.now(timezone.utc).isoformat()
with open(report_path, "w", encoding="utf-8") as report:
    report.write("time_utc=" + now + "\n")
    report.write("base_url_host=" + urllib.parse.urlparse(base_url).netloc + "\n")
    report.write("key_count=" + str(len(keys)) + "\n")
    report.write("prepaid_balance=not_exposed_by_api_key_endpoint\n")
    report.write("min_remaining_requests=" + str(min_remaining) + "\n")
    report.write("failed_keys=" + str(failures) + "\n")
    report.write("low_remaining_keys=" + str(low_remaining) + "\n")
    report.write("index|key_sha256_12|status|latency_ms|limit_requests|remaining_requests|reset_requests|remaining_tokens|result\n")
    for row in rows:
        report.write("|".join(str(value) for value in row) + "\n")

print("key_count=%d failed_keys=%d low_remaining_keys=%d report=%s" % (len(keys), failures, low_remaining, report_path))
if failures or low_remaining:
    raise SystemExit(1)
PY
chmod 600 "$REPORT"
cat "$TMP"
