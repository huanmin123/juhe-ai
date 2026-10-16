# BUG-0320 磁盘写满：审计 payload blob 爆炸与采样率治理

- 级别/状态：P1，已处置（2026-10-16，受控运维变更 + 数据清理，无代码变更待发布）
- 形态：国内单机 Docker 生产（103.36.63.105）

## 现象与影响

2026-10-16 01:5x 用户报障生产服务不可用。根目录 `/` 100% 写满（99G 盘 used 95G、Avail 0）：gateway `/__aisys__/health` 503（`auditLogReady/operationLogReady/ownerReady/accountCircuitRuntimeReady` 全 false——ready 探针依赖 DB 写，PG 因磁盘满写 WAL 失败），caddy 因上游不健康连带 unhealthy，公网全站不可用。PG 在磁盘满窗口内两次崩溃进入 crash-recovery（`RestartCount=67` 为含 10-14 整机僵死期间的历史累计），03:18 空间释放后完成 recovery；gateway/jobs 的 F3/F4 owner 组件在 57P03 窗口重试后 03:19:3x 全部自愈，无 OOM、无 panic。同机公益站共享 PG/Redis/Caddy，随同受影响。

## 根因链

1. **直接原因**：磁盘 100% 满。
2. **占用大头**：`/opt/juhe-ai/data/app/data/audit-payload-blobs/sha256` 53G（23.8 万个文件）+ `search-hot` 热文件 11G。
3. **爆炸起点**：blob 按天数量 10-12 前约 5 千/天 → 10-13 起 4.5 万→9.8 万/天，最近 24h 写入 25.5G（107,513 个文件）；审计行从每天 ~50 条/小时跳到 ~1000 条/小时（10-15 08 时起稳定在该量级）。
4. **流量源头**：carborn 用户 `grok_德娜` Key（key_39a5402ee91717f6）打 grok-4.7 / reasoningEffort=xhigh，**单请求体 7-18MB**（客户端超大上下文），2 小时 1,006 次；每请求审计捕获 4-8 个部件（client_request / upstream_request / upstream_response / gateway_response），单部件平均 ~245KB。
5. **放大器（关键缺陷形态）**：设置 `auditLogSuccessSampleRate=1.0`（2026-09-28 默认整改为全量）使 retention 的成功行 trim 通道**恒不生效**——trim 谓词 `sample_bucket >= rate×10000`（retention.go:45）在 1.0 时为 `>= 10000`，而 bucket 域是 0..9999，永假；成功请求只剩 `SuccessRetentionDays=3` 硬过期一条清理路径。25G/天 写入 × 3 天保留 ≈ 75G 稳态，99G 盘（其余占用 ~40G）必然写满。
6. jobs 的 `data-retention-cleanup` 任务全程正常（每 ~10 分钟 completed），`audit_payload_blob_gc` 意图表为空——**不是清理机制失灵，是清理范围被采样率配置锁死**。

## 处置（2026-10-16 02:0x-03:4x，用户授权）

清理（磁盘 100% → 95%）：

- `docker builder prune -af` 792M；`docker image prune -a` 未使用镜像 1.97G；删除 Exited 13 天的 maintenance 容器（compose run 形态，无保留价值）
- `/var/log/journal` vacuum 至 200M（回收 245M）；`apt-get clean`
- `/opt/juhe-ai/build/bin` 14 个旧二进制 `.bak*`（~700M，保留当前运行三件套）；`build/frontend-dist.bak-*` 30 个旧前端备份（保留最新 1 个）
- `/opt/juhe-ai/data/app/logs` 5 个旧轮转日志（~400M，保留最新 2 个）

受控设置变更（`juhe_business.system_settings` sys_admin 行，value_json 数字；jobs retention 每 tick 热读、无需重启）：

- `auditLogSuccessSampleRate` 1.0 → **0.05**（用户选定方案：成功请求热窗口 1h 内仍全量，窗口外按 5% 留样保留 3 天，问题请求仍 100% 全量）
- `auditLogProblemRetentionDays` 7 → **3**（用户指定）

## 生效机制与验证

- gateway **写入侧**采样率来源是 env 固化值（`auditlog.LoadConfig` 的 `JUHE_AI_AUDIT_LOG_SUCCESS_SAMPLE_RATE`，默认 1.0；`chain_compose.go:759` 构造时注入 `auditSettingsSourceAdapter`），**不读 DB**。本次未动 env——新请求仍全量进热窗口，由 retention 按新阈值在窗口外清理；磁盘治理目标达成且零重启。
- jobs **清理侧**每轮 `ReadRetentionSettings` 直读 DB（`internal/auditlog/settingsread.go`），下一 tick 即生效。
- 验证：设置变更后磁盘 100% → 95% → 92% → 91%（可用 8.7G）持续回升；一轮内 refs 释放近 5 万行、blob 行/文件转净减少——清理速率 > 写入速率，趋势反转确认；五容器 healthy、公网正常。

## 错误去重调研结论（用户提问"相同问题能否去重"）

- `juhe_dataset.audit_error_groups` 已有完整错误分组：fingerprint、request/error_fingerprint、时间窗口、count、sample_event_id；失败请求 `error_group_id` 覆盖率 100%（12742/12742）。**同问题聚合展示已存在**。
- 缺口在存储层：同组内每条失败请求仍各自全量存 payload。7 天内 14,203 条失败事件仅 4,054 组（avg 3.5 条/组，max 47）；Top 持续性错误 `model_unsupported` 5,727 条、`invalid_request_error` 4,069 条。按组只保留 sample_event_id 的 payload、其余降级 metadata_only/hash_only，失败面存储约可降至 28.5%，对持续性错误风暴收益最大。
- 定性：写入路径行为变更，需文档先行立项（利用现有 error_group 在写入时判定组内是否已有样本），不在本次运维处置内顺手改码。
- 后记（2026-10-16）：错误组存储去重已实现落地，最终方案与本节调研结论有四处实质偏离——降级枚举为新增的 `deduped` / `error_group_deduped`（不复用 `metadata_only`/`hash_only`）；保留样本按 10 分钟窗口移交而非全局单点（同窗口连续相同错误视为一致）；同窗口重复在写入侧"出生即降级"，blob 不落盘、零 GC 往返；请求侧（`client_request`/`upstream_request`）与 `gateway_metadata` 不降级。当前权威口径见 `docs/functions/原始审计日志设计.md` 的"错误组存储去重"节。

## 遗留观察

1. `search-hot/audit-hot-2026101517.ndjson` 按小时命名却持续写入 8h 至 7.5G——热文件滚动疑似缺陷；采样率生效后热写入量将下降，滚动行为另查。
2. 7-18MB 单请求体属异常客户端行为；如需网关侧请求体上限另立项。
3. 问题请求 3 天保留在新流量形态下的稳态占用继续观察（失败面仍全量捕获）。
4. gateway 容器 StartedAt 02:22、RestartCount=0（被 recreate 而非 crash 重启），非本会话操作，未深究；期间健康检查正常。
5. 磁盘水位建议纳入日巡检：blob 目录大小 + `df`（可用 <15% 时预警）；监控栈 node-exporter 在跑但无磁盘告警规则，可按 2026-09-28"定期巡检"口径在 Grafana 加红绿灯。

## 教训

1. "100% 采样"让分层保留的 trim 通道变成死代码——保留策略的每个参数档位都应有对应的生效量观测（每轮 retention 的 trim 行数入监控），配置把清理范围锁死时没有任何信号。
2. 审计 payload 是磁盘上唯一随客户端流量形态无上限增长的数据面，容量规划必须按"峰值流量 × 保留窗口"估算，不能按历史均值。
3. 磁盘 100% 满是用户报障发现的——巡检路线应补磁盘水位项。
