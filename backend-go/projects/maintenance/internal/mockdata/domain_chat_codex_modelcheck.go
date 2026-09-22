package mockdata

import "context"

// seedChatCodexModelCheck 是 chat / codex / 模型检测域：chat 会话与消息、
// codex context state 分片（会话、响应状态、压缩摘要）、J3b 模型检测运行与
// 受控 observation、账户健康库的探针结果，以及后台任务运行库的样本。
//
// 覆盖点：chat 需要会话与消息各至少一条；codex 分片需要覆盖全部已配置分片
// （Paths.CodexContextStateShardCount）；模型检测需要覆盖快速 / 深度、运行中、
// 已完成、失败、已取消与深度可信对比样本，受控 observation 只绑定深度样本，
// 并通过真实游标聚合器生成 Token 完整轮次、身份来源、基线、配对窗口与账户
// 最新可信结果。
//
// 实施要点（给域实现者）：
//   - codex context state 分片的表结构用 schema.EnsureSQLiteCodexContext，
//     分片文件名 codexContextShardFilename（state-%03d.sqlite3）；
//   - J3b 专库（Paths.ModelCheck）的表结构用 maintenance 既有
//     j3bmodelcheck 引导路径，不要在本域另写 DDL；
//   - 任务运行库的表结构用 jobs 侧 taskruns 语义，leases 与 state 表不在本域
//     写入（autofill 与覆盖白名单都把它们排除在外）。
//
// TODO(domain): 由域实现替换
func seedChatCodexModelCheck(ctx context.Context, e *env) (DomainResult, error) {
	return DomainResult{Name: DomainChatCodexModelCheck, Counts: map[string]int{}}, nil
}
