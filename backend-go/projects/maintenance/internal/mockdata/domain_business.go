package mockdata

import "context"

// seedBusiness 是 business 库的造数域：系统账户与配套用户、AI 账户、分组、
// 路由策略与分组绑定、API Key（含本地网关明文 Key）、授权与授权来源、团队、
// 代理、公告、外部来源系统、响应检查策略、自定义模型目录、账户测试任务与
// 模型检测题库。
//
// 范围与覆盖点见 docs/functions/Mockdata造数设计.md「数据边界」；本文件只放
// 该域函数与它的私有辅助，其他域不得在此写入。
//
// 实施要点（给域实现者）：
//   - 所有业务名称用 CleanupNamePrefix 前缀、ID/trace 用 CleanupIDPrefix /
//     CleanupTracePrefix，否则重复执行会叠加旧样本；
//   - 新写的表如果不在 autofill.go 的跳过清单里，必须补进去，否则自动补全会在
//     本域写入之后盖占位行；
//   - 创建账户/Key 后用 e.addMockUser / e.addAPIKey / e.setOwner 登记摘要字段。
//
// TODO(domain): 由域实现替换
func seedBusiness(ctx context.Context, e *env) (DomainResult, error) {
	return DomainResult{Name: DomainBusiness, Counts: map[string]int{}}, nil
}
