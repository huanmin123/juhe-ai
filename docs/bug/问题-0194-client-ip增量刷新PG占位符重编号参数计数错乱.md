# 问题-0194：client-ip 窗口增量刷新 PG 占位符重编号致参数计数错乱

- 发现：2026-09-26 22:12（国内单机生产 jobs 重启后例行日志复查）。任务 `client-ip-stats-aggregation` 每轮失败：

  ```
  jobsched_run_failed job=client-ip-stats-aggregation
  error="清理 juhe_stats.client_ip_usage_range_windows 失败: mismatched param and argument count"
  ```

  consecFail 持续增长（22:20 已 18），退避约 40–50s 一轮。客户端 IP 使用窗口的**增量刷新（按 dirty ip_hash）完全不可用**。

## 根因

`backend-go/projects/jobs/internal/statsverify/clientipwindows.go` 的 `refreshRangeWindow` 非 full 分支（`full=false`，按 dirty ip 分块 DELETE+INSERT）：

- `s.placeholders(count)` 固定从 `$1` 起编（`store.go`）。
- DELETE 语句主体已占 `$1`(start_date)、`$2`(end_date)，再把 `IN ($1..$n)` 拼进 WHERE；
- INSERT（`buildInsert(ipFilter, 1)`）主体已占 `$1..$5`，同样嵌入 `IN ($1..$n)`；
- pgx stdlib 按语句内**最大占位符序号**解析参数总数：DELETE 期望 `max(2, n)` 个参数、实际传入 `2+n` 个 → `mismatched param and argument count`；即便数量凑巧相等，`IN ($1)` 也会错绑到 start_date 的参数，语义错误。
- SQLite 方言不受影响：`?` 按出现顺序绑定，无序号概念——缺陷只在 PG 直达路径。

同文件 `readDirtyRows`、`groupstats.go`、`clientipwriter.go` 的 `placeholders(` 调用点核对过，均为语句内唯一占位符组，无此问题。

## 修复（2026-09-26 22:35 已上线）

- DELETE 的 IN 列表改 `s.placeholdersFrom(3, len(chunk))`；INSERT 的 IN 列表改 `s.placeholdersFrom(6, len(chunk))`（buildInsert 主体 `$1..$5` 之后续号）。
- `go build` + `go test ./projects/jobs/internal/statsverify/` 通过；jobs 二进制 `d50170e0…` 上线（仅 jobs，gateway 无涉）。
- 验证口径：jobs 重启后 `client-ip-stats-aggregation` 不再出现该错误（需覆盖一轮带 dirty ip 的增量刷新）。

## 关联

- 与最近 `fix(proberepo): 全部查询缺失 ?→$n 占位符绑定——PG 直达 42601`（b0ff7ff3c）同族：SQLite 惯性写法在 PG 方言下的占位符序号问题。
- 排查中发现的其他事项（独立缺陷，另行跟进）：无。
