package gatewaydispatch

// UpstreamAdapterContractError 是调度内核消费上游适配器错误的通用契约接口
// （docs/functions/调度内核通用化设计.md 5.3）：内核只经 errors.As 接口目标
// 识别适配器错误并读取契约载荷，不再引用具体供应商适配器的具名错误类型。
// 三组能力与既有内核消费点逐点对应：
//   - 类型识别（errors.As 接口目标）：attemptoutcomes.go 的 request_validation
//     审计分类 + rethrow 分支、enginehelpers.go isLocalRequestFailure 联合判断；
//   - IsAccountScoped：dispatchsingle.go 分派段错误分派的调度终止判断
//     （账户级错误走 default 失败分派，非账户级终止整请求）；
//   - AdapterStatusCode / AdapterMessage / AdapterErrorType / AdapterErrorCode：
//     accountpreparation.go wrapCodexPreparationError 失败尝试记录载荷。
//
// 方法名带 Is/Adapter 前缀而与载荷字段同名形式错位的原因：Go 不允许同一
// 类型的字段与方法同名，而具体适配器错误的导出载荷字段是适配器边缘构造点
// 与 gatewaypreauth 渲染层的既有契约（保持原样），故由具体错误类型提供
// 薄包装方法实现本接口。
type UpstreamAdapterContractError interface {
	error

	// IsAccountScoped 报告错误是否只作用于单个账户。
	IsAccountScoped() bool

	// AdapterStatusCode 返回适配器错误对应的 HTTP 状态码。
	AdapterStatusCode() int

	// AdapterMessage 返回适配器错误消息。
	AdapterMessage() string

	// AdapterErrorType 返回适配器错误类型（如 invalid_request_error）。
	AdapterErrorType() string

	// AdapterErrorCode 返回适配器错误码。
	AdapterErrorCode() string
}
