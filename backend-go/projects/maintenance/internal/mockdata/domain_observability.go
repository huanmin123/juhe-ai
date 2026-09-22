package mockdata

import "context"

// seedObservability 是可观测域：运行日志、表空间监控、审计日志与 payload blob、
// 操作日志，以及 dataset 库的公开接口日志与后台记录清理目标。
//
// 覆盖点：审计日志需要覆盖成功 / 失败 / 抽样 / 上游响应模型 payload；操作日志
// 需要覆盖管理端筛选可见的模块与动作；公开接口日志需要覆盖正式来源 Token、
// 内置测试 Token、成功、参数错误、鉴权失败、权限不足、限流与服务异常；表空间
// 监控必须包含新增核心表（docs/functions/Mockdata造数设计.md「数据边界」）。
//
// 实施要点（给域实现者）：
//   - 审计 payload blob 与 search-hot 分桶目录在 Paths.AuditBlobDirectory /
//     Paths.SearchHotDirectory 下，写入形态必须能被 F3 读面（logreads）扫到；
//   - 运行日志索引文件在 Paths.RuntimeLog 库内，文件目录是 Paths.LogDir，
//     两者都要有样本，否则日志页面只显示一半。
//
// TODO(domain): 由域实现替换
func seedObservability(ctx context.Context, e *env) (DomainResult, error) {
	return DomainResult{Name: DomainObservability, Counts: map[string]int{}}, nil
}
