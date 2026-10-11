package modelcheckquestionbank

import (
	"errors"
	"fmt"
)

// 调用方可见错误对应的 HTTP 状态码。store 层不依赖 net/http，
// HTTP 层按这些状态渲染响应。
const (
	statusBadRequest   = 400
	statusUnauthorized = 401
	statusForbidden    = 403
	statusNotFound     = 404
	statusConflict     = 409
)

// StatusError 是调用方可见的题库域错误：HTTP 层按 Status 渲染中文
// Message，其余错误一律按 500 处理，不把存储细节泄漏给调用方。
type StatusError struct {
	Status  int
	Message string
}

func (e *StatusError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func newStatusError(status int, format string, args ...any) *StatusError {
	return &StatusError{Status: status, Message: fmt.Sprintf(format, args...)}
}

// 提交门禁测试记录哨兵错误（FindUsableTest 返回，HTTP 层按 400 渲染对应
// 中文文案，见 writeStoreError 的 testGateMessage 映射）。
var (
	// ErrTestNotFound：测试记录不存在或不属于当前操作者（WHERE 含
	// created_by，不泄露他人记录的存在性）。
	ErrTestNotFound = errors.New("测试记录不存在，请重新完成提交测试")
	// ErrTestNotPassed：记录存在但 verdict 非 pass。
	ErrTestNotPassed = errors.New("提交测试未通过，不能提交题目")
	// ErrTestDraftMismatch：记录的 draft_hash 与本次提交内容不一致。
	ErrTestDraftMismatch = errors.New("测试记录与题目内容不一致，请重新测试")
	// ErrTestExpired：记录已超过 24 小时有效窗口。
	ErrTestExpired = errors.New("测试记录已超过 24 小时，请重新测试")
)
