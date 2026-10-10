package gatewaydispatch

import (
	"errors"
	"fmt"
	"testing"
)

// mockAdapterContractError 是与具体适配器无关的契约 Mock（调度内核通用化设计
// 5.3 Mock 回归）：内核消费点只依赖 UpstreamAdapterContractError 接口时，
// 任何实现该接口的错误都应被等价识别与读取，无需引用具名适配器错误类型。
type mockAdapterContractError struct {
	accountScoped bool
	statusCode    int
	message       string
	errorType     string
	code          string
}

func (e *mockAdapterContractError) Error() string            { return e.message }
func (e *mockAdapterContractError) IsAccountScoped() bool    { return e.accountScoped }
func (e *mockAdapterContractError) AdapterStatusCode() int   { return e.statusCode }
func (e *mockAdapterContractError) AdapterMessage() string   { return e.message }
func (e *mockAdapterContractError) AdapterErrorType() string { return e.errorType }
func (e *mockAdapterContractError) AdapterErrorCode() string { return e.code }

// 编译期断言：OpenAI OAuth Codex 适配器错误实现通用适配器错误契约接口。
var _ UpstreamAdapterContractError = (*OpenAIOAuthCodexAdapterError)(nil)

func TestUpstreamAdapterContractErrorAsMatchesCodexAdapterError(t *testing.T) {
	original := NewOpenAIOAuthCodexAdapterError("请求体必须是 JSON 对象",
		WithCodexAdapterStatus(422),
		WithCodexAdapterCode("custom_code"),
		WithCodexAdapterType("custom_type"))
	wrapped := fmt.Errorf("账户准备失败: %w", original)

	var contractErr UpstreamAdapterContractError
	if !errors.As(wrapped, &contractErr) {
		t.Fatalf("errors.As 未以接口目标识别适配器错误: %v", wrapped)
	}
	if contractErr.AdapterMessage() != "请求体必须是 JSON 对象" {
		t.Fatalf("AdapterMessage = %q", contractErr.AdapterMessage())
	}
	if contractErr.AdapterStatusCode() != 422 {
		t.Fatalf("AdapterStatusCode = %d", contractErr.AdapterStatusCode())
	}
	if contractErr.AdapterErrorType() != "custom_type" {
		t.Fatalf("AdapterErrorType = %q", contractErr.AdapterErrorType())
	}
	if contractErr.AdapterErrorCode() != "custom_code" {
		t.Fatalf("AdapterErrorCode = %q", contractErr.AdapterErrorCode())
	}
	if contractErr.IsAccountScoped() {
		t.Fatal("默认构造必须是非账户级错误")
	}
}

func TestUpstreamAdapterContractErrorAccountScopedPayload(t *testing.T) {
	scoped := NewOpenAIOAuthCodexAdapterError("覆盖值非法", WithCodexAdapterAccountScoped())
	var scopedTarget UpstreamAdapterContractError
	if !errors.As(error(scoped), &scopedTarget) {
		t.Fatal("errors.As 未识别账户级适配器错误")
	}
	if !scopedTarget.IsAccountScoped() {
		t.Fatal("WithCodexAdapterAccountScoped 构造必须经契约报告账户级")
	}

	plain := NewOpenAIOAuthCodexAdapterError("请求体必须是 JSON 对象")
	var plainTarget UpstreamAdapterContractError
	if !errors.As(error(plain), &plainTarget) {
		t.Fatal("errors.As 未识别适配器错误")
	}
	if plainTarget.IsAccountScoped() {
		t.Fatal("默认构造必须报告非账户级")
	}
}

func TestUpstreamAdapterContractErrorKernelConsumesMock(t *testing.T) {
	// 内核联合判断（isLocalRequestFailure，enginehelpers.go）对 Mock 契约错误
	// 与具名适配器错误同等识别；其他错误不进入 request_validation 分类。
	mock := &mockAdapterContractError{message: "mock", statusCode: 400}
	if !isLocalRequestFailure(mock) {
		t.Fatal("内核必须仅凭契约接口识别 Mock 适配器错误")
	}
	if !isLocalRequestFailure(fmt.Errorf("包装: %w", mock)) {
		t.Fatal("内核必须经错误链识别 Mock 契约错误")
	}
	if !isLocalRequestFailure(NewOpenAIOAuthCodexAdapterError("x")) {
		t.Fatal("内核必须继续识别具名适配器错误")
	}
	if isLocalRequestFailure(errors.New("其他错误")) {
		t.Fatal("非契约错误不得进入 request_validation 分类")
	}

	// errors.As 接口目标对 Mock 与具名错误同样命中，载荷逐点读取。
	var contractErr UpstreamAdapterContractError
	if !errors.As(error(mock), &contractErr) || contractErr.AdapterStatusCode() != 400 {
		t.Fatal("errors.As 接口目标未命中 Mock 契约错误或载荷不符")
	}
}
