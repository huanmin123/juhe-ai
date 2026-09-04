// Package gatewayresponse 是 Node backend/src/modules/gateway/response/ 的
// Go 迁移（工作包 G16）：上游响应的流式管道、非流式管道、终态 finalization
//（完成 / 失败 / 中止 / 客户端断开）、响应检查决策、失败分类、usage 快照组装
// 以及 G05 冻结的 ResponseSink port 的实现。
//
// 装配面：
//   - 协议层（G02-G04，只 import）：gatewayproto 的 StreamInspector /
//     ParsedUsage / SemanticFrame 契约，gatewayopenai 的 SSE 检查缓冲与语义帧，
//     gatewayanthropic / gatewaygemini 的流检查与 usage 提取；
//   - 前置层（G05，只 import）：gatewaypreauth 的 GatewayRequest /
//     GatewayResponseWriter / 错误载荷渲染 / 审计捕获 port；
//   - kernel：压缩中间件的 SSE 旁路（text/event-stream 不 gzip）与
//     MarkUpstream 语义；
//   - usage / audit 收尾经本包导出的 ports 交接给 G17 消费。
//
// 与 Node 的边界差异（后续工作包承接，本包只保留接缝）：
//   - usage 记录（usage/records.ts）与审计捕获实现 → G17 端口；
//   - 账号状态副作用（runtime/account-effects.ts 等）→ G13 端口；
//   - session 亲和遗忘（G14）、客户端来源避让（G18）、混合路由质量检查 →
//     可选端口，缺省为空实现（保持 Node 的 nil 短路顺序）；
//   - codex-compaction-contract.ts 只迁契约部分（计数 + 失配帧 + 请求判定），
//     codex 桥（G18）对它的强依赖通过本包导出的函数满足。
package gatewayresponse
