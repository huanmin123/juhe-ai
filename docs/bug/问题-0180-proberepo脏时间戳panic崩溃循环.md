# BUG-0180 proberepo 读取脏时间戳触发 panic 崩溃循环风险

## 基本信息

- 编号：BUG-0180
- 状态：已修复（2026-09-20，提交 `46c367e18`）
- 发现时间：2026-09-20（w16k 覆盖率补齐任务的 profile 分析中发现）
- 关联模块：后端 / shared/platform / 探针 worker
- 编号勘误：本文档标题与正文曾误写为 "BUG-0009"，2026-09-20 复核时更正为 BUG-0180（BUG-0009 另有其文 `问题-0009-菜单切换短暂卡顿.md`，与本缺陷无关）。

## 问题

`shared/platform/accounttest/proberepo/reader.go` 的可用性推导路径对数据库中的脏时间戳数据使用 `panic(err)` 而非错误返回：

- `reader.go:354`：`account_expires_at` 无法解析时 `panic(err)`（`isPastInstant` 失败）；
- `reader.go:377`：`cooldown_until` 无法解析时 `panic(err)`（`isFutureInstant` 失败）。

**影响**：`juhe_business` 账户行中任一条 `account_expires_at`/`cooldown_until` 存在非法值（手工修数、历史脏数据、导入误差），probe worker 读取该账户时会 panic → supervisor 重启 → 重启后同一条脏行仍在 → **panic 崩溃循环**，拖垮整个探针 worker（全部账户的探针停摆），并以每次重启一轮的频率刷错误日志。

## 复现路径

1. 在 `juhe_business.accounts`（或 probe 读取的同源行）将某账户 `account_expires_at` 置为非法字符串；
2. 触发 probe worker 读取该账户（到期候选扫描）；
3. 观察 worker panic 与重启循环。

## 建议修复（历史记录，已按方案 1 落地）

1. `deriveEffectiveAvailability` 路径将 `panic(err)` 改为返回错误（函数签名增加 error 或跳过该账户并记 warn 日志 + 降级为不可调度），语义对齐 Node 版的容错行为；
2. 修复补回归：库内脏时间戳行 → worker 不崩溃、该账户降级/跳过、其余账户正常。

## 2026-09-20 复核结论

**结论：缺陷真实存在过，已由提交 `46c367e18`（2026-09-20 10:36:07 +0800，"feat(gateway): 添加健康快照功能并改进导入导出与定价数据"）修复。** 当前代码该包零 `panic(`，脏时间戳走错误返回。

证据：

1. **缺陷引入**：`git log -S "panic(err)" -- backend-go/shared/platform/accounttest/proberepo/reader.go` 命中两个提交。`7e36e20ba`（2026-09-07，"refactor(gateway): 移除跨进程审计和健康检查调度，改用进程内实现"）在该文件新增两处 `panic(err)`（位于该版本的 `reader.go:349` 与 `reader.go:372`）——登记时点缺陷真实存在。
2. **修复落地**：`46c367e18` 移除上述两处 `panic(err)`（diff -132/-163 行），改为错误上抛，并留下注释：
   - `reader.go:170`："BUG-0180：账户行时间戳脏数据按错误上抛（原 panic 会使长运行 worker……）"；
   - `reader.go:293`："……error 而非 panic（BUG-0180：长运行 worker 对脏行不得崩溃循环）"。
3. **当前代码**：`rg -n "panic\(" backend-go/shared/platform/accounttest/proberepo/` 零命中；两处脏时间戳路径现为错误返回：
   - `reader.go:359`：`fmt.Errorf("解析 account_expires_at 失败: %w", err)`；
   - `reader.go:382`：`fmt.Errorf("解析 cooldown_until 失败: %w", err)`。
   行号相对登记时（354/377）因修复注释插入整体后移。
4. **回归测试**：同提交新增 `proberepo/w18_dirty_timestamp_test.go`（`TestW18DeriveDirtyTimestampReturnsError`），断言脏 `account_expires_at`/`cooldown_until` 返回指明脏字段的错误而非 panic，覆盖本缺陷"建议修复"第 2 条。
5. **联动项**：原文备注的 jobs cmd `worker_probe_jobs.go:288-290` 错误传导臂（`parseRFC3339Millis` 失败 `return nil, err`）未随修复提交改动，依赖的错误传导现已成立，该臂自然可达，与预期一致。

未完成取证：原文"待对照 `migration-backup` 确认 Node 是否同样 panic"——本次复核未在 Node 归档中定位到探针可用性推导的对应解析代码，Node 侧语义未取证。因 Go 侧修复已落地为错误返回且不影响 Node 归档（只读），该遗留仅作记录，不再阻塞本缺陷。
