// Package studio 是媒体工作室产品域（媒体工作室设计 §3/§4）：登录用户的
// 素材资产库（studio_assets + studio_user_asset_usage 配额账本）、purpose=
// studio 专用 Key 端口与 /my-studio 账户·模型·资产路由族。本批只交付资产/
// 账户/模型目录三面；generations/tasks 端点与 jobs 结算 worker 属后续批次。
// 表结构权威在《媒体工作室设计》§3（DDL 由 maintenance 幂等迁移交付）。
package studio

import (
	"fmt"
	"net/http"
)

// 稳定错误码（媒体工作室设计 §4.6 错误码总表，资产/通用部分）。code 是对
// 外契约，实现可补上下文但不得改码。
const (
	CodeInvalidRequest          = "studio_invalid_request"
	CodeDomainInvalid           = "studio_domain_invalid"
	CodeAssetNotFound           = "studio_asset_not_found"
	CodeAssetPreviewUnavailable = "studio_asset_preview_unavailable"
	CodeAssetReferenced         = "studio_asset_referenced"
	CodeAssetQuotaExceeded      = "studio_asset_quota_exceeded"
	CodeAssetCountExceeded      = "studio_asset_count_exceeded"

	// CodeOperationIDInvalid 是 clientOperationId 缺失或格式非法（400，§4.6）。
	CodeOperationIDInvalid = "studio_operation_id_invalid"
	// CodeDomainNotAuthorized 是 studio Key 域授权未含请求域（403）。该码由
	// 第二批任务指令补充（§4.6 总表未列），词表与判定在 key.go/执行链。
	CodeDomainNotAuthorized = "studio_domain_not_authorized"
	// CodeAccountNotAuthorized 是账户不在当前用户可派发范围（403，§4.3/§4.6）。
	CodeAccountNotAuthorized = "studio_account_not_authorized"
	// CodeModelDomainMismatch 是模型不属于所选能力域（400，§4.3/§4.6）。
	CodeModelDomainMismatch = "studio_model_domain_mismatch"
	// CodeTaskNotFound 是任务不存在（404，越权同型，§4.4/§4.6）。
	CodeTaskNotFound = "studio_task_not_found"
	// CodeTaskAlreadyCompleted 是 completed 未结算任务的取消拒绝（409）。
	// 第二批任务指令裁决：completed 未结算 → 409 本地不删，仅停止展示由前端
	// 处理（契约 §4.4 c 态原文为 200 + message，差异已登记交付报告）。
	CodeTaskAlreadyCompleted = "studio_task_already_completed"
	// CodeGenerationFailed 是生成执行失败（502，脱敏含上游错误摘要，§4.3/§4.6）。
	CodeGenerationFailed = "studio_generation_failed"
	// CodeUpstreamUnavailable 是受理前派发穷尽/上游暂不可用（502，§4.3/§4.6）。
	CodeUpstreamUnavailable = "studio_upstream_unavailable"
	// CodeOperationInProgress 是同用户操作串行化冲突的稳定码（409）。本批
	// 资产/目录端点不产生该冲突，为 generations 批次的创建串行化（设计
	// §4.5.7）预留；与 §4.6 既有码共存，不替代 studio_asset_referenced。
	CodeOperationInProgress = "studio_operation_in_progress"
)

// RouteError 是带稳定码的路由层错误：Status/Code/Message 直接映射
// {message, code} 错误响应（§4.0 通用约定）。
type RouteError struct {
	Status  int
	Code    string
	Message string
}

func (e *RouteError) Error() string { return e.Message }

func newRouteError(status int, code, message string) *RouteError {
	return &RouteError{Status: status, Code: code, Message: message}
}

// InvalidRequest renders 400 studio_invalid_request（请求参数无效）。
func InvalidRequest(format string, args ...any) *RouteError {
	return newRouteError(http.StatusBadRequest, CodeInvalidRequest, fmt.Sprintf(format, args...))
}

// DomainInvalid renders 400 studio_domain_invalid（domain 参数非法）。
func DomainInvalid() *RouteError {
	return newRouteError(http.StatusBadRequest, CodeDomainInvalid, "domain 参数非法")
}

// AssetNotFound renders 404 studio_asset_not_found（素材不存在；越权与不存在
// 同型，不泄露存在性，§4.0/§7）。
func AssetNotFound() *RouteError {
	return newRouteError(http.StatusNotFound, CodeAssetNotFound, "素材不存在")
}

// AssetPreviewUnavailable renders 400 studio_asset_preview_unavailable（P0
// 无 preview 生成管线，variant=preview 一律拒绝，§4.2）。
func AssetPreviewUnavailable() *RouteError {
	return newRouteError(http.StatusBadRequest, CodeAssetPreviewUnavailable, "该素材类型不支持 preview 变体")
}

// AssetReferenced renders 409 studio_asset_referenced（素材被未终态结算任务
// 引用，无法删除，§4.2 引用保护）。
func AssetReferenced() *RouteError {
	return newRouteError(http.StatusConflict, CodeAssetReferenced, "素材正被生成任务结算引用，无法删除")
}

// AssetQuotaExceeded renders 413 studio_asset_quota_exceeded（容量已满，
// §3.6）。
func AssetQuotaExceeded() *RouteError {
	return newRouteError(http.StatusRequestEntityTooLarge, CodeAssetQuotaExceeded, "素材存储容量已满，请删除不用的素材后重试")
}

// AssetCountExceeded renders 413 studio_asset_count_exceeded（个数已达上限，
// §3.6）。
func AssetCountExceeded() *RouteError {
	return newRouteError(http.StatusRequestEntityTooLarge, CodeAssetCountExceeded, "素材个数已达上限，请删除不用的素材后重试")
}

// OperationInProgress renders 409 studio_operation_in_progress（同用户操作
// 串行化冲突，generations 批次消费）。
func OperationInProgress() *RouteError {
	return newRouteError(http.StatusConflict, CodeOperationInProgress, "当前有进行中的操作，请稍后重试")
}

// OperationIDInvalid renders 400 studio_operation_id_invalid（clientOperationId
// 缺失或格式非法，§4.0 幂等协议：1..128 字符、用户范围唯一）。
func OperationIDInvalid() *RouteError {
	return newRouteError(http.StatusBadRequest, CodeOperationIDInvalid, "clientOperationId 必填且为 1 到 128 个字符")
}

// DomainNotAuthorized renders 403 studio_domain_not_authorized（studio Key 域
// 授权未含请求域，studio_domains_json 授权数据判定，§3.5.3）。
func DomainNotAuthorized() *RouteError {
	return newRouteError(http.StatusForbidden, CodeDomainNotAuthorized, "当前工作室授权未包含该能力域")
}

// AccountNotAuthorized renders 403 studio_account_not_authorized（账户不在当
// 前用户可派发范围，§4.3 公共规则）。
func AccountNotAuthorized() *RouteError {
	return newRouteError(http.StatusForbidden, CodeAccountNotAuthorized, "账户不在当前用户可派发范围")
}

// ModelDomainMismatch renders 400 studio_model_domain_mismatch（模型不属于所
// 选能力域目录，§4.3 公共规则）。
func ModelDomainMismatch() *RouteError {
	return newRouteError(http.StatusBadRequest, CodeModelDomainMismatch, "模型不属于所选能力域")
}

// TaskNotFound renders 404 studio_task_not_found（任务不存在；越权与不存在同
// 型，不泄露存在性，§4.4/§4.0）。
func TaskNotFound() *RouteError {
	return newRouteError(http.StatusNotFound, CodeTaskNotFound, "任务不存在")
}

// TaskAlreadyCompleted renders 409 studio_task_already_completed（completed 未
// 结算任务不接受取消——仅停止展示由前端处理，本地不删；第二批任务指令裁决，
// 见 errors.go 常量注释）。
func TaskAlreadyCompleted() *RouteError {
	return newRouteError(http.StatusConflict, CodeTaskAlreadyCompleted, "任务已完成，无法取消")
}

// GenerationFailed renders 502 studio_generation_failed（生成执行失败，脱敏含
// 上游错误摘要，§4.3）。
func GenerationFailed(summary string) *RouteError {
	message := "生成执行失败"
	if summary != "" {
		message = "生成执行失败：" + summary
	}
	return newRouteError(http.StatusBadGateway, CodeGenerationFailed, message)
}

// UpstreamUnavailable renders 502 studio_upstream_unavailable（受理前派发穷
// 尽/上游暂不可用，§4.3）。
func UpstreamUnavailable() *RouteError {
	return newRouteError(http.StatusBadGateway, CodeUpstreamUnavailable, "上游暂不可用，请稍后重试")
}

// QuotaExceededError / CountExceededError 是存储层向路由层传递配额越界的
// 领域错误（CreateAsset 事务内判定），由路由层映射为 413 两个稳定码。
type QuotaExceededError struct{}

func (e *QuotaExceededError) Error() string {
	return "素材存储容量已满，请删除不用的素材后重试"
}

type CountExceededError struct{}

func (e *CountExceededError) Error() string {
	return "素材个数已达上限，请删除不用的素材后重试"
}
