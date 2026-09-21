package modelcheckquestionbank

import "fmt"

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
