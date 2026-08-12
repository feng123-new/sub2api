from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path

from .core import (
    DEFAULT_AUTH_API_BASE,
    DEFAULT_CODEX_BASE,
    OAuth2AgentError,
    check_conversation_isolation,
    convert_oauth_to_agent_identity,
    load_identity_file,
    load_oauth_file,
    verify_responses,
    write_identity_file,
)
from .mock import run_simulation
from .sub2api import pull_oauth_from_sub2api, push_identity_to_sub2api


def _default_output(input_path: str) -> str:
    path = Path(input_path)
    return str(path.with_name(path.stem + "-agent.json"))


def _add_sub2api_auth(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--admin-api-key", default=os.getenv("SUB2API_ADMIN_API_KEY"))
    parser.add_argument("--admin-jwt", default=os.getenv("SUB2API_JWT"))


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="oauth2agent",
        description="Convert ChatGPT/Codex OAuth credentials into an identity-only Codex Agent Identity file.",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    convert = sub.add_parser("convert", help="convert a local OAuth JSON file")
    convert.add_argument("input", help="OAuth/Codex/Sub2API JSON file")
    convert.add_argument("-o", "--output")
    convert.add_argument("--format", choices=["sub2api", "codex"], default="sub2api")
    convert.add_argument("--auth-api-base", default=DEFAULT_AUTH_API_BASE)

    pull = sub.add_parser("pull", help="pull one OAuth account from Sub2API and convert it")
    pull.add_argument("--base-url", default=os.getenv("SUB2API_BASE_URL"), required=os.getenv("SUB2API_BASE_URL") is None)
    pull.add_argument("--account-id", type=int, required=True)
    pull.add_argument("-o", "--output", required=True)
    pull.add_argument("--format", choices=["sub2api", "codex"], default="sub2api")
    pull.add_argument("--auth-api-base", default=DEFAULT_AUTH_API_BASE)
    pull.add_argument("--push", action="store_true", help="also import the identity as a new Sub2API account")
    pull.add_argument("--name")
    _add_sub2api_auth(pull)

    push = sub.add_parser("push", help="import an existing Agent Identity file into Sub2API")
    push.add_argument("input")
    push.add_argument("--base-url", default=os.getenv("SUB2API_BASE_URL"), required=os.getenv("SUB2API_BASE_URL") is None)
    push.add_argument("--name")
    push.add_argument("--update-existing", action="store_true")
    _add_sub2api_auth(push)

    verify = sub.add_parser("verify", help="verify Agent Identity against Codex Responses")
    verify.add_argument("input")
    verify.add_argument("--codex-base", default=DEFAULT_CODEX_BASE)
    verify.add_argument("--model", default="gpt-5.4")
    verify.add_argument("--prompt", default="只回复：OK")
    verify.add_argument("--check-isolation", action="store_true")

    simulate = sub.add_parser("simulate", help="run an end-to-end local mock without real credentials")
    simulate.add_argument("--output-dir")
    return parser


def _print_identity_summary(identity, output: str | None = None) -> None:
    print(f"Agent runtime: {identity.agent_runtime_id}")
    print(f"Task ID:       {identity.task_id}")
    print(f"Account ID:    {identity.account_id}")
    print(f"User ID:       {identity.chatgpt_user_id}")
    print(f"Plan:          {identity.plan_type}")
    if identity.email:
        print(f"Email:         {identity.email}")
    if output:
        print(f"Output:        {output}")
    print("OAuth tokens:  not written")


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        if args.command == "convert":
            oauth = load_oauth_file(args.input)
            identity = convert_oauth_to_agent_identity(oauth, auth_api_base=args.auth_api_base)
            output = args.output or _default_output(args.input)
            write_identity_file(output, identity, output_format=args.format)
            _print_identity_summary(identity, output)
            return 0

        if args.command == "pull":
            oauth = pull_oauth_from_sub2api(
                base_url=args.base_url,
                account_id=args.account_id,
                admin_api_key=args.admin_api_key,
                admin_jwt=args.admin_jwt,
            )
            identity = convert_oauth_to_agent_identity(oauth, auth_api_base=args.auth_api_base)
            write_identity_file(args.output, identity, output_format=args.format)
            _print_identity_summary(identity, args.output)
            if args.push:
                result = push_identity_to_sub2api(
                    identity,
                    base_url=args.base_url,
                    admin_api_key=args.admin_api_key,
                    admin_jwt=args.admin_jwt,
                    name=args.name,
                    update_existing=False,
                )
                print("Sub2API import:", json.dumps(result, ensure_ascii=False))
            return 0

        if args.command == "push":
            identity = load_identity_file(args.input)
            result = push_identity_to_sub2api(
                identity,
                base_url=args.base_url,
                admin_api_key=args.admin_api_key,
                admin_jwt=args.admin_jwt,
                name=args.name,
                update_existing=args.update_existing,
            )
            print(json.dumps(result, ensure_ascii=False, indent=2))
            return 0

        if args.command == "verify":
            identity = load_identity_file(args.input)
            text = verify_responses(
                identity,
                codex_base=args.codex_base,
                model=args.model,
                prompt=args.prompt,
            )
            print("Responses:", text)
            if args.check_isolation:
                status = check_conversation_isolation(identity, codex_base=args.codex_base)
                print(f"Conversations endpoint: HTTP {status}")
                if status not in {401, 403}:
                    raise OAuth2AgentError("conversation isolation check failed")
            return 0

        if args.command == "simulate":
            print("[1/6] Starting local mock Sub2API + OpenAI endpoints")
            report = run_simulation(args.output_dir)
            print(f"[2/6] OAuth pulled: {report.email or 'unknown'} ({report.plan_type})")
            print(f"[3/6] Agent registered: {report.runtime_id}")
            print(f"[4/6] Task registered:  {report.task_id}")
            print(f"[5/6] Identity-only file written and pushed: {report.output_file}")
            print(f"[6/6] Responses={report.response_text!r}; conversations=HTTP {report.isolation_status}")
            print("SIMULATION PASSED")
            return 0

        raise AssertionError("unreachable")
    except OAuth2AgentError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
