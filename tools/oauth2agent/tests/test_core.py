from __future__ import annotations

import base64
import json

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric import ed25519

from oauth2agent.core import (
    AgentIdentity,
    _b64url_decode,
    build_agent_assertion,
    generate_key_material,
    parse_oauth_document,
)


def fake_jwt(payload: dict) -> str:
    def enc(value):
        raw = json.dumps(value, separators=(",", ":")).encode()
        return base64.urlsafe_b64encode(raw).rstrip(b"=").decode()

    return f"{enc({'alg': 'none'})}.{enc(payload)}.sig"


def test_parse_nested_sub2api_export():
    token = fake_jwt(
        {
            "email": "u@example.com",
            "https://api.openai.com/auth": {
                "chatgpt_account_id": "account-1",
                "chatgpt_user_id": "user-1",
                "chatgpt_plan_type": "pro",
            },
        }
    )
    oauth = parse_oauth_document(
        {
            "accounts": [
                {
                    "credentials": {
                        "access_token": token,
                        "refresh_token": "should-not-be-output",
                    }
                }
            ]
        }
    )
    assert oauth.account_id == "account-1"
    assert oauth.chatgpt_user_id == "user-1"
    assert oauth.email == "u@example.com"
    assert oauth.plan_type == "pro"


def test_generated_key_is_pkcs8_ed25519():
    material = generate_key_material()
    der = base64.b64decode(material.private_key_pkcs8_base64)
    key = serialization.load_der_private_key(der, password=None)
    assert isinstance(key, ed25519.Ed25519PrivateKey)
    assert material.public_key_ssh.startswith("ssh-ed25519 ")


def test_agent_assertion_signature_verifies():
    material = generate_key_material()
    identity = AgentIdentity(
        agent_runtime_id="agent-1",
        agent_private_key=material.private_key_pkcs8_base64,
        task_id="task-1",
        account_id="account-1",
        chatgpt_user_id="user-1",
        email=None,
        plan_type="pro",
        chatgpt_account_is_fedramp=False,
    )
    header = build_agent_assertion(identity, timestamp="2026-08-12T12:00:00Z")
    envelope = json.loads(_b64url_decode(header.split(" ", 1)[1]))
    signature = base64.b64decode(envelope["signature"])
    material.private_key.public_key().verify(
        signature,
        b"agent-1:task-1:2026-08-12T12:00:00Z",
    )
