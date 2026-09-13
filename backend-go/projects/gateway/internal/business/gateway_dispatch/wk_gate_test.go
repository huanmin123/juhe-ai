package gatewaydispatch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	keymodelruntime "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/key_model_runtime"
)

// wkErrGate 用于驱动 AdmitForeground 失败分支。
type wkErrGate struct {
	fakeGate
	err error
}

func (g *wkErrGate) AdmitForeground(context.Context, keymodelruntime.Capability, string) (keymodelruntime.ForegroundDecision, keymodelruntime.ForegroundPermit, uint64, error) {
	return "", keymodelruntime.ForegroundPermit{}, 0, g.err
}

// wkNilAttemptGate 返回 dispatchable 但没有 attempt，驱动护栏分支。
type wkNilAttemptGate struct{}

func (wkNilAttemptGate) Prepare(context.Context, AccountCircuitInput) (AccountCircuitDecision, AccountCircuitAttempt, error) {
	return AccountCircuitDispatchable, nil, nil
}

// wkErrAttempt 让 circuit 上报失败，驱动 CompleteSuccess 的错误聚合分支。
type wkErrAttempt struct{}

func (wkErrAttempt) ReportFramingComplete(context.Context) error { return errors.New("framing boom") }
func (wkErrAttempt) ReportTransportFailure(context.Context, error) error {
	return errors.New("transport boom")
}
func (wkErrAttempt) ReportUnknown(context.Context) error { return errors.New("unknown boom") }

type wkErrCircuitGate struct{}

func (wkErrCircuitGate) Prepare(context.Context, AccountCircuitInput) (AccountCircuitDecision, AccountCircuitAttempt, error) {
	return "", nil, errors.New("prepare boom")
}

func TestDispatchInputValidation(t *testing.T) {
	gate := &fakeGate{admitted: true}
	req, _ := http.NewRequest(http.MethodGet, "https://example.test", nil)
	tests := []struct {
		name    string
		d       Dispatcher
		input   func() Request
		wantErr error
	}{
		{name: "missing client", d: Dispatcher{KeyModel: gate}, input: func() Request {
			return Request{HTTP: req, AttemptID: "a"}
		}, wantErr: ErrClientRequired},
		{name: "missing request", d: Dispatcher{Client: fakeClient{}, KeyModel: gate}, input: func() Request {
			return Request{AttemptID: "a"}
		}, wantErr: nil},
		{name: "missing attempt id", d: Dispatcher{Client: fakeClient{}, KeyModel: gate}, input: func() Request {
			return Request{HTTP: req}
		}, wantErr: nil},
		{name: "missing key model gate", d: Dispatcher{Client: fakeClient{}}, input: func() Request {
			return Request{HTTP: req, AttemptID: "a"}
		}, wantErr: nil},
		{name: "circuit input required", d: Dispatcher{Client: fakeClient{}, KeyModel: gate, Circuit: &fakeCircuitGate{}}, input: func() Request {
			return Request{HTTP: req, AttemptID: "a"}
		}, wantErr: nil},
		{name: "prepare error", d: Dispatcher{Client: fakeClient{}, KeyModel: gate, Circuit: wkErrCircuitGate{}}, input: func() Request {
			return Request{HTTP: req, AttemptID: "a", AccountCircuit: &AccountCircuitInput{AccountID: "a"}}
		}, wantErr: nil},
		{name: "dispatchable without attempt", d: Dispatcher{Client: fakeClient{}, KeyModel: gate, Circuit: wkNilAttemptGate{}}, input: func() Request {
			return Request{HTTP: req, AttemptID: "a", AccountCircuit: &AccountCircuitInput{AccountID: "a"}}
		}, wantErr: nil},
		{name: "admit error reports unknown", d: Dispatcher{Client: fakeClient{}, KeyModel: &wkErrGate{err: errors.New("redis down")}, Circuit: &fakeCircuitGate{}}, input: func() Request {
			return Request{HTTP: req, AttemptID: "a", AccountCircuit: &AccountCircuitInput{AccountID: "a"}}
		}, wantErr: nil},
		{name: "admission blocked reports unknown", d: Dispatcher{Client: fakeClient{}, KeyModel: &fakeGate{}, Circuit: &fakeCircuitGate{}}, input: func() Request {
			return Request{HTTP: req, AttemptID: "a", AccountCircuit: &AccountCircuitInput{AccountID: "a"}}
		}, wantErr: ErrAttemptBlocked},
		{name: "nil response", d: Dispatcher{Client: fakeClient{}, KeyModel: gate}, input: func() Request {
			return Request{HTTP: req, AttemptID: "a"}
		}, wantErr: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tc.d.Dispatch(context.Background(), tc.input())
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("错误不符: got=%v want=%v", err, tc.wantErr)
				}
				return
			}
			if err == nil {
				t.Fatalf("预期失败但成功: %+v", result)
			}
		})
	}
}

func TestDispatchCircuitUnknownWhenAdmissionBlocked(t *testing.T) {
	// 契约：准入被拒时必须以 unknown 语义结算电路尝试，避免悬挂租约。
	gate := &fakeGate{}
	circuit := &fakeCircuitGate{}
	d := Dispatcher{Client: fakeClient{}, KeyModel: gate, Circuit: circuit}
	req, _ := http.NewRequest(http.MethodGet, "https://example.test", nil)
	_, err := d.Dispatch(context.Background(), Request{HTTP: req, AttemptID: "a", AccountCircuit: &AccountCircuitInput{AccountID: "a"}})
	if !errors.Is(err, ErrAttemptBlocked) {
		t.Fatalf("err=%v", err)
	}
	if circuit.attempt.unknown != 1 {
		t.Fatalf("blocked 准入未上报 unknown: %+v", circuit.attempt)
	}
}

func TestDispatchAdmitErrorReportsCircuitUnknown(t *testing.T) {
	circuit := &fakeCircuitGate{}
	d := Dispatcher{Client: fakeClient{}, KeyModel: &wkErrGate{err: errors.New("boom")}, Circuit: circuit}
	req, _ := http.NewRequest(http.MethodGet, "https://example.test", nil)
	_, err := d.Dispatch(context.Background(), Request{HTTP: req, AttemptID: "a", AccountCircuit: &AccountCircuitInput{AccountID: "a"}})
	if err == nil || !strings.Contains(err.Error(), "admit key-model foreground") {
		t.Fatalf("admit 错误未被包裹: %v", err)
	}
	if circuit.attempt.unknown != 1 {
		t.Fatalf("admit 失败未上报 unknown: %+v", circuit.attempt)
	}
}

func TestDispatchNilResponseReportsUnknown(t *testing.T) {
	gate := &fakeGate{admitted: true}
	circuit := &fakeCircuitGate{}
	d := Dispatcher{Client: fakeClient{}, KeyModel: gate, Circuit: circuit}
	req, _ := http.NewRequest(http.MethodGet, "https://example.test", nil)
	result, err := d.Dispatch(context.Background(), Request{HTTP: req, AttemptID: "a", AccountCircuit: &AccountCircuitInput{AccountID: "a"}})
	if err == nil || !strings.Contains(err.Error(), "response/body is missing") {
		t.Fatalf("nil 响应错误不符: %v", err)
	}
	if result.Attempt == nil || circuit.attempt.unknown != 1 {
		t.Fatalf("nil 响应未上报 unknown: %+v", circuit.attempt)
	}
}

func TestAttemptLifecycleSemantics(t *testing.T) {
	gate := &fakeGate{admitted: true}
	d := Dispatcher{Client: fakeClient{response: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}}, KeyModel: gate}
	req, _ := http.NewRequest(http.MethodGet, "https://example.test", nil)
	result, err := d.Dispatch(context.Background(), Request{HTTP: req, AttemptID: "a", Capability: dispatchCapability()})
	if err != nil {
		t.Fatal(err)
	}
	if result.Attempt.Permit().AttemptID == "" {
		t.Fatalf("Permit() 未暴露准入租约: %+v", result.Attempt.Permit())
	}
	attempt := result.Attempt
	if ok, err := attempt.Renew(context.Background()); err != nil || !ok {
		t.Fatalf("renew: %v %v", ok, err)
	}
	// framing 完成只结算电路侧，foreground permit 仍可续期。
	if err := result.ReportFramingComplete(context.Background()); err != nil {
		t.Fatalf("framing complete: %v", err)
	}
	if ok, err := attempt.Renew(context.Background()); err != nil || !ok {
		t.Fatalf("framing 后 renew 仍应可用: %v %v", ok, err)
	}
	// Release 结算后 Renew 必须拒绝。
	if err := attempt.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := attempt.Renew(context.Background()); !errors.Is(err, ErrAttemptSettled) {
		t.Fatalf("settled renew err=%v", err)
	}
	var nilAttempt *Attempt
	if _, err := nilAttempt.Renew(context.Background()); !errors.Is(err, ErrAttemptSettled) {
		t.Fatalf("nil attempt renew err=%v", err)
	}
	// 无 gate 的 Unknown 是 no-op（防御分支）。
	gateless := &Attempt{}
	if err := gateless.Unknown(context.Background(), time.Now(), "req"); err != nil {
		t.Fatalf("gateless unknown 必须为 no-op: %v", err)
	}
	if err := nilAttempt.Release(context.Background()); err != nil {
		t.Fatalf("nil attempt release 必须为 no-op: %v", err)
	}
	if err := nilAttempt.CompleteSuccess(context.Background()); err != nil {
		t.Fatalf("nil attempt complete 必须为 no-op: %v", err)
	}
}

func TestAttemptReleaseIdempotent(t *testing.T) {
	gate := &fakeGate{admitted: true}
	d := Dispatcher{Client: fakeClient{response: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}}, KeyModel: gate}
	req, _ := http.NewRequest(http.MethodGet, "https://example.test", nil)
	result, err := d.Dispatch(context.Background(), Request{HTTP: req, AttemptID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := result.Attempt.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 释放后再释放必须是 no-op（settled 短路）。
	if err := result.Attempt.Release(context.Background()); err != nil {
		t.Fatalf("重复 release 必须为 no-op: %v", err)
	}
	if gate.released != 1 {
		t.Fatalf("released=%d", gate.released)
	}
	// settled 后 Unknown 返回 ErrAttemptSettled。
	if err := result.Attempt.Unknown(context.Background(), time.UnixMilli(1), "req"); !errors.Is(err, ErrAttemptSettled) {
		t.Fatalf("settled unknown err=%v", err)
	}
}

func TestCompleteSuccessAggregatesBothErrors(t *testing.T) {
	// 契约：framing 与 release 任一失败都不能吞掉另一个操作的执行。
	attempt := &Attempt{gate: &fakeGate{admitted: true}, circuit: wkErrAttempt{}}
	err := attempt.CompleteSuccess(context.Background())
	if err == nil || !strings.Contains(err.Error(), "framing boom") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("CompleteSuccess 错误聚合不符: %v", err)
	}
}

func TestResultForwardingWithNilAttempt(t *testing.T) {
	var result Result
	if err := result.ReportFramingComplete(context.Background()); err != nil {
		t.Fatalf("nil attempt framing: %v", err)
	}
	if err := result.CompleteSuccess(context.Background()); err != nil {
		t.Fatalf("nil attempt success: %v", err)
	}
	if err := result.ReportTransportFailure(context.Background(), errors.New("x")); err != nil {
		t.Fatalf("nil attempt transport: %v", err)
	}
	if err := result.ReportUnknown(context.Background()); err != nil {
		t.Fatalf("nil attempt unknown: %v", err)
	}
	if err := (Result{Attempt: &Attempt{circuit: wkErrAttempt{}}}).ReportTransportFailure(context.Background(), nil); err == nil {
		t.Fatal("transport failure 上报必须转发 circuit")
	}
	if err := (Result{Attempt: &Attempt{circuit: wkErrAttempt{}}}).ReportUnknown(context.Background()); err == nil {
		t.Fatal("unknown 上报必须转发 circuit")
	}
}

func TestReadBodySemantics(t *testing.T) {
	if _, err := ReadBody(nil, 10); err == nil {
		t.Fatal("nil response 必须失败")
	}
	if _, err := ReadBody(&http.Response{}, 10); err == nil {
		t.Fatal("nil body 必须失败")
	}
	// limit<=0 使用默认上限。
	body := &fakeReadCloser{reader: strings.NewReader("hello")}
	data, err := ReadBody(&http.Response{Body: body}, 0)
	if err != nil || string(data) != "hello" || !body.closed {
		t.Fatalf("默认 limit 读取: %q %v closed=%v", data, err, body.closed)
	}
	// 超限必须报错并关闭 body。
	oversized := &fakeReadCloser{reader: strings.NewReader(strings.Repeat("x", 64))}
	if _, err := ReadBody(&http.Response{Body: oversized}, 8); err == nil || !strings.Contains(err.Error(), "exceeds 8 bytes") {
		t.Fatalf("超限错误不符: %v", err)
	}
	if !oversized.closed {
		t.Fatal("超限读取未关闭 body")
	}
	// 读取错误被保留。
	broken := &fakeReadCloser{err: errors.New("socket reset")}
	if _, err := ReadBody(&http.Response{Body: broken}, 10); err == nil || !strings.Contains(err.Error(), "socket reset") {
		t.Fatalf("读取错误未保留: %v", err)
	}
}

type fakeReadCloser struct {
	reader   io.Reader
	err      error
	closeErr error
	closed   bool
}

func (f *fakeReadCloser) Read(p []byte) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	return f.reader.Read(p)
}
func (f *fakeReadCloser) Close() error { f.closed = true; return f.closeErr }

func TestCloseResponsePreservesErrors(t *testing.T) {
	// CloseResponse 必须同时保留 body 关闭错误与 permit 释放错误。
	body := &fakeReadCloser{reader: strings.NewReader("x"), closeErr: errors.New("close boom")}
	attempt := &Attempt{gate: &wkErrReleaseGate{}}
	result := Result{Response: &http.Response{Body: body}, Attempt: attempt}
	err := result.CloseResponse()
	if err == nil || !strings.Contains(err.Error(), "close boom") || !strings.Contains(err.Error(), "release boom") {
		t.Fatalf("CloseResponse 错误聚合不符: %v", err)
	}
}

type wkErrReleaseGate struct{}

func (wkErrReleaseGate) AdmitForeground(context.Context, keymodelruntime.Capability, string) (keymodelruntime.ForegroundDecision, keymodelruntime.ForegroundPermit, uint64, error) {
	return keymodelruntime.ForegroundAdmitted, keymodelruntime.ForegroundPermit{}, 0, nil
}
func (wkErrReleaseGate) ReleaseForeground(context.Context, keymodelruntime.ForegroundPermit) (bool, error) {
	return false, errors.New("release boom")
}
func (wkErrReleaseGate) RenewForeground(context.Context, keymodelruntime.ForegroundPermit) (keymodelruntime.ForegroundPermit, bool, error) {
	return keymodelruntime.ForegroundPermit{}, false, nil
}
func (wkErrReleaseGate) RecordFailureIntent(context.Context, keymodelruntime.FailureIntent) (keymodelruntime.MutationStatus, keymodelruntime.State, error) {
	return keymodelruntime.StatusApplied, keymodelruntime.State{}, nil
}
