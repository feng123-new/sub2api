from __future__ import annotations

import json
import urllib.parse
from typing import Any

from .core import AgentIdentity, OAuth2AgentError, _json_request, parse_oauth_document


def _admin_headers(admin_api_key: str | None, admin_jwt: str | None) -> dict[str, str]:
    if admin_api_key:
        return {"x-api-key": admin_api_key}
    if admin_jwt:
        return {"Authorization": f"Bearer {admin_jwt}"}
    raise OAuth2AgentError("Sub2API admin authentication is required (--admin-api-key or --admin-jwt)")


def _unwrap_json(body: bytes, operation: str) -> Any:
    try:
        document = json.loads(body)
    except json.JSONDecodeError as exc:
        raise OAuth2AgentError(f"{operation} returned invalid JSON") from exc
    if isinstance(document, dict) and "data" in document and len(document) <= 4:
        return document["data"]
    return document


def pull_oauth_from_sub2api(
    *,
    base_url: str,
    account_id: int,
    admin_api_key: str | None = None,
    admin_jwt: str | None = None,
):
    query = urllib.parse.urlencode({"ids": str(account_id), "include_proxies": "false"})
    url = base_url.rstrip("/") + "/api/v1/admin/accounts/data?" + query
    status, _headers, body = _json_request(
        "GET",
        url,
        headers=_admin_headers(admin_api_key, admin_jwt),
        timeout=30,
    )
    if status < 200 or status >= 300:
        detail = body.decode("utf-8", errors="replace")[:1000].strip()
        raise OAuth2AgentError(f"Sub2API export returned HTTP {status}: {detail}")
    document = _unwrap_json(body, "Sub2API export")
    return parse_oauth_document(document)


def push_identity_to_sub2api(
    identity: AgentIdentity,
    *,
    base_url: str,
    admin_api_key: str | None = None,
    admin_jwt: str | None = None,
    name: str | None = None,
    update_existing: bool = False,
) -> Any:
    url = base_url.rstrip("/") + "/api/v1/admin/accounts/import/codex-session"
    identity_document = identity.to_sub2api_document()
    payload: dict[str, Any] = {
        "content": json.dumps(identity_document, ensure_ascii=False, separators=(",", ":")),
        "update_existing": update_existing,
        "skip_default_group_bind": True,
    }
    if name:
        payload["name"] = name
    status, _headers, body = _json_request(
        "POST",
        url,
        headers=_admin_headers(admin_api_key, admin_jwt),
        json_body=payload,
        timeout=120,
    )
    if status < 200 or status >= 300:
        detail = body.decode("utf-8", errors="replace")[:1000].strip()
        raise OAuth2AgentError(f"Sub2API import returned HTTP {status}: {detail}")
    return _unwrap_json(body, "Sub2API import")
