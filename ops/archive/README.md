# Historical operations and inherited development notes

Start current VPS work from the [operations entrypoints](../README.md) and
[deployed source provenance](../SOURCE_PROVENANCE.md). The maintained upgrade
runbook lives at `/opt/sub2api-deploy/SUB2API_UPGRADE_RUNBOOK.md` on the VPS and
`/home/fengyong/projects/ops/sub2api/SUB2API_UPGRADE_RUNBOOK.md` on the control host.

The records below preserve their original environment, measurements, and dates.
Their commands and statements about pending work describe that historical scope;
check the current runbook before applying them to production.

| Record | Scope and current interpretation |
| --- | --- |
| [PostgreSQL index investigation, 2026-10-30](2026-10-30/POSTGRES_INDEX_BLOAT_20261030.md) | Pre-maintenance measurements. Its "not executed" recommendations are historical; the accounts GIN and usage request unique-index rebuilds were subsequently completed. |
| [PostgreSQL maintenance follow-up, 2026-10-30](2026-10-30/POSTGRES_INDEX_MAINTENANCE_20261030.md) | Companion release/migration rationale and one-time maintenance decisions. Follow current observations before repeating maintenance. |
| [Inherited development guide](inherited-development/DEV_GUIDE.md) | Original `bayma888/sub2api-bmai` Windows/PostgreSQL 16 environment. Kept unchanged as inherited development reference, not the VPS deployment procedure. |
| [Inherited Changlian maintenance guide](../../docs/CHANGLIAN_MAINTENANCE.md) | Preserved in its existing location for historical context. The root README now points to this fork's current operations entrypoints. |

The three relocated records retain their original bytes. The investigation and
follow-up are siblings so the historical filename reference remains meaningful.
This archive is repository documentation; it is not a new production maintenance
entrypoint or a required payload of the scheduled recovery backup.

The misplaced `deploy/Makefile` was removed during the 2026-11-02 cleanup because
it required backend-only paths (`cmd/server`, `scripts/resolve-version.sh`, and a
Go module) that do not exist under `deploy/`. Root `make build-backend` continues
to delegate to `backend/Makefile`. Docker deployment files and build-image helpers
remain in `deploy/`.
