# Sub2API VPS operations entrypoints

Runtime facts come from `/opt/sub2api-deploy/runtime/manifest.json`, not image tags
or worktree directory names. The maintained upgrade runbook is
`/home/fengyong/projects/ops/sub2api/SUB2API_UPGRADE_RUNBOOK.md` on the control host.
Its identical VPS copy is `/opt/sub2api-deploy/SUB2API_UPGRADE_RUNBOOK.md`;
`/opt/sub2api-deploy/OPERATIONS.md` points to that copy.

The scripts in this source directory are installed together into
`/opt/sub2api-deploy/ops`. Keep `lib/backup_state.py` with `backup-sub2api.sh`.
Release directories contain context, artifacts and evidence, not the authoritative
copy of reusable helpers. No application dependencies or builds run on WSL.

## Build runner

```bash
python3 /opt/sub2api-deploy/ops/guarded-docker.py --help
python3 /opt/sub2api-deploy/ops/guarded-docker.py --evidence-dir "$OUT" \
  --cpus 1.5 --memory-mib 2800 -- \
  -e NODE_OPTIONS=--max-old-space-size=2300 \
  -v "$SRC:/src" -w /src/frontend node:24-alpine \
  sh -ec 'corepack enable; corepack prepare pnpm@9.15.9 --activate; pnpm run build'
```

The evidence directory must already exist. A host lock serializes guarded builds.
Start requires at least 3500 MiB MemAvailable. The runner stops its own container
below 1024 MiB or when memory full PSI avg10 exceeds 10. The current VPS profile
allows at most 2800 MiB and 2 CPUs, with no additional swap allowance. Resource,
identity and lifecycle flags belong to the runner and cannot be overridden in
the Docker arguments. Startup/attachment failures and incomplete container states
are failures. Results go to `$OUT/resource-guards.jsonl`.

The frontend standard build runs i18n checks, one application typecheck, one Vite
configuration typecheck, then bundling. Dev/build/preview select `vite.config.ts`
explicitly; the development checker is not loaded during production bundling.

## Release probe

```bash
python3 /opt/sub2api-deploy/ops/probe.py --help
python3 /opt/sub2api-deploy/ops/probe.py --admin --out "$OUT/probe.json"
python3 /opt/sub2api-deploy/ops/probe.py \
  --base-url https://sub2api.136.117.94.64.nip.io --out "$OUT/public-probe.json"
```

The probe compares the manifest, host/container binary checksums and public
version. `--admin` also checks the existing ticket pool and an available archived
image; short-lived administrator credentials stay in memory. This probe does not
make a paid model request. Use the runbook's explicit streaming smoke request
when publishing an application release. `sql()` and `request()` are also the
shared helpers for the current release's detailed acceptance script.

## Scheduled backup

```bash
/opt/sub2api-deploy/ops/backup-sub2api.sh --help
/opt/sub2api-deploy/ops/backup-sub2api.sh
python3 /opt/sub2api-deploy/ops/verify-backup.py \
  /home/fengyong/backups/sub2api-scheduled/latest.tar.gz
```

Cron keeps its existing nightly entry. The script holds its own lock and a shared
deployment lock, so backups cannot overlap each other or a release switch.
A PostgreSQL repeatable-read exported snapshot supplies both the image index and
`pg_dump --snapshot`. Indexed immutable images are copied and checked for size and
SHA-256 before that snapshot is closed. A missing or corrupt image fails the run;
no partial backup replaces `latest.tar.gz`. Application processes remain running.

Each format-2 archive contains:

- `postgres.dump` and its readable TOC; the image index is in `backup.json`.
- `.env`, `docker-compose.yml`, `config.yaml` when present, and existing settings exports.
- `runtime/manifest.json`, the matching `runtime/sub2api`, and `runtime/source.tar.gz`.
- `data/generated-images/`, with every file referenced by the captured image index.
- The installed public operations scripts and their backup library.
- Docker metadata and supplemental Redis persistence when available.

The streaming verifier checks every covered file plus release and image-index
checksums before the archive is published atomically. A sidecar SHA-256 is written;
successful archives and sidecars retain the existing seven-day policy. Credentials
remain inside the private backup directory/archive and must not enter Git or logs.

Recovery mapping: restore image files beneath the deployment root, `config.yaml`
to `data/config.yaml`, and the exact runtime binary with its manifest. The source
archive is copied into the backup, so recovery does not depend on an old release
directory still existing. Adjust restored manifest paths to their new locations.
Database restoration remains an explicitly authorized recovery operation.
A compatible base image and PostgreSQL/Redis installation are prerequisites;
Docker images and the whole host are not included. Redis is supplemental and is
not transactionally synchronized with PostgreSQL. These backups live on the VPS;
they complement, rather than replace, separately managed off-host snapshots.

Historical release scripts/evidence remain for diagnosis and rollback. Do not
execute old deployment scripts as a general-purpose upgrade command, and do not
delete the shared `source/.git` object store.
