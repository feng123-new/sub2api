# Changlian maintenance workflow

This fork carries production behavior that is not present in a stock Sub2API
release. Never deploy a stock upstream image directly. Every upgrade must port
the custom delta and pass the gates below.

## Upgrade sequence

1. Fetch the canonical `Wei-Shaw/sub2api` remote and select a stable release
   tag. Create a new versioned worktree and `changlian/vX.Y.Z-custom-YYYYMMDD`
   branch from that tag. Do not reuse or rewrite a historical worktree.
2. Inventory the production custom commits and compare their paths with the
   upstream release diff. Apply the custom commits without committing first so
   the complete staged tree can be reviewed and tested.
3. Prove compatibility behavior against plain upstream where practical, then
   run the focused custom tests. For the VS Code Unify boundary, OpenAI OAuth
   requests must drop unsupported `service_tier: auto`, while API-key accounts
   retain standard OpenAI semantics.
4. Run the full backend test suite with the Go version pinned by the project.
   Run frontend type checking, lint, focused custom tests, and the production
   build. A release is blocked by any new failure.
5. Create the source archive from the Git index tree, not from the working
   directory. Record its SHA-256 in the image label `local.source.sha256`, and
   record the upstream tag commit in `org.opencontainers.image.revision`.
6. Before promotion, verify the current production image, health, restart
   count, Compose configuration, disk capacity, image/source checksums, and the
   rollback image. Create and validate a PostgreSQL custom-format dump.
7. Rehearse against an isolated Docker network with cloned PostgreSQL data, a
   disposable Redis instance, copied app data, and a loopback-only candidate
   port. PostgreSQL 18 and newer must mount the host rehearsal directory at
   `/var/lib/postgresql`, not `/var/lib/postgresql/data`.
8. Exercise health, public settings, login/assets, migrations, version labels,
   startup logs, and at least one authenticated streaming request against the
   isolated candidate. Keep credentials out of process arguments by using a
   mode-0600 temporary header file.
9. After rehearsal passes, update only the application image reference and run
   `docker compose up -d --no-deps sub2api`. Do not restart PostgreSQL, Redis,
   Caddy, or Cloudflare, and do not stop the production application before the
   isolated rehearsal.
10. Verify three health probes, public login, unauthorized model behavior, the
    authenticated compatibility request, version/source identity, zero
    restarts, unchanged dependency container start times, unchanged migration
    checksums, and release-blocking log patterns. Finish with a real desktop and
    mobile browser render check.
11. Commit the exact source tree used to build the image, push the versioned
    branch, and merge it into the fork default branch through a pull request.
    Preserve source archives, image archives, checksums, deployment evidence,
    and rollback inputs.

## Boundaries

- Do not modify ingress or proxy topology as part of a routine Sub2API upgrade.
- Do not point a candidate at the production database or production Redis.
- Do not overwrite a failed attempt's evidence; use a new backup directory.
- Do not expose API keys, database credentials, or environment snapshots in
  Git, command arguments, logs, or pull request text.
- Do not force-push the fork default branch or a published maintenance branch.
- Do not claim completion from tests alone; production HTTP and browser checks
  are required.
