package managementopenaioauth

import "errors"

type ErrorCode string

const (
	ErrorCodeRequestInvalid          ErrorCode = "oauth_request_invalid"
	ErrorCodeStateInvalid            ErrorCode = "oauth_state_invalid"
	ErrorCodeAccountStateInvalid     ErrorCode = "oauth_account_state_invalid"
	ErrorCodeAccountNotFound         ErrorCode = "oauth_account_not_found"
	ErrorCodeSessionExpired          ErrorCode = "oauth_session_expired"
	ErrorCodeSessionProcessing       ErrorCode = "oauth_session_processing"
	ErrorCodeSessionConsumed         ErrorCode = "oauth_session_consumed"
	ErrorCodeGrantInvalid            ErrorCode = "oauth_grant_invalid"
	ErrorCodeAccountConflict         ErrorCode = "oauth_account_conflict"
	ErrorCodeUpstreamUnavailable     ErrorCode = "oauth_upstream_unavailable"
	ErrorCodeSessionStoreUnavailable ErrorCode = "oauth_session_store_unavailable"
)

type errorDefinition struct {
	status  int
	message string
}

var errorDefinitions = map[ErrorCode]errorDefinition{
	ErrorCodeRequestInvalid:          {status: 400, message: "OAuth 请求参数无效"},
	ErrorCodeStateInvalid:            {status: 400, message: "OAuth state 无效"},
	ErrorCodeAccountStateInvalid:     {status: 400, message: "当前账户状态不允许执行 OAuth 操作"},
	ErrorCodeAccountNotFound:         {status: 404, message: "OAuth 账户不存在或无权操作"},
	ErrorCodeSessionExpired:          {status: 409, message: "OAuth 会话不存在或已过期"},
	ErrorCodeSessionProcessing:       {status: 409, message: "OAuth 会话正在处理中"},
	ErrorCodeSessionConsumed:         {status: 409, message: "OAuth 会话已被消费"},
	ErrorCodeGrantInvalid:            {status: 409, message: "OAuth 授权材料无效、已过期或已撤销"},
	ErrorCodeAccountConflict:         {status: 409, message: "OAuth 账户已发生并发变更"},
	ErrorCodeUpstreamUnavailable:     {status: 502, message: "OpenAI OAuth 服务暂时不可用"},
	ErrorCodeSessionStoreUnavailable: {status: 503, message: "OAuth 会话服务暂时不可用"},
}

type Error struct {
	StatusCode int
	Code       ErrorCode
	Message    string
	cause      error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *Error) Response() ErrorResponse {
	if e == nil {
		fallback := NewError(ErrorCodeUpstreamUnavailable, nil)
		return ErrorResponse{Code: fallback.Code, Message: fallback.Message}
	}
	return ErrorResponse{Code: e.Code, Message: e.Message}
}

func (c ErrorCode) Valid() bool {
	_, ok := errorDefinitions[c]
	return ok
}

func NewError(code ErrorCode, cause error) *Error {
	definition, ok := errorDefinitions[code]
	if !ok {
		code = ErrorCodeUpstreamUnavailable
		definition = errorDefinitions[code]
	}
	return &Error{
		StatusCode: definition.status,
		Code:       code,
		Message:    definition.message,
		cause:      cause,
	}
}

func ErrorCodeOf(err error) ErrorCode {
	var oauthErr *Error
	if errors.As(err, &oauthErr) {
		return oauthErr.Code
	}
	return ""
}
