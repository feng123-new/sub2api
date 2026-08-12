from __future__ import annotations

import base64
import json
import os
import platform
import struct
import tempfile
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Iterable

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric import ed25519

VERSION = "0.1.0"
DEFAULT_AUTH_API_BASE = "https://auth.openai.com/api/accounts"
DEFAULT_CODEX_BASE = "https://chatgpt.com/backend-api/codex"
USER_AGENT = f"oauth2agent/{VERSION}"


class OAuth2AgentError(RuntimeError):
    pass


@dataclass(frozen=True)
class OAuthMaterial:
    access_token: str
    account_id: str
    chatgpt_user_id: str
    email: str | None = None
    plan_type: str = "unknown"
    chatgpt_account_is_fedramp: bool = False


@dataclass(frozen=True)
class GeneratedKeyMaterial:
    private_key: ed25519.Ed25519PrivateKey
    private_key_pkcs8_base64: str
    public_key_ssh: str


@dataclass(frozen=True)
class AgentIdentity:
    agent_runtime_id: str
    agent_private_key: str
    task_id: str
    account_id: str
    chatgpt_user_id: str
    email: str | None
    plan_type: str
    chatgpt_account_is_fedramp: bool

    def to_sub2api_document(self) -> dict[str, Any]:
        identity: dict[str, Any] = {
            "agent_runtime_id": self.agent_runtime_id,
            "agent_private_key": self.agent_private_key,
            "task_id": self.task_id,
            "account_id": self.account_id,
            "chatgpt_user_id": self.chatgpt_user_id,
            "plan_type": self.plan_type,
            "chatgpt_account_is_fedramp": self.chatgpt_account_is_fedramp,
        }
        if self.email:
            identity["email"] = self.email
        return {
            "auth_mode": "agentIdentity",
            "agent_identity": identity,
        }

    def to_codex_auth_json(self) -> dict[str, Any]:
        identity = self.to_sub2api_document()["agent_identity"]
        return {
            "auth_mode": "agentIdentity",
            "OPENAI_API_KEY": None,
            "tokens": None,
            "last_refresh": None,
            "agent_identity": identity,
            "personal_access_token": None,
            "bedrock_api_key": None,
        }


def _b64url_decode(value: str) -> bytes:
    return base64.urlsafe_b64decode(value + "=" * (-len(value) % 4))


def _b64url_encode(value: bytes) -> str:
    return base64.urlsafe_b64encode(value).rstrip(b"=").decode("ascii")


def decode_jwt_payload(token: str) -> dict[str, Any]:
    parts = token.split(".")
    if len(parts) != 3 or not all(parts):
        raise OAuth2AgentError("token is not a three-part JWT")
    try:
        payload = json.loads(_b64url_decode(parts[1]))
    except (ValueError, json.JSONDecodeError) as exc:
        raise OAuth2AgentError("JWT payload is invalid") from exc
    if not isinstance(payload, dict):
        raise OAuth2AgentError("JWT payload must be a JSON object")
    return payload


def _walk_values(obj: Any) -> Iterable[tuple[str, Any]]:
    if isinstance(obj, dict):
        for key, value in obj.items():
            yield str(key), value
        for value in obj.values():
            yield from _walk_values(value)
    elif isinstance(obj, list):
        for value in obj:
            yield from _walk_values(value)


def _first_string(obj: Any, keys: set[str]) -> str | None:
    normalized = {key.lower() for key in keys}
    for key, value in _walk_values(obj):
        if key.lower() in normalized and isinstance(value, str) and value.strip():
            return value.strip()
    return None


def _first_bool(obj: Any, keys: set[str]) -> bool | None:
    normalized = {key.lower() for key in keys}
    for key, value in _walk_values(obj):
        if key.lower() in normalized and isinstance(value, bool):
            return value
    return None


def parse_oauth_document(document: Any) -> OAuthMaterial:
    if not isinstance(document, (dict, list)):
        raise OAuth2AgentError("OAuth JSON must be an object or array")

    access_token = _first_string(document, {"access_token", "accessToken"})
    if not access_token:
        raise OAuth2AgentError("OAuth JSON does not contain access_token")
    id_token = _first_string(document, {"id_token", "idToken"})

    claims_candidates: list[dict[str, Any]] = []
    for token in (id_token, access_token):
        if not token:
            continue
        try:
            claims_candidates.append(decode_jwt_payload(token))
        except OAuth2AgentError:
            continue

    openai_auth: dict[str, Any] = {}
    email: str | None = _first_string(document, {"email"})
    plan_type: str | None = _first_string(document, {"plan_type", "chatgpt_plan_type"})
    account_id: str | None = _first_string(document, {"chatgpt_account_id", "account_id"})
    chatgpt_user_id: str | None = _first_string(document, {"chatgpt_user_id"})
    fedramp = _first_bool(document, {"chatgpt_account_is_fedramp"})

    for claims in claims_candidates:
        candidate = claims.get("https://api.openai.com/auth")
        if isinstance(candidate, dict):
            openai_auth = {**candidate, **openai_auth}
        if not email and isinstance(claims.get("email"), str):
            email = claims["email"].strip() or None
        profile = claims.get("https://api.openai.com/profile")
        if not email and isinstance(profile, dict) and isinstance(profile.get("email"), str):
            email = profile["email"].strip() or None

    if not account_id:
        value = openai_auth.get("chatgpt_account_id")
        if isinstance(value, str) and value.strip():
            account_id = value.strip()
    if not chatgpt_user_id:
        value = openai_auth.get("chatgpt_user_id") or openai_auth.get("user_id")
        if isinstance(value, str) and value.strip():
            chatgpt_user_id = value.strip()
    if not plan_type:
        value = openai_auth.get("chatgpt_plan_type")
        if isinstance(value, str) and value.strip():
            plan_type = value.strip()
    if fedramp is None:
        fedramp = bool(openai_auth.get("chatgpt_account_is_fedramp", False))

    if not account_id:
        raise OAuth2AgentError("unable to determine chatgpt_account_id from OAuth JSON/JWT")
    if not chatgpt_user_id:
        raise OAuth2AgentError("unable to determine chatgpt_user_id from OAuth JSON/JWT")

    return OAuthMaterial(
        access_token=access_token,
        account_id=account_id,
        chatgpt_user_id=chatgpt_user_id,
        email=email,
        plan_type=plan_type or "unknown",
        chatgpt_account_is_fedramp=bool(fedramp),
    )


def load_oauth_file(path: str | os.PathLike[str]) -> OAuthMaterial:
    try:
        document = json.loads(Path(path).read_text(encoding="utf-8"))
    except OSError as exc:
        raise OAuth2AgentError(f"failed to read OAuth file: {exc}") from exc
    except json.JSONDecodeError as exc:
        raise OAuth2AgentError(f"OAuth file is not valid JSON: {exc}") from exc
    return parse_oauth_document(document)


def generate_key_material() -> GeneratedKeyMaterial:
    private_key = ed25519.Ed25519PrivateKey.generate()
    private_der = private_key.private_bytes(
        encoding=serialization.Encoding.DER,
        format=serialization.PrivateFormat.PKCS8,
        encryption_algorithm=serialization.NoEncryption(),
    )
    public_raw = private_key.public_key().public_bytes(
        encoding=serialization.Encoding.Raw,
        format=serialization.PublicFormat.Raw,
    )
    algorithm = b"ssh-ed25519"
    blob = (
        struct.pack(">I", len(algorithm))
        + algorithm
        + struct.pack(">I", len(public_raw))
        + public_raw
    )
    return GeneratedKeyMaterial(
        private_key=private_key,
        private_key_pkcs8_base64=base64.b64encode(private_der).decode("ascii"),
        public_key_ssh="ssh-ed25519 " + base64.b64encode(blob).decode("ascii"),
    )


def _timestamp() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds").replace("+00:00", "Z")


def _json_request(
    method: str,
    url: str,
    *,
    headers: dict[str, str] | None = None,
    json_body: dict[str, Any] | None = None,
    timeout: float = 30,
) -> tuple[int, dict[str, str], bytes]:
    request_headers = {"User-Agent": USER_AGENT, "Accept": "application/json"}
    request_headers.update(headers or {})
    data = None
    if json_body is not None:
        data = json.dumps(json_body, separators=(",", ":")).encode("utf-8")
        request_headers["Content-Type"] = "application/json"
    request = urllib.request.Request(url, data=data, headers=request_headers, method=method)
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            return response.status, dict(response.headers.items()), response.read()
    except urllib.error.HTTPError as exc:
        return exc.code, dict(exc.headers.items()), exc.read()
    except OSError as exc:
        raise OAuth2AgentError(f"request failed: {method} {url}: {exc}") from exc


def _expect_json_success(method: str, url: str, **kwargs: Any) -> dict[str, Any]:
    status, _headers, body = _json_request(method, url, **kwargs)
    if status < 200 or status >= 300:
        detail = body.decode("utf-8", errors="replace")[:1000].strip()
        raise OAuth2AgentError(f"{method} {url} returned HTTP {status}: {detail}")
    try:
        result = json.loads(body or b"{}")
    except json.JSONDecodeError as exc:
        raise OAuth2AgentError(f"{method} {url} returned invalid JSON") from exc
    if not isinstance(result, dict):
        raise OAuth2AgentError(f"{method} {url} did not return a JSON object")
    return result


def build_abom() -> dict[str, str]:
    return {
        "agent_version": VERSION,
        "agent_harness_id": "codex-cli",
        "running_location": f"cli-{platform.system().lower() or 'unknown'}",
    }


def register_agent_identity(
    oauth: OAuthMaterial,
    key_material: GeneratedKeyMaterial,
    *,
    auth_api_base: str = DEFAULT_AUTH_API_BASE,
) -> str:
    url = auth_api_base.rstrip("/") + "/v1/agent/register"
    headers = {"Authorization": f"Bearer {oauth.access_token}"}
    if oauth.chatgpt_account_is_fedramp:
        headers["X-OpenAI-Fedramp"] = "true"
    result = _expect_json_success(
        "POST",
        url,
        headers=headers,
        json_body={
            "abom": build_abom(),
            "agent_public_key": key_material.public_key_ssh,
            "capabilities": ["responsesapi"],
            "ttl": None,
        },
        timeout=20,
    )
    runtime_id = result.get("agent_runtime_id") or result.get("agentRuntimeId")
    if not isinstance(runtime_id, str) or not runtime_id.strip():
        raise OAuth2AgentError("agent registration response omitted agent_runtime_id")
    return runtime_id.strip()


def _sign(private_key: ed25519.Ed25519PrivateKey, payload: str) -> str:
    return base64.b64encode(private_key.sign(payload.encode("utf-8"))).decode("ascii")


def _decrypt_task_id(private_key: ed25519.Ed25519PrivateKey, encrypted_task_id: str) -> str:
    try:
        from nacl.bindings import crypto_box_seal_open
        from nacl.bindings import crypto_sign_ed25519_pk_to_curve25519
        from nacl.bindings import crypto_sign_ed25519_sk_to_curve25519
        from nacl.bindings import crypto_sign_seed_keypair
    except ImportError as exc:
        raise OAuth2AgentError(
            "server returned encrypted_task_id; install PyNaCl (`python -m pip install PyNaCl`) to decrypt it"
        ) from exc

    seed = private_key.private_bytes(
        encoding=serialization.Encoding.Raw,
        format=serialization.PrivateFormat.Raw,
        encryption_algorithm=serialization.NoEncryption(),
    )
    ed_public, ed_secret = crypto_sign_seed_keypair(seed)
    curve_public = crypto_sign_ed25519_pk_to_curve25519(ed_public)
    curve_secret = crypto_sign_ed25519_sk_to_curve25519(ed_secret)
    try:
        ciphertext = base64.b64decode(encrypted_task_id, validate=True)
        plaintext = crypto_box_seal_open(ciphertext, curve_public, curve_secret)
        task_id = plaintext.decode("utf-8").strip()
    except Exception as exc:
        raise OAuth2AgentError("failed to decrypt encrypted task id") from exc
    if not task_id:
        raise OAuth2AgentError("decrypted task id is empty")
    return task_id


def register_agent_task(
    agent_runtime_id: str,
    private_key: ed25519.Ed25519PrivateKey,
    *,
    auth_api_base: str = DEFAULT_AUTH_API_BASE,
) -> str:
    timestamp = _timestamp()
    signature = _sign(private_key, f"{agent_runtime_id}:{timestamp}")
    url = auth_api_base.rstrip("/") + f"/v1/agent/{urllib.parse.quote(agent_runtime_id, safe='')}/task/register"
    result = _expect_json_success(
        "POST",
        url,
        json_body={"timestamp": timestamp, "signature": signature},
        timeout=35,
    )
    task_id = result.get("task_id") or result.get("taskId")
    if isinstance(task_id, str) and task_id.strip():
        return task_id.strip()
    encrypted = result.get("encrypted_task_id") or result.get("encryptedTaskId")
    if isinstance(encrypted, str) and encrypted.strip():
        return _decrypt_task_id(private_key, encrypted.strip())
    raise OAuth2AgentError("task registration response omitted task id")


def convert_oauth_to_agent_identity(
    oauth: OAuthMaterial,
    *,
    auth_api_base: str = DEFAULT_AUTH_API_BASE,
) -> AgentIdentity:
    key_material = generate_key_material()
    runtime_id = register_agent_identity(oauth, key_material, auth_api_base=auth_api_base)
    task_id = register_agent_task(runtime_id, key_material.private_key, auth_api_base=auth_api_base)
    return AgentIdentity(
        agent_runtime_id=runtime_id,
        agent_private_key=key_material.private_key_pkcs8_base64,
        task_id=task_id,
        account_id=oauth.account_id,
        chatgpt_user_id=oauth.chatgpt_user_id,
        email=oauth.email,
        plan_type=oauth.plan_type,
        chatgpt_account_is_fedramp=oauth.chatgpt_account_is_fedramp,
    )


def private_key_from_identity(identity: AgentIdentity) -> ed25519.Ed25519PrivateKey:
    try:
        der = base64.b64decode(identity.agent_private_key, validate=True)
        key = serialization.load_der_private_key(der, password=None)
    except Exception as exc:
        raise OAuth2AgentError("agent_private_key is not valid PKCS#8 base64") from exc
    if not isinstance(key, ed25519.Ed25519PrivateKey):
        raise OAuth2AgentError("agent_private_key is not Ed25519")
    return key


def build_agent_assertion(identity: AgentIdentity, *, timestamp: str | None = None) -> str:
    timestamp = timestamp or _timestamp()
    key = private_key_from_identity(identity)
    signature = _sign(
        key,
        f"{identity.agent_runtime_id}:{identity.task_id}:{timestamp}",
    )
    envelope = {
        "agent_runtime_id": identity.agent_runtime_id,
        "task_id": identity.task_id,
        "timestamp": timestamp,
        "signature": signature,
    }
    encoded = json.dumps(envelope, separators=(",", ":")).encode("utf-8")
    return "AgentAssertion " + _b64url_encode(encoded)


def identity_from_document(document: Any) -> AgentIdentity:
    if not isinstance(document, dict):
        raise OAuth2AgentError("Agent Identity JSON must be an object")
    raw = document.get("agent_identity")
    if not isinstance(raw, dict):
        raw = document.get("credentials") if isinstance(document.get("credentials"), dict) else None
    if not isinstance(raw, dict):
        raise OAuth2AgentError("Agent Identity JSON does not contain agent_identity")

    def required(key: str) -> str:
        value = raw.get(key)
        if not isinstance(value, str) or not value.strip():
            raise OAuth2AgentError(f"Agent Identity is missing {key}")
        return value.strip()

    return AgentIdentity(
        agent_runtime_id=required("agent_runtime_id"),
        agent_private_key=required("agent_private_key"),
        task_id=required("task_id"),
        account_id=required("account_id") if "account_id" in raw else required("chatgpt_account_id"),
        chatgpt_user_id=required("chatgpt_user_id"),
        email=raw.get("email") if isinstance(raw.get("email"), str) else None,
        plan_type=raw.get("plan_type") if isinstance(raw.get("plan_type"), str) else "unknown",
        chatgpt_account_is_fedramp=bool(raw.get("chatgpt_account_is_fedramp", False)),
    )


def load_identity_file(path: str | os.PathLike[str]) -> AgentIdentity:
    try:
        document = json.loads(Path(path).read_text(encoding="utf-8"))
    except OSError as exc:
        raise OAuth2AgentError(f"failed to read identity file: {exc}") from exc
    except json.JSONDecodeError as exc:
        raise OAuth2AgentError(f"identity file is not valid JSON: {exc}") from exc
    return identity_from_document(document)


def write_identity_file(
    path: str | os.PathLike[str],
    identity: AgentIdentity,
    *,
    output_format: str = "sub2api",
) -> Path:
    destination = Path(path).expanduser()
    destination.parent.mkdir(parents=True, exist_ok=True)
    if output_format == "sub2api":
        document = identity.to_sub2api_document()
    elif output_format == "codex":
        document = identity.to_codex_auth_json()
    else:
        raise OAuth2AgentError(f"unsupported output format: {output_format}")

    payload = (json.dumps(document, ensure_ascii=False, indent=2) + "\n").encode("utf-8")
    fd, temp_name = tempfile.mkstemp(prefix=destination.name + ".", dir=str(destination.parent))
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, "wb") as handle:
            handle.write(payload)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temp_name, destination)
        try:
            os.chmod(destination, 0o600)
        except OSError:
            pass
    except Exception:
        try:
            os.close(fd)
        except OSError:
            pass
        try:
            os.unlink(temp_name)
        except OSError:
            pass
        raise
    return destination


def verify_responses(
    identity: AgentIdentity,
    *,
    codex_base: str = DEFAULT_CODEX_BASE,
    model: str = "gpt-5.4",
    prompt: str = "只回复：OK",
) -> str:
    url = codex_base.rstrip("/") + "/responses"
    headers = {
        "Authorization": build_agent_assertion(identity),
        "ChatGPT-Account-ID": identity.account_id,
        "Content-Type": "application/json",
        "Accept": "text/event-stream",
        "originator": "codex_cli_rs",
    }
    body = {
        "model": model,
        "instructions": "You are a concise assistant.",
        "input": [
            {
                "type": "message",
                "role": "user",
                "content": [{"type": "input_text", "text": prompt}],
            }
        ],
        "tools": [],
        "tool_choice": "auto",
        "parallel_tool_calls": False,
        "reasoning": {"summary": "auto"},
        "store": False,
        "stream": True,
        "include": ["reasoning.encrypted_content"],
    }
    request = urllib.request.Request(
        url,
        data=json.dumps(body, separators=(",", ":")).encode("utf-8"),
        headers={"User-Agent": USER_AGENT, **headers},
        method="POST",
    )
    chunks: list[str] = []
    try:
        with urllib.request.urlopen(request, timeout=120) as response:
            for raw_line in response:
                line = raw_line.decode("utf-8", errors="replace").strip()
                if not line.startswith("data:"):
                    continue
                data = line[5:].strip()
                if not data or data == "[DONE]":
                    continue
                try:
                    event = json.loads(data)
                except json.JSONDecodeError:
                    continue
                if event.get("type") == "error":
                    raise OAuth2AgentError(f"Responses stream returned an error: {event}")
                if event.get("type") == "response.output_text.delta" and isinstance(event.get("delta"), str):
                    chunks.append(event["delta"])
    except urllib.error.HTTPError as exc:
        detail = exc.read().decode("utf-8", errors="replace")[:1000].strip()
        raise OAuth2AgentError(f"Responses returned HTTP {exc.code}: {detail}") from exc
    except OSError as exc:
        raise OAuth2AgentError(f"Responses request failed: {exc}") from exc
    return "".join(chunks)


def check_conversation_isolation(
    identity: AgentIdentity,
    *,
    codex_base: str = DEFAULT_CODEX_BASE,
) -> int:
    base = codex_base.rstrip("/")
    backend_base = base[: -len("/codex")] if base.endswith("/codex") else base
    url = backend_base + "/conversations?offset=0&limit=1"
    status, _headers, _body = _json_request(
        "GET",
        url,
        headers={
            "Authorization": build_agent_assertion(identity),
            "ChatGPT-Account-ID": identity.account_id,
            "originator": "codex_cli_rs",
        },
        timeout=20,
    )
    return status
