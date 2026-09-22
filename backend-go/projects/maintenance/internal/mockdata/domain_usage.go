package mockdata

import "context"

// seedUsage 是用量域：usage-catalog 库的分片登记 + usage-shards 分片文件里的
// usage_records 明细。
//
// 范围：gateway / manual_account_test / account_health_check / cooldown_retest
// 来源，OpenAI 与 Anthropic 端点，成功与失败样本，图片 token、缓存读取、
// 模型映射命中、上游响应模型一致 / 不一致 / 无映射不一致三连样本、流式与非流式、
// 服务档位与思考强度样本，时间跨度由 Options.Days 决定。
//
// 实施要点（给域实现者）：
//   - 分片文件的建表语句必须用 schema.EnsureSQLiteUsageShard（jobs 侧
//     UsageShardBaseSchemaSQL 的逐字副本），不要在本域另写 DDL；
//   - 分片路径 <UsageShardRoot>/<bucketDateKey>/<shardID>.sqlite3，分片号取
//     Options.Paths.UsageShardCount 的模，分片文件由本域按需创建；
//   - 覆盖断言（coverage.go）要求图片 token、模型映射命中、上游响应模型一致
//     与不一致样本各至少一行。
//
// TODO(domain): 由域实现替换
func seedUsage(ctx context.Context, e *env) (DomainResult, error) {
	return DomainResult{Name: DomainUsage, Counts: map[string]int{}}, nil
}
