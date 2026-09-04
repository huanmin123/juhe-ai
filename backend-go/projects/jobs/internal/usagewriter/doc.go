// Package usagewriter is the J-F (F6) slice of the Node→Go migration: the
// usage-record (使用记录) writer for the jobs project, mirroring:
//
//	backend/src/storage/usage-record-shards.ts        → shards.go (id 生成、
//	                                                    分片路由、bucket 键)
//	backend/src/storage/usage-records.repository.ts   → rows.go (批量写计划、
//	                                                    列序、默认值、失败归因)、
//	                                                    freeze.go (pricing freeze)、
//	                                                    store.go (分片与目录落库)
//	backend/src/modules/gateway/usage/record-queue.service.ts
//	                                                  → records.go (normalize、
//	                                                    snapshot bounding)、
//	                                                    writer.go (队列/批量/
//	                                                    重试/终态/统计)
//	backend/src/modules/usage-semantics/types.ts      → semantics.go (15 行契约)
//	backend/src/shared/rfc3339.ts                     → rfc3339.go
//	backend/src/shared/queue-size.ts                  → size.go
//
// # 与 G17 (gateway/internal/gatewayusage) 的契约关系
//
// gateway/internal/gatewayusage（另一 Go module，互不可 import）冻结了
// UsageRecordInput 字段契约与 UsageRecorder port。本包的 UsageRecordInput
// 逐字段复刻 G17 records.go 的同名结构（字段名、Go 类型、json tag 一致），
// 保证 G20 装配时的适配层只是机械的值拷贝。分片 id 格式按 G17 的声明归属
// 本 slice（"The shard-id format stays with the writer slice (J-F/G20)"）。
//
// # 队列消灭（批准的架构差异）
//
// Node 侧 enqueueUsageRecord 有四条投递路径（Redis Stream / ingest-worker
// IPC / db-service IPC / 本地队列），其中 Redis Stream 队列按总计划在 Go 侧
// 消灭：本包只保留一条进程内路径——有界 pending 队列 + 单 writer goroutine
// 批量落库，等价于 Node 的本地队列（usageRecordQueueMaxItems 10000 /
// usageRecordQueueMaxMb 64 / batchSize 1000 / flushBatchMaxBytes 8MB /
// flushInterval 500ms / 固定 1s 失败重试 / 停机 drain 100 批）。
// 子进程 writer pool（usage-record-writer-pool.ts）同样消灭：分片写由
// ShardStore 实现自行串行化（SQLite 单写者 + 事务）。
//
// # 失败终态
//
// 对齐 Node：写失败整批保留队头、固定 1s 重试、flushFailureCount 计数；
// 停机 drain（retryOnFailure=false）失败即放弃。任务批准的差异：Node 的
// 本地队列是无限重试，本包提供 Config.MaxWriteAttempts（默认 0 = 无限重试
// 与 Node 完全一致；>0 时同一批重试耗尽后转入死信终态并计数，防止进程内
// 队列永久堆积——Redis Stream 路径消灭后不再有"pending 消息"兜底）。
//
// # pricing freeze
//
// freeze.go 复刻 freezeUsageRecordPricingFactsAsync 的冻结时点语义：记录在
// 入队时点定价一次，冻结的 pricingSnapshot 之后不再按未来目录重新解释
// （对照 gateway/internal/pricing 包注释中的冻结声明；定价算法本身留在
// C03 的 pricing 包，本包通过 CatalogPricing port 调用）。
//
// 所有时间源为注入 Clock；测试只跑内存 mock 与 t.TempDir() 内的 SQLite，
// 不访问真实 DB、Redis、HTTP 或外部文件系统。
package usagewriter
