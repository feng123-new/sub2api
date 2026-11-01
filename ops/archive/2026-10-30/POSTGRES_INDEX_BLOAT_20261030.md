# PostgreSQL 索引占用与膨胀调查（2026-10-30）

## 范围与方法

- 实例：GCP VPS 上的 `sub2api-postgres`，数据库 `sub2api`；调查时间为 2026-10-30 00:47–01:00 左右（Asia/Shanghai）。
- 仅执行统计查询及事务内的诊断扩展调用：`pgstattuple` 的 `pgstatindex` 和 `pageinspect` 的 `gin_metapage_info`；事务最后 `ROLLBACK`，未持久安装扩展。
- 没有执行 `VACUUM`、`ANALYZE`、`REINDEX`、`DROP INDEX` 或更改线上保留策略。本报告中的优化命令均为建议。
- `idx_scan` 是 PostgreSQL 累积统计量，受索引重建、重启和统计重置等影响。显示 0 不能单独作为删索引的依据。

## 体量与使用率

数据库总占用 `6493 MB`（约 6.34 GiB）。以下容量来自 `pg_total_relation_size` / `pg_indexes_size`，单位 `MB` 为 PostgreSQL 的二进制 MiB。

| 表 | 表及索引总占用 | 索引总占用 | 索引数 | `idx_scan=0` 索引数 / 占用 |
| --- | ---: | ---: | ---: | ---: |
| `usage_logs` | 4312 MB | 2830 MB | 26 | 9 / 586 MB |
| `usage_billing_dedup` | 1164 MB | 463 MB | 3 | 1 / 84 MB |
| `ops_error_logs` | 409 MB | 252 MB | 17 | 9 / 175 MB |
| `ops_system_logs` | 190 MB | 61 MB | 12 | 5 / 51 MB |
| `accounts` | 103 MB | 101 MB | 21 | 10 / 约 101 MB |

有代表性的索引如下。B-tree 叶页填充率及已删除页由 `pgstatindex` 实测，不能直接把 `100% − 叶页填充率` 等同于可回收磁盘百分比。

| 索引 | 体积 | `idx_scan` | B-tree 平均叶页填充率 | 已删除页 |
| --- | ---: | ---: | ---: | ---: |
| `idx_usage_logs_request_id_api_key_unique` | 338 MB | 300,860 | **50.65%** | 37 |
| `idx_usage_logs_effective_upstream_model_created` | 205 MB | **0** | **64.45%** | **4,685**（至少约 36.6 MiB 页空间） |
| `idx_usage_billing_dedup_request_api_key` | 379 MB | 300,858 | 69.26% | 0 |
| `idx_usage_logs_created_requested_model_upstream_model` | 173 MB | **2,599,801** | 未采样 | 未采样 |
| `idx_usage_logs_user_created` | 108 MB | **900,625** | 未采样 | 未采样 |
| `idx_accounts_extra_gin` | **101 MB** | **0** | GIN，不适用 | 参见下文 |

`usage_logs` 的部分复合索引几乎无扫描，另一些却非常频繁。例如 `idx_usage_logs_model_created_at`（234 MB）仅 5 次、`idx_usage_logs_service_tier_created_at`（122 MB）为 0 次，而 `idx_usage_logs_created_at`（78 MB）约 652,456 次。需要核对实际 SQL 查询计划及索引约束后再决定是否移除冗余索引。

## 增长与膨胀原因

**1. `usage_logs`：数据写入 + 90 天滚动删除 + 26 份索引维护。**

- 近期每天新增量约 2,600–12,700 行；样本时约 211.6 万行。累计 `n_tup_del=1,186,958`，估算 `n_dead_tup=437,403`，估算活元组约 211 万。
- 最近一次自动 VACUUM 是 **2026-10-21 19:39 +08**。数据库默认 `autovacuum_vacuum_scale_factor=0.2`、`threshold=50`，据当前估算活行数得到触发阈值约 42.2 万死元组；目前数值已超过阈值，但统计估计和自动 VACUUM 的调度需要进一步监控。
- 2026-10-30 样本中，超过 90 天的数据仅约 2,166 行，说明历史清理大体在推进。代码 `dashboard_aggregation_service.go` 每隔约 6 小时执行 retention，`dashboard_aggregation_repo.go` 以每批 10,000 行执行 `DELETE`。当前 `usage_logs` 是普通表（`relkind=r`），不是分区表。DELETE 及 B-tree 随机键写入是页面碎片/低填充率的合理原因。
- 对多个未充分使用的组合索引，任何写入和删除都仍需维护索引结构，造成写放大。50.65% 的 B-tree 叶页填充率和 4,685 个已删除页是已测量的膨胀信号。

**2. `accounts.extra`：异常 GIN 增长，优先处理。**

- 仅 **175** 个账号，`extra` 字段平均约 **913 B**、最大 **3,717 B**，但 `idx_accounts_extra_gin` 已达到 **101 MB**，`idx_scan=0`。
- GIN 元页报告 `n_total_pages=12,930`、`n_entry_pages=12,879`、`n_entries=1,489,787`。同时，`accounts` 统计 `n_tup_upd=318,247`、`n_tup_hot_upd=239`，约 0.075% 为 HOT 更新；自动 VACUUM 执行频繁。
- 代码中确实有 `extra @>` JSONB 包含查询（例如 `account_repo.go` 中的计费探测筛选），但线上两条代表性语句的 `EXPLAIN (ANALYZE, BUFFERS)` 均选择 **Seq Scan**，分别扫描 175 行，执行耗时约 3.35 ms 和 0.69 ms，未使用该 GIN。这是其累计 `idx_scan=0` 的具体解释线索，而非所有场景都用不到它的证明。
- 大量非 HOT 更新与 JSONB GIN 索引键积累高度吻合，符合高频更新导致显著膨胀的判断。但现有数据不能逐项证明所有索引键已失效。GIN 的 `pgstattuple` 诊断不支持该索引类型；应以重建前后体积及查询延迟验证可回收比例。

**3. `usage_billing_dedup`：长保留期带来的正常体量占主导，统计信息严重滞后。**

- 实际 `COUNT(*)=3,926,078`，`pg_stat_user_tables.n_live_tup` 却只有约 300,858；未见历史 `autovacuum` / `autoanalyze`。建议先执行一次受控 `ANALYZE`，恢复估算与查询计划质量。
- 代码保留期为 365 天（不少于 usage_logs 的保留天数），样本中超过 365 天的行数为 0。唯一请求键 B-tree 379 MB、叶页填充率 69.26%、已删除页为 0，当前证据更支持大量保留记录和随机索引键的正常空间消耗，不能把全部 379 MB 认定为膨胀。

**4. 运维日志：旧膨胀与较多索引仍需警惕。**

- `ops_system_logs` 在 2026-10-28 对 11 个 B-tree 索引执行过 CONCURRENTLY 重建，索引曾从约 4.53 GiB 降到约 58.4 MiB；现在总索引约 61 MB，未见同等级别复发。
- `ops_error_logs` 252 MB 索引中，9 个零扫描索引合计约 175 MB，包含多个 trigram 搜索索引。需要按管理后台实际搜索行为确认用途；不宜仅据累计零扫描删除。

## 优先处理建议（均未执行）

1. **P0-A：核查 `accounts.extra` GIN 的真实查询依赖**：已确认代码存在 `extra @>` 查询，但当前小表的代表性执行计划是顺序扫描。若需保留该索引，在低峰执行 `REINDEX INDEX CONCURRENTLY public.idx_accounts_extra_gin;`，记录体积前后差和查询性能。若进一步确认业务周期内均无需索引，再评估移除；两种做法都需先核对约束和回滚方案。
2. **P0-B：降低 `usage_logs` 的 VACUUM 滞后**：监控 `pg_stat_progress_vacuum`、`n_dead_tup` 与实际后台清理时长；在维护窗口考虑 `VACUUM (ANALYZE) public.usage_logs;`。根据写入/删除压力逐步降低表级 vacuum scale factor，例如从 0.2 调整到 0.05，并复测后台负载。针对低填充率 B-tree，再考虑逐个 `REINDEX INDEX CONCURRENTLY`。
3. **P0-C：`ANALYZE public.usage_billing_dedup;`**，修复明显过时的行数估算；以后定期验证 autoanalyze 是否触发及查询计划质量。
4. **P0-D：审核重叠/低使用率的 `usage_logs`、`ops_error_logs` 索引**：至少采集跨多个业务周期的 `idx_scan` 差量，并与 SQL 和 `EXPLAIN (ANALYZE, BUFFERS)` 配对后调整。避免一次性删除多个索引。
5. **P0-E：定期记录索引体积和统计快照**，同时记录是否发生 `REINDEX`、重启、VACUUM 及保留期变更。VPS 根盘调查时约有 34 GB 空闲，但并发索引重建仍会额外占用磁盘与 I/O。

可重复执行的只读统计 SQL：

```sql
SELECT t.relname AS table_name, i.relname AS index_name,
       pg_size_pretty(pg_relation_size(i.oid)) AS index_size,
       s.idx_scan, s.idx_tup_read, s.idx_tup_fetch
FROM pg_class t
JOIN pg_index x ON x.indrelid=t.oid
JOIN pg_class i ON i.oid=x.indexrelid
JOIN pg_stat_user_indexes s ON s.indexrelid=i.oid
WHERE t.relname IN ('usage_logs', 'usage_billing_dedup',
                    'accounts', 'ops_error_logs', 'ops_system_logs')
ORDER BY pg_relation_size(i.oid) DESC;

SELECT relname, n_live_tup, n_dead_tup, n_tup_upd, n_tup_hot_upd,
       n_tup_del, last_autovacuum, last_autoanalyze
FROM pg_stat_user_tables
WHERE relname IN ('usage_logs', 'usage_billing_dedup', 'accounts');
```

**解释边界：** `pgstatindex` 的叶页填充率和已删除页是采样时间点的实际结构证据，但并不直接给出重建后最终大小；`idx_scan` 也不是业务代码调用次数。诊断中的临时扩展创建事务已回滚，没有永久改变数据库结构。
