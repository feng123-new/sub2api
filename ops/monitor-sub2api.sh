#!/usr/bin/env bash
set -Eeuo pipefail

PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
DEPLOY_DIR=/opt/sub2api-deploy
STATE_DIR=${SUB2API_MONITOR_STATE_DIR:-/home/fengyong/.local/state/sub2api-monitor}
STATUS_FILE="$STATE_DIR/status.txt"
ALERT_LOG="$STATE_DIR/alerts.log"
MODERATION_STATE_FILE="$STATE_DIR/moderation-failopen.last"
MODERATION_AUDIT_FAILURE_THRESHOLD=${SUB2API_MODERATION_AUDIT_FAILURE_THRESHOLD:-0}
MODERATION_NO_KEY_THRESHOLD=${SUB2API_MODERATION_NO_KEY_THRESHOLD:-0}
MODERATION_429_THRESHOLD=${SUB2API_MODERATION_429_THRESHOLD:-3}
mkdir -p "$STATE_DIR"
chmod 700 "$STATE_DIR"
ALERTS=()

add_alert() {
  ALERTS+=("$1")
}

check_container() {
  local name=$1 status
  status=$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$name" 2>/dev/null || true)
  [[ $status == healthy || $status == running ]] || add_alert "container:$name status:${status:-missing}"
}

moderation_failopen_pattern='content_moderation\.(audit_api_failed|skip_no_audit_api_keys|skip_config_load_failed|skip_unavailable)'

check_moderation_failopen() {
  local log_file=$1 count latest_hash previous_hash alert_text=""
  MODERATION_FAILOPEN_10M=$(grep -Eic "$moderation_failopen_pattern" "$log_file" || true)
  MODERATION_AUDIT_API_FAILED_10M=$(grep -Eic 'content_moderation\.audit_api_failed' "$log_file" || true)
  MODERATION_NO_AUDIT_API_KEYS_10M=$(grep -Eic 'content_moderation\.skip_no_audit_api_keys' "$log_file" || true)
  MODERATION_429_10M=$(grep -Eic 'content_moderation\.audit_api_failed.*(429|Too Many Requests)' "$log_file" || true)
  [[ ${MODERATION_AUDIT_API_FAILED_10M:-0} -gt ${MODERATION_AUDIT_FAILURE_THRESHOLD} ]] && alert_text="${alert_text} content_moderation_audit_api_failed_10m:$MODERATION_AUDIT_API_FAILED_10M"
  [[ ${MODERATION_NO_AUDIT_API_KEYS_10M:-0} -gt ${MODERATION_NO_KEY_THRESHOLD} ]] && alert_text="${alert_text} content_moderation_no_api_keys_10m:$MODERATION_NO_AUDIT_API_KEYS_10M"
  [[ ${MODERATION_429_10M:-0} -gt ${MODERATION_429_THRESHOLD} ]] && alert_text="${alert_text} content_moderation_429_10m:$MODERATION_429_10M"

  count=$((MODERATION_AUDIT_API_FAILED_10M + MODERATION_NO_AUDIT_API_KEYS_10M + MODERATION_429_10M))
  [[ $count -gt 0 ]] || return 0
  latest_hash=$(printf '%s|%s|%s|%s' "$MODERATION_AUDIT_API_FAILED_10M" "$MODERATION_NO_AUDIT_API_KEYS_10M" "$MODERATION_429_10M" "$(grep -Ei "$moderation_failopen_pattern" "$log_file" | tail -n 1)" | sha256sum | cut -d' ' -f1)
  previous_hash=$(cat "$MODERATION_STATE_FILE" 2>/dev/null || true)
  if [[ -n $latest_hash && $latest_hash != "$previous_hash" ]]; then
    add_alert "${alert_text# }"
    printf '%s\n' "$latest_hash" >"$MODERATION_STATE_FILE"
    chmod 600 "$MODERATION_STATE_FILE"
  fi
}

if [[ ${1:-} == --self-test-moderation-alert ]]; then
  fixture=$(mktemp)
  trap 'rm -f "$fixture"' EXIT
  printf '%s\n' \
    '2026-08-02T00:00:00Z ERROR service content_moderation.audit_api_failed {"error":"synthetic"}' \
    >"$fixture"
  check_moderation_failopen "$fixture"
  [[ ${#ALERTS[@]} -eq 1 && ${MODERATION_FAILOPEN_10M:-0} -eq 1 ]]
  ALERTS=()
  check_moderation_failopen "$fixture"
  [[ ${#ALERTS[@]} -eq 0 ]]
  printf '%s\n' 'moderation_alert_self_test=pass'
  exit 0
fi

check_container sub2api
check_container sub2api-postgres
check_container sub2api-redis

PUBLIC_CHECK=$(mktemp)
RECENT_LOGS=$(mktemp)
trap 'rm -f "$PUBLIC_CHECK" "$RECENT_LOGS"' EXIT

python3 - <<'PY' >"$PUBLIC_CHECK" 2>&1 || add_alert "public_settings_endpoint:failed"
import json
import urllib.request

with urllib.request.urlopen("http://127.0.0.1:8080/api/v1/settings/public", timeout=5) as response:
    data = json.load(response).get("data", {})
print("version=" + str(data.get("version")))
print("risk_control_enabled=" + str(data.get("risk_control_enabled")))
PY
if [[ -s $PUBLIC_CHECK ]]; then
  PUBLIC_INFO=$(cat "$PUBLIC_CHECK")
else
  PUBLIC_INFO="public_check=no_output"
fi

if [[ -n ${SUB2API_MONITOR_LOG_FILE:-} ]]; then
  cp "$SUB2API_MONITOR_LOG_FILE" "$RECENT_LOGS"
else
  docker logs --since 10m sub2api >"$RECENT_LOGS" 2>&1 || true
fi

IMAGE_VERSION=$(docker inspect -f '{{ index .Config.Labels "org.opencontainers.image.version" }}' sub2api 2>/dev/null || echo unknown)
IMAGE_REVISION=$(docker inspect -f '{{ index .Config.Labels "org.opencontainers.image.revision" }}' sub2api 2>/dev/null || echo unknown)
IMAGE_NAME=$(docker inspect -f '{{.Config.Image}}' sub2api 2>/dev/null || echo unknown)
DISK_USE=$(df -P /opt/sub2api-deploy | awk 'NR==2 {gsub(/%/,"",$5); print $5}')
BACKUP_DISK_USE=$(df -P /home/fengyong/backups | awk 'NR==2 {gsub(/%/,"",$5); print $5}')
[[ ${DISK_USE:-0} -lt 85 ]] || add_alert "disk:/opt/sub2api-deploy usage:${DISK_USE}%"
[[ ${BACKUP_DISK_USE:-0} -lt 85 ]] || add_alert "disk:/home/fengyong/backups usage:${BACKUP_DISK_USE}%"

RECENT_ERRORS=$(grep -Eic 'panic|fatal|database.*(failed|error)|redis.*(failed|error)|content_moderation.*failed|No available accounts|upstream rate limit| 50[234] | 429 ' "$RECENT_LOGS" || true)
[[ $RECENT_ERRORS -lt 20 ]] || add_alert "recent_errors_10m:$RECENT_ERRORS"
check_moderation_failopen "$RECENT_LOGS"

ALLOWLIST_ENV=$(docker inspect sub2api --format '{{range .Config.Env}}{{println .}}{{end}}' 2>/dev/null | grep -E '^SECURITY_URL_ALLOWLIST_(ENABLED|ALLOW_INSECURE_HTTP|ALLOW_PRIVATE_HOSTS)=' | sort || true)
grep -Fx 'SECURITY_URL_ALLOWLIST_ENABLED=true' <<<"$ALLOWLIST_ENV" >/dev/null || add_alert 'url_allowlist:disabled'
grep -Fx 'SECURITY_URL_ALLOWLIST_ALLOW_INSECURE_HTTP=false' <<<"$ALLOWLIST_ENV" >/dev/null || add_alert 'url_allowlist:http_allowed'
grep -Fx 'SECURITY_URL_ALLOWLIST_ALLOW_PRIVATE_HOSTS=false' <<<"$ALLOWLIST_ENV" >/dev/null || add_alert 'url_allowlist:private_hosts_allowed'

GEOFENCE_CN_STATUS=$(curl -k -sS -m 8 -H 'CF-IPCountry: CN' -o /dev/null -w '%{http_code}' https://sub2api.136.117.94.64.nip.io/health 2>/dev/null || true)
[[ $GEOFENCE_CN_STATUS == 403 ]] || add_alert "geofence_cn_status:${GEOFENCE_CN_STATUS:-failed}"
MODERATION_SKIP_EMPTY_10M=$(grep -c 'content_moderation.skip_empty_input' "$RECENT_LOGS" || true)
MODERATION_INPUT_EXTRACTED_10M=$(grep -c 'content_moderation.input_extracted' "$RECENT_LOGS" || true)
NOW=$(date -Is)

{
  echo "time=$NOW"
  echo "$PUBLIC_INFO"
  echo "image_name=$IMAGE_NAME"
  echo "image_version=$IMAGE_VERSION"
  echo "image_revision=$IMAGE_REVISION"
  echo "sub2api=$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' sub2api 2>/dev/null || echo missing)"
  echo "postgres=$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' sub2api-postgres 2>/dev/null || echo missing)"
  echo "redis=$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' sub2api-redis 2>/dev/null || echo missing)"
  echo "disk_opt=${DISK_USE:-unknown}%"
  echo "disk_backup=${BACKUP_DISK_USE:-unknown}%"
  echo "recent_errors_10m=$RECENT_ERRORS"
  echo "url_allowlist_enabled=$(grep -Fx 'SECURITY_URL_ALLOWLIST_ENABLED=true' <<<"$ALLOWLIST_ENV" >/dev/null && echo true || echo false)"
  echo "url_allowlist_allow_insecure_http=$(grep -Fx 'SECURITY_URL_ALLOWLIST_ALLOW_INSECURE_HTTP=true' <<<"$ALLOWLIST_ENV" >/dev/null && echo true || echo false)"
  echo "url_allowlist_allow_private_hosts=$(grep -Fx 'SECURITY_URL_ALLOWLIST_ALLOW_PRIVATE_HOSTS=true' <<<"$ALLOWLIST_ENV" >/dev/null && echo true || echo false)"
  echo "geofence_cn_status=${GEOFENCE_CN_STATUS:-unknown}"
  echo "moderation_skip_empty_10m=$MODERATION_SKIP_EMPTY_10M"
  echo "moderation_input_extracted_10m=$MODERATION_INPUT_EXTRACTED_10M"
  echo "moderation_failopen_10m=${MODERATION_FAILOPEN_10M:-0}"
  echo "moderation_audit_api_failed_10m=${MODERATION_AUDIT_API_FAILED_10M:-0}"
  echo "moderation_no_audit_api_keys_10m=${MODERATION_NO_AUDIT_API_KEYS_10M:-0}"
  echo "moderation_429_10m=${MODERATION_429_10M:-0}"
  echo "moderation_alert_thresholds=audit:${MODERATION_AUDIT_FAILURE_THRESHOLD},no_key:${MODERATION_NO_KEY_THRESHOLD},429:${MODERATION_429_THRESHOLD}"
  if [[ ${#ALERTS[@]} -eq 0 ]]; then
    echo 'alerts=0'
  else
    printf 'alerts=%s\n' "${ALERTS[*]}"
  fi
} >"$STATUS_FILE"

if [[ ${#ALERTS[@]} -gt 0 ]]; then
  ALERT_TEXT="${ALERTS[*]}"
  printf '%s ALERT %s\n' "$NOW" "$ALERT_TEXT" >>"$ALERT_LOG"
  logger -p user.err -t sub2api-monitor -- "ALERT $ALERT_TEXT"
  exit 1
fi

printf '%s OK\n' "$NOW" >>"$ALERT_LOG"
tail -n 1000 "$ALERT_LOG" >"$ALERT_LOG.tmp" && mv "$ALERT_LOG.tmp" "$ALERT_LOG"
