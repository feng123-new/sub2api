-- Lower the dead-tuple and statistics staleness thresholds observed in P0.
-- Only relation options change; no heap rewrite, index rebuild, or row deletion.
-- If a busy table cannot grant its short DDL lock, fail and retry deployment.
SET LOCAL lock_timeout = '3s';

ALTER TABLE public.usage_logs SET (
    autovacuum_vacuum_scale_factor = 0.05,
    autovacuum_vacuum_threshold = 1000,
    autovacuum_analyze_scale_factor = 0.02,
    autovacuum_analyze_threshold = 1000
);

ALTER TABLE public.usage_billing_dedup SET (
    autovacuum_vacuum_scale_factor = 0.05,
    autovacuum_vacuum_threshold = 1000,
    autovacuum_analyze_scale_factor = 0.01,
    autovacuum_analyze_threshold = 1000
);
