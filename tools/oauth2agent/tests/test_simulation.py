from __future__ import annotations

import json
from pathlib import Path

from oauth2agent.mock import run_simulation


def test_end_to_end_simulation(tmp_path: Path):
    report = run_simulation(str(tmp_path))
    assert report.response_text == "OK"
    assert report.isolation_status == 403
    assert report.pushed is True
    document = json.loads(Path(report.output_file).read_text())
    serialized = json.dumps(document)
    assert document["auth_mode"] == "agentIdentity"
    assert document["agent_identity"]["agent_runtime_id"] == "agent-demo"
    assert "access_token" not in serialized
    assert "refresh_token" not in serialized
