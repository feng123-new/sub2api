from __future__ import annotations

import base64
import json
import tempfile
import threading
from dataclasses import dataclass
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any
from urllib.parse import urlparse

from cryptography.hazmat.primitives.asymmetric import ed25519

from .core import (
    OAuth2AgentError,
    _b64url_decode,
    check_conversation_isolation,
    convert_oauth_to_agent_identity,
    verify_responses,
    write_identity_file,
)
from .sub2api import pull_oauth_from_sub2api, push_identity_to_sub2api


def _fake_jwt(payload: dict[str, Any]) -> str:
    def enc(value: Any) -> str:
        raw = json.dumps(value, separators=(",", ":")).encode("utf-8")
        return base64.urlsafe_b64encode(raw).rstrip(b"=").decode("ascii")

    return f"{enc({'alg': 'none', 'typ': 'JWT'})}.{enc(payload)}.signature"


def _ssh_ed25519_raw(public_key: str) -> bytes:
    prefix, encoded = public_key.split(" ", 1)
    if prefix != "ssh-ed25519":
        raise ValueError("unexpected SSH key type")
    blob = base64.b64decode(encoded)
    offset = 0

    def read_string() -> bytes:
        nonlocal offset
        length = int.from_bytes(blob[offset : offset + 4], "big")
        offset += 4
        value = blob[offset : offset + length]
        offset += length
        return value

    if read_string() != b"ssh-ed25519":
        raise ValueError("invalid SSH key blob")
    return read_string()


@dataclass
class MockState:
    access_token: str
    account_id: str = "account-demo"
    user_id: str = "user-demo"
    runtime_id: str = "agent-demo"
    task_id: str = "task-demo"
    public_key: ed25519.Ed25519PublicKey | None = None
    import_received: bool = False


class MockHandler(BaseHTTPRequestHandler):
    server_version = "OAuth2AgentMock/0.1"

    @property
    def state(self) -> MockState:
        return self.server.state  # type: ignore[attr-defined]

    def log_message(self, format: str, *args: Any) -> None:  # noqa: A003
        return

    def _read_json(self) -> dict[str, Any]:
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length) if length else b"{}"
        value = json.loads(body or b"{}")
        if not isinstance(value, dict):
            raise ValueError("JSON body must be object")
        return value

    def _send_json(self, status: int, value: Any) -> None:
        body = json.dumps(value, separators=(",", ":")).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self) -> None:  # noqa: N802
        parsed = urlparse(self.path)
        if parsed.path == "/api/v1/admin/accounts/data":
            claims = {
                "email": "demo@example.com",
                "https://api.openai.com/auth": {
                    "chatgpt_account_id": self.state.account_id,
                    "chatgpt_user_id": self.state.user_id,
                    "chatgpt_plan_type": "pro",
                    "chatgpt_account_is_fedramp": False,
                },
            }
            id_token = _fake_jwt(claims)
            document = {
                "accounts": [
                    {
                        "id": 7,
                        "platform": "openai",
                        "type": "oauth",
                        "credentials": {
                            "access_token": self.state.access_token,
                            "refresh_token": "mock-refresh-token",
                            "id_token": id_token,
                            "chatgpt_account_id": self.state.account_id,
                            "chatgpt_user_id": self.state.user_id,
                        },
                    }
                ]
            }
            self._send_json(200, document)
            return
        if parsed.path == "/backend-api/conversations":
            self._send_json(403, {"error": "forbidden for agent identity"})
            return
        self._send_json(404, {"error": "not found"})

    def do_POST(self) -> None:  # noqa: N802
        parsed = urlparse(self.path)
        if parsed.path == "/api/accounts/v1/agent/register":
            if self.headers.get("Authorization") != f"Bearer {self.state.access_token}":
                self._send_json(401, {"error": "bad bearer"})
                return
            body = self._read_json()
            if body.get("capabilities") != ["responsesapi"]:
                self._send_json(400, {"error": "missing responsesapi capability"})
                return
            try:
                raw_public = _ssh_ed25519_raw(str(body["agent_public_key"]))
                self.state.public_key = ed25519.Ed25519PublicKey.from_public_bytes(raw_public)
            except Exception:
                self._send_json(400, {"error": "bad public key"})
                return
            self._send_json(200, {"agent_runtime_id": self.state.runtime_id})
            return

        if parsed.path == f"/api/accounts/v1/agent/{self.state.runtime_id}/task/register":
            body = self._read_json()
            if not self.state.public_key:
                self._send_json(409, {"error": "agent not registered"})
                return
            try:
                payload = f"{self.state.runtime_id}:{body['timestamp']}".encode("utf-8")
                signature = base64.b64decode(body["signature"], validate=True)
                self.state.public_key.verify(signature, payload)
            except Exception:
                self._send_json(401, {"error": "invalid task signature"})
                return
            self._send_json(200, {"task_id": self.state.task_id})
            return

        if parsed.path == "/api/v1/admin/accounts/import/codex-session":
            body = self._read_json()
            try:
                document = json.loads(body["content"])
                serialized = json.dumps(document)
                if "access_token" in serialized or "refresh_token" in serialized:
                    raise ValueError("OAuth token leaked into Agent Identity import")
                identity = document["agent_identity"]
                if identity["agent_runtime_id"] != self.state.runtime_id:
                    raise ValueError("wrong runtime")
            except Exception as exc:
                self._send_json(400, {"error": str(exc)})
                return
            self.state.import_received = True
            self._send_json(200, {"created": 1, "updated": 0, "failed": 0})
            return

        if parsed.path == "/backend-api/codex/responses":
            authorization = self.headers.get("Authorization", "")
            if not authorization.startswith("AgentAssertion ") or not self.state.public_key:
                self._send_json(401, {"error": "missing assertion"})
                return
            try:
                envelope = json.loads(_b64url_decode(authorization.split(" ", 1)[1]))
                if envelope["agent_runtime_id"] != self.state.runtime_id:
                    raise ValueError("runtime mismatch")
                if envelope["task_id"] != self.state.task_id:
                    raise ValueError("task mismatch")
                payload = f"{envelope['agent_runtime_id']}:{envelope['task_id']}:{envelope['timestamp']}".encode("utf-8")
                self.state.public_key.verify(base64.b64decode(envelope["signature"]), payload)
            except Exception as exc:
                self._send_json(401, {"error": f"invalid assertion: {exc}"})
                return

            events = [
                {"type": "response.output_text.delta", "delta": "OK"},
                {"type": "response.completed"},
            ]
            body = "".join(f"data: {json.dumps(event, separators=(',', ':'))}\n\n" for event in events)
            body += "data: [DONE]\n\n"
            encoded = body.encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Content-Length", str(len(encoded)))
            self.end_headers()
            self.wfile.write(encoded)
            return

        self._send_json(404, {"error": "not found"})


class MockServer(ThreadingHTTPServer):
    def __init__(self, state: MockState):
        super().__init__(("127.0.0.1", 0), MockHandler)
        self.state = state


@dataclass(frozen=True)
class SimulationReport:
    output_file: str
    email: str | None
    plan_type: str
    runtime_id: str
    task_id: str
    pushed: bool
    response_text: str
    isolation_status: int


def run_simulation(output_dir: str | None = None) -> SimulationReport:
    access_claims = {
        "email": "demo@example.com",
        "https://api.openai.com/auth": {
            "chatgpt_account_id": "account-demo",
            "chatgpt_user_id": "user-demo",
            "chatgpt_plan_type": "pro",
        },
    }
    access_token = _fake_jwt(access_claims)
    state = MockState(access_token=access_token)
    server = MockServer(state)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    host, port = server.server_address
    root = f"http://{host}:{port}"
    try:
        oauth = pull_oauth_from_sub2api(
            base_url=root,
            account_id=7,
            admin_api_key="mock-admin-key",
        )
        identity = convert_oauth_to_agent_identity(
            oauth,
            auth_api_base=root + "/api/accounts",
        )
        destination_dir = Path(output_dir) if output_dir else Path(tempfile.mkdtemp(prefix="oauth2agent-sim-"))
        destination = destination_dir / "demo-agent.json"
        write_identity_file(destination, identity, output_format="sub2api")
        pushed = push_identity_to_sub2api(
            identity,
            base_url=root,
            admin_api_key="mock-admin-key",
            name="demo-agent",
            update_existing=False,
        )
        if not isinstance(pushed, dict) or pushed.get("created") != 1:
            raise OAuth2AgentError("mock Sub2API did not accept Agent Identity import")
        response_text = verify_responses(identity, codex_base=root + "/backend-api/codex")
        isolation_status = check_conversation_isolation(identity, codex_base=root + "/backend-api/codex")
        if response_text != "OK":
            raise OAuth2AgentError(f"mock Responses verification returned {response_text!r}")
        if isolation_status not in {401, 403}:
            raise OAuth2AgentError(f"mock isolation check returned HTTP {isolation_status}")
        if not state.import_received:
            raise OAuth2AgentError("mock Sub2API import endpoint was not called")
        return SimulationReport(
            output_file=str(destination),
            email=oauth.email,
            plan_type=oauth.plan_type,
            runtime_id=identity.agent_runtime_id,
            task_id=identity.task_id,
            pushed=True,
            response_text=response_text,
            isolation_status=isolation_status,
        )
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=2)
