# PostgreSQL index growth: guarded operations (2026-10-30)

This is the operational follow-up to `POSTGRES_INDEX_BLOAT_20261030.md`. The report records ~6.34 GiB of database data and indexes.

## Changes shipped in the application

- `249z_autovacuum_hot_tables.sql` lowers the VACUUM and ANALYZE scale factors for `usage_logs` and `usage_billing_dedup` in a forward-only migration, with a 3-second DDL lock timeout. It performs no destructive maintenance. Existing migration checksums remain unchanged. The `249z` suffix accommodates the upstream `249_mcp_oauth.sql` migration on future merges.
- At ~2.1 million `usage_logs` rows, the default VACUUM scale threshold of 20% (~420k dead rows) becomes 5% (~106k). The ANALYZE threshold becomes ~43k changed rows. Thresholds are approximate and autovacuum scheduling also depends on system load.
- At ~3.93 million dedup rows, an ANALYZE change threshold of 1% (~40k) limits stale table statistics. The 365-day dedup history and uniqueness index remain intact.

## Post-upgrade read-only checks

Run as an authorized PostgreSQL operator:

```sql
SELECT c.relname, c.reloptions, s.n_live_tup, s.n_dead_tup,
       s.last_autovacuum, s.last_autoanalyze
FROM pg_class c
JOIN pg_stat_user_tables s ON s.relid = c.oid
WHERE c.oid IN ('public.usage_logs'::regclass,
                'public.usage_billing_dedup'::regclass,
                'public.accounts'::regclass)
ORDER BY c.relname;

SELECT t.relname AS table_name, i.relname AS index_name,
       pg_size_pretty(pg_relation_size(i.oid)) AS index_size,
       s.idx_scan, s.idx_tup_read, s.idx_tup_fetch
FROM pg_index x
JOIN pg_class t ON t.oid=x.indrelid
JOIN pg_class i ON i.oid=x.indexrelid
JOIN pg_stat_user_indexes s ON s.indexrelid=i.oid
WHERE t.relname IN ('usage_logs','usage_billing_dedup','accounts')
ORDER BY pg_relation_size(i.oid) DESC;
```

Capture the timestamp of each sample and compare index sizes, `idx_scan` deltas, and query plans over at least one billing cycle. Cumulative `idx_scan=0` alone is insufficient evidence for an index DROP.

## Single-run, guarded maintenance decisions

1. First consider `ANALYZE public.usage_billing_dedup;` separately from migrations. This updates planner statistics without rebuilding indexes. Confirm active load and run in a quiet period.
2. For the ~101 MiB `idx_accounts_extra_gin` (175 rows), inspect the production `extra @>` queries and query plans again. If the broad JSONB index is still needed, `REINDEX INDEX CONCURRENTLY public.idx_accounts_extra_gin;` can reclaim historical index bloat without a long exclusive read/write lock. Check PostgreSQL free disk, active index maintenance, and alerting first; monitor I/O and size before/after. It can still consume I/O/disk and briefly wait for conflicting transactions. Do not run inside a transaction.
3. For `usage_logs`, record scans of 26 existing indexes and compare query plans before touching an index. If a specific 338 MiB B-tree still has poor leaf fill, schedule its concurrent rebuild individually with sufficient free disk and a rollback plan. Unique indexes and their constraints must be preserved.
4. If bloat reappears quickly, investigate `accounts.extra` update frequency, volatile ticket metadata, low HOT update ratio, and `usage_logs` retention DELETE batches. Table-level autovacuum cannot shrink large existing indexes; it only improves dead-tuple cleanup and planner freshness.

No mass DROP, table rewrite, or automated periodic REINDEX is part of this release. Schema migrations are forward-only; rolling back the binary leaves these compatible autovacuum settings in effect. To revert settings manually if necessary, use `ALTER TABLE ... RESET (autovacuum_vacuum_scale_factor, autovacuum_vacuum_threshold, autovacuum_analyze_scale_factor, autovacuum_analyze_threshold)` for each affected table.
