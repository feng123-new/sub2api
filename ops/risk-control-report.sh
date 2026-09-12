#!/usr/bin/env bash
set -Eeuo pipefail

PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
DEPLOY_DIR=/opt/sub2api-deploy
STATE_DIR=/home/fengyong/.local/state/sub2api-risk-control
WINDOW_HOURS=${1:-24}

case "$WINDOW_HOURS" in
  ''|*[!0-9]*) echo "usage: $0 [window_hours_integer]" >&2; exit 2 ;;
esac
if [ "$WINDOW_HOURS" -lt 1 ] || [ "$WINDOW_HOURS" -gt 168 ]; then
  echo "window_hours must be between 1 and 168" >&2
  exit 2
fi

mkdir -p "$STATE_DIR"
chmod 700 "$STATE_DIR"

TS=$(date -u +%Y%m%dT%H%M%SZ)
OUT="$STATE_DIR/risk-control-report-$TS.md"
LATEST="$STATE_DIR/latest.md"
TMP_LOG=$(mktemp)
TMP_EVENTS=$(mktemp)
trap 'rm -f "$TMP_LOG" "$TMP_EVENTS"' EXIT

ALERTS=()
add_alert() { ALERTS+=("$1"); }

sql() {
  docker exec sub2api-postgres psql -U sub2api -d sub2api -Atc "$1"
}

table_sql() {
  docker exec sub2api-postgres psql -U sub2api -d sub2api -P pager=off -F ' | ' -Atc "$1"
}

container_status() {
  docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$1" 2>/dev/null || echo missing
}

public_json=$(curl -fsS -m 8 http://127.0.0.1:8080/api/v1/settings/public || echo '{}')
version=$(printf '%s' "$public_json" | jq -r '.data.version // "unknown"' 2>/dev/null || echo unknown)
risk_control_enabled=$(printf '%s' "$public_json" | jq -r '.data.risk_control_enabled // false' 2>/dev/null || echo false)
registration_enabled=$(printf '%s' "$public_json" | jq -r 'if (.data | has("registration_enabled")) then .data.registration_enabled else "unknown" end' 2>/dev/null || echo unknown)
payment_enabled=$(printf '%s' "$public_json" | jq -r 'if (.data | has("payment_enabled")) then .data.payment_enabled else "unknown" end' 2>/dev/null || echo unknown)
allow_user_view_error_requests=$(printf '%s' "$public_json" | jq -r 'if (.data | has("allow_user_view_error_requests")) then .data.allow_user_view_error_requests else "unknown" end' 2>/dev/null || echo unknown)

image_name=$(docker inspect -f '{{.Config.Image}}' sub2api 2>/dev/null || echo unknown)
image_id=$(docker inspect -f '{{.Image}}' sub2api 2>/dev/null || echo unknown)

allowlist_env=$(docker inspect sub2api --format '{{range .Config.Env}}{{println .}}{{end}}' 2>/dev/null | grep -E '^SECURITY_URL_ALLOWLIST_(ENABLED|ALLOW_INSECURE_HTTP|ALLOW_PRIVATE_HOSTS)=' | sort || true)
allowlist_enabled=$(printf '%s\n' "$allowlist_env" | grep -Fx 'SECURITY_URL_ALLOWLIST_ENABLED=true' >/dev/null && echo true || echo false)
allow_http=$(printf '%s\n' "$allowlist_env" | grep -Fx 'SECURITY_URL_ALLOWLIST_ALLOW_INSECURE_HTTP=true' >/dev/null && echo true || echo false)
allow_private=$(printf '%s\n' "$allowlist_env" | grep -Fx 'SECURITY_URL_ALLOWLIST_ALLOW_PRIVATE_HOSTS=true' >/dev/null && echo true || echo false)

[ "$risk_control_enabled" = true ] || add_alert "public_settings:risk_control_disabled"
[ "$allowlist_enabled" = true ] || add_alert "url_allowlist:disabled"
[ "$allow_http" = false ] || add_alert "url_allowlist:http_allowed"
[ "$allow_private" = false ] || add_alert "url_allowlist:private_hosts_allowed"

geofence_us_status=$(curl -k -sS -m 8 -H 'CF-IPCountry: US' -o /dev/null -w '%{http_code}' https://sub2api.136.117.94.64.nip.io/health 2>/dev/null || true)
geofence_cn_status=$(curl -k -sS -m 8 -H 'CF-IPCountry: CN' -o /dev/null -w '%{http_code}' https://sub2api.136.117.94.64.nip.io/health 2>/dev/null || true)
[ "$geofence_us_status" = 200 ] || add_alert "geofence_us_status:${geofence_us_status:-failed}"
[ "$geofence_cn_status" = 403 ] || add_alert "geofence_cn_status:${geofence_cn_status:-failed}"

moderation_status=$(sql "select coalesce(value::jsonb->>'enabled','false') || '|' || coalesce(value::jsonb->>'mode','') || '|' || coalesce(value::jsonb->>'sample_rate','') || '|' || jsonb_array_length(coalesce(value::jsonb->'api_keys','[]'::jsonb)) from settings where key='content_moderation_config'" 2>/dev/null || echo 'false|||0')
moderation_enabled=$(printf '%s' "$moderation_status" | cut -d'|' -f1)
moderation_mode=$(printf '%s' "$moderation_status" | cut -d'|' -f2)
moderation_sample_rate=$(printf '%s' "$moderation_status" | cut -d'|' -f3)
moderation_key_count=$(printf '%s' "$moderation_status" | cut -d'|' -f4)
[ "$moderation_enabled" = true ] || add_alert "content_moderation:disabled"
[ "${moderation_key_count:-0}" -gt 0 ] || add_alert "content_moderation:no_api_keys"

docker logs --since "${WINDOW_HOURS}h" sub2api >"$TMP_LOG" 2>&1 || true
python3 - "$TMP_LOG" >"$TMP_EVENTS" <<'PY'
import collections, json, re, sys
path = sys.argv[1]
pat = re.compile(r'(content_moderation\.[A-Za-z0-9_]+)\t(\{.*\})$')
events = collections.Counter()
by_key = collections.Counter()
body_sizes = collections.defaultdict(list)
with open(path, 'r', errors='replace') as fh:
    for line in fh:
        m = pat.search(line.rstrip('\n'))
        if not m:
            continue
        msg = m.group(1).split('.', 1)[1]
        try:
            data = json.loads(m.group(2))
        except Exception:
            continue
        events[msg] += 1
        endpoint = data.get('endpoint') or ''
        protocol = data.get('protocol') or ''
        by_key[(msg, endpoint, protocol)] += 1
        if msg == 'skip_empty_input':
            try:
                body_sizes[(endpoint, protocol)].append(int(data.get('body_bytes') or 0))
            except Exception:
                pass
print('EVENTS')
for key, value in events.most_common():
    print(f'{key}\t{value}')
print('DETAILS')
for (msg, endpoint, protocol), value in by_key.most_common(40):
    print(f'{msg}\t{endpoint}\t{protocol}\t{value}')
print('SKIP_SIZES')
for (endpoint, protocol), values in sorted(body_sizes.items()):
    if not values:
        continue
    values.sort()
    print(f'{endpoint}\t{protocol}\t{len(values)}\t{values[0]}\t{values[len(values)//2]}\t{values[-1]}')
PY

event_count() {
  awk -F '\t' -v key="$1" '$1 == key {print $2}' "$TMP_EVENTS" | head -1
}
gateway_checks=${gateway_checks:-$(event_count gateway_check_start)}
input_extracted=${input_extracted:-$(event_count input_extracted)}
skip_empty=${skip_empty:-$(event_count skip_empty_input)}
check_failed=${check_failed:-$(event_count check_failed)}
audit_api_failed=${audit_api_failed:-$(event_count audit_api_failed)}
skip_no_audit_api_keys=${skip_no_audit_api_keys:-$(event_count skip_no_audit_api_keys)}
cache_hits=${cache_hits:-$(event_count cache_hit)}
gateway_checks=${gateway_checks:-0}
input_extracted=${input_extracted:-0}
skip_empty=${skip_empty:-0}
check_failed=${check_failed:-0}
audit_api_failed=${audit_api_failed:-0}
skip_no_audit_api_keys=${skip_no_audit_api_keys:-0}
cache_hits=${cache_hits:-0}

if [ "$gateway_checks" -gt 0 ] && [ "$input_extracted" -eq 0 ]; then
  add_alert "content_moderation:no_extracted_inputs_with_gateway_checks"
fi
if [ "$check_failed" -gt 10 ]; then
  add_alert "content_moderation:check_failed_${check_failed}"
fi
if [ "$audit_api_failed" -gt 0 ]; then
  add_alert "content_moderation:audit_api_failed_${audit_api_failed}"
fi
if [ "$skip_no_audit_api_keys" -gt 0 ]; then
  add_alert "content_moderation:skip_no_audit_api_keys_${skip_no_audit_api_keys}"
fi

usage_summary=$(table_sql "select count(*) as requests, count(distinct user_id) as users, count(distinct api_key_id) as api_keys, coalesce(sum(coalesce(input_tokens,0)+coalesce(output_tokens,0)+coalesce(cache_creation_tokens,0)+coalesce(cache_read_tokens,0)+coalesce(image_output_tokens,0)),0) as tokens, coalesce(round(sum(coalesce(total_cost,0))::numeric,6),0) as total_cost from usage_logs where created_at > now() - interval '${WINDOW_HOURS} hours'" 2>/dev/null || echo '0 | 0 | 0 | 0 | 0')
ops_error_count=$(sql "select count(*) from ops_error_logs where created_at > now() - interval '${WINDOW_HOURS} hours'" 2>/dev/null || echo 0)
ops_5xx_count=$(sql "select count(*) from ops_error_logs where created_at > now() - interval '${WINDOW_HOURS} hours' and coalesce(status_code, upstream_status_code) between 500 and 599" 2>/dev/null || echo 0)
ops_429_count=$(sql "select count(*) from ops_error_logs where created_at > now() - interval '${WINDOW_HOURS} hours' and coalesce(status_code, upstream_status_code) = 429" 2>/dev/null || echo 0)

api_key_rate_summary=$(table_sql "select count(*) as active_keys, count(*) filter (where coalesce(rate_limit_5h,0) <= 0 and coalesce(rate_limit_1d,0) <= 0 and coalesce(rate_limit_7d,0) <= 0) as no_window_limit, count(*) filter (where expires_at is null) as no_expiry, count(*) filter (where coalesce(quota,0) <= 0) as no_quota from api_keys where deleted_at is null and status='active'" 2>/dev/null || echo '0 | 0 | 0 | 0')

cat >"$OUT" <<EOF
# Sub2API Risk-Control Report

- Time UTC: $(date -u -Is)
- Window: last ${WINDOW_HOURS}h
- Image: ${image_name}
- Image ID: ${image_id}
- Version: ${version}
- Alerts: ${#ALERTS[@]}

## Community-Derived Control Matrix

| Control area | Community signal | Current status | Next action |
|---|---|---|---|
| Prompt injection / unsafe content | OWASP LLM Top 10 and OpenAI safety guidance recommend layered moderation and monitoring | Content moderation enabled, pre_block, sample_rate=${moderation_sample_rate}, keys=${moderation_key_count}; extraction patch deployed | Keep request-shape regression tests and monitor extraction coverage |
| Sensitive data and leakage | OWASP highlights sensitive information disclosure | Recent log secret-pattern scan is separate monitor work; this report avoids secret values | Keep scans in release/incident checklist |
| Model DoS and cost spikes | LLM gateway practice recommends token-aware limits, quotas, and tiered access | Usage summary and active key limit distribution are visible below | Add token/cost SLO thresholds once normal baseline is stable |
| SSRF / unsafe upstreams | Gateway hardening requires outbound URL allowlists and private-host blocking | URL allowlist enabled; HTTP/private hosts disabled | Keep allowlist changes audited and tested |
| Excessive agency / tool risk | OWASP flags excessive agency; tool loops need special treatment | Tool-continuation requests with user text are now moderated; pure tool-output skips remain observable | Split skip reasons in application logs in future upstream patch |
| Observability and rollback | OpenAI production guidance stresses measurable logs, evals, rollback | Report, monitor, remediation note, backup, and rollback path exist | Add scheduled baseline comparison if traffic grows |

## Runtime Guardrails

| Check | Value |
|---|---|
| risk_control_enabled | ${risk_control_enabled} |
| registration_enabled | ${registration_enabled} |
| payment_enabled | ${payment_enabled} |
| allow_user_view_error_requests | ${allow_user_view_error_requests} |
| url_allowlist_enabled | ${allowlist_enabled} |
| url_allowlist_allow_insecure_http | ${allow_http} |
| url_allowlist_allow_private_hosts | ${allow_private} |
| geofence_us_status | ${geofence_us_status:-unknown} |
| geofence_cn_status | ${geofence_cn_status:-unknown} |
| content_moderation_enabled | ${moderation_enabled} |
| content_moderation_mode | ${moderation_mode} |
| content_moderation_sample_rate | ${moderation_sample_rate} |
| content_moderation_key_count | ${moderation_key_count} |

## Health

| Component | Status |
|---|---|
| sub2api | $(container_status sub2api) |
| postgres | $(container_status sub2api-postgres) |
| redis | $(container_status sub2api-redis) |

## Moderation Event Coverage From Logs

| Event | Count |
|---|---:|
EOF
awk 'BEGIN{section=0} /^EVENTS$/{section=1; next} /^DETAILS$/{section=0} section==1 && NF>=2 {printf "| %s | %s |\n", $1, $2}' "$TMP_EVENTS" >>"$OUT"
cat >>"$OUT" <<EOF

- audit_api_failed=${audit_api_failed}
- skip_no_audit_api_keys=${skip_no_audit_api_keys}
- cache_hit=${cache_hits}

### Moderation Event Detail

| Event | Endpoint | Protocol | Count |
|---|---|---|---:|
EOF
awk 'BEGIN{section=0; FS="\t"} /^DETAILS$/{section=1; next} /^SKIP_SIZES$/{section=0} section==1 && NF>=4 {printf "| %s | %s | %s | %s |\n", $1, $2, $3, $4}' "$TMP_EVENTS" >>"$OUT"
cat >>"$OUT" <<EOF

### skip_empty_input Body Size Distribution

| Endpoint | Protocol | Count | Min bytes | Median bytes | Max bytes |
|---|---|---:|---:|---:|---:|
EOF
awk 'BEGIN{section=0; FS="\t"} /^SKIP_SIZES$/{section=1; next} section==1 && NF>=6 {printf "| %s | %s | %s | %s | %s | %s |\n", $1, $2, $3, $4, $5, $6}' "$TMP_EVENTS" >>"$OUT"
cat >>"$OUT" <<EOF

## Usage And Abuse Signals

| Metric | Value |
|---|---|
| usage_summary requests/users/api_keys/tokens/total_cost | ${usage_summary} |
| ops_error_count | ${ops_error_count} |
| ops_5xx_count | ${ops_5xx_count} |
| ops_429_count | ${ops_429_count} |
| active_api_keys/no_window_limit/no_expiry/no_quota | ${api_key_rate_summary} |

### Top Endpoints By Usage

| Endpoint | Requests | Tokens | Cost |
|---|---:|---:|---:|
EOF
table_sql "select coalesce(inbound_endpoint,'(none)'), count(*), coalesce(sum(coalesce(input_tokens,0)+coalesce(output_tokens,0)+coalesce(cache_creation_tokens,0)+coalesce(cache_read_tokens,0)+coalesce(image_output_tokens,0)),0), coalesce(round(sum(coalesce(total_cost,0))::numeric,6),0) from usage_logs where created_at > now() - interval '${WINDOW_HOURS} hours' group by 1 order by count(*) desc limit 12" 2>/dev/null | awk -F ' \| ' '{printf "| %s | %s | %s | %s |\n", $1, $2, $3, $4}' >>"$OUT" || true
cat >>"$OUT" <<EOF

### Top Models By Usage

| Model | Requests | Tokens | Cost |
|---|---:|---:|---:|
EOF
table_sql "select coalesce(requested_model, model, '(none)'), count(*), coalesce(sum(coalesce(input_tokens,0)+coalesce(output_tokens,0)+coalesce(cache_creation_tokens,0)+coalesce(cache_read_tokens,0)+coalesce(image_output_tokens,0)),0), coalesce(round(sum(coalesce(total_cost,0))::numeric,6),0) from usage_logs where created_at > now() - interval '${WINDOW_HOURS} hours' group by 1 order by count(*) desc limit 12" 2>/dev/null | awk -F ' \| ' '{printf "| %s | %s | %s | %s |\n", $1, $2, $3, $4}' >>"$OUT" || true
cat >>"$OUT" <<EOF

### Top API Keys By Request Count (IDs Only)

| User ID | API Key ID | Requests | Tokens | Cost |
|---:|---:|---:|---:|---:|
EOF
table_sql "select coalesce(user_id,0), coalesce(api_key_id,0), count(*), coalesce(sum(coalesce(input_tokens,0)+coalesce(output_tokens,0)+coalesce(cache_creation_tokens,0)+coalesce(cache_read_tokens,0)+coalesce(image_output_tokens,0)),0), coalesce(round(sum(coalesce(total_cost,0))::numeric,6),0) from usage_logs where created_at > now() - interval '${WINDOW_HOURS} hours' group by 1,2 order by count(*) desc limit 12" 2>/dev/null | awk -F ' \| ' '{printf "| %s | %s | %s | %s | %s |\n", $1, $2, $3, $4, $5}' >>"$OUT" || true
cat >>"$OUT" <<EOF

### Top Error Buckets

| Severity | Owner | Type | Status | Path | Model | Count |
|---|---|---|---:|---|---|---:|
EOF
table_sql "select coalesce(severity,''), coalesce(error_owner,''), coalesce(error_type,''), coalesce(status_code, upstream_status_code,0), coalesce(inbound_endpoint, request_path, ''), coalesce(requested_model, model, ''), count(*) from ops_error_logs where created_at > now() - interval '${WINDOW_HOURS} hours' group by 1,2,3,4,5,6 order by count(*) desc limit 15" 2>/dev/null | awk -F ' \| ' '{printf "| %s | %s | %s | %s | %s | %s | %s |\n", $1, $2, $3, $4, $5, $6, $7}' >>"$OUT" || true

cat >>"$OUT" <<EOF

## Alerts

EOF
if [ "${#ALERTS[@]}" -eq 0 ]; then
  echo "- none" >>"$OUT"
else
  printf -- '- %s\n' "${ALERTS[@]}" >>"$OUT"
fi

cat >>"$OUT" <<EOF

## Recommended Next Engineering Work

1. Add first-class skip-reason taxonomy in application code: tool_only_no_user_text, assistant_ended_request, unsupported_request_shape, invalid_json, extraction_failed.
2. Expose moderation coverage in the admin risk-control page by endpoint/model/group/API key.
3. Add token-aware anomaly thresholds after one week of baseline reports.
4. Add a fixed request-shape regression suite for Chat, Responses, Gemini, Anthropic, image, and tool-continuation traffic.
5. Add config-change audit events for moderation, URL allowlist, geofence, registration/payment, and error-passthrough settings.
EOF

cp "$OUT" "$LATEST"
chmod 600 "$OUT" "$LATEST"
echo "$OUT"

if [ "${#ALERTS[@]}" -gt 0 ]; then
  exit 1
fi
