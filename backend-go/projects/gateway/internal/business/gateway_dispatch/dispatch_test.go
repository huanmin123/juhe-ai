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

type fakeGate struct {
	admitted          bool
	released, unknown int
}

func (f *fakeGate) AdmitForeground(context.Context, keymodelruntime.Capability, string) (keymodelruntime.ForegroundDecision, keymodelruntime.ForegroundPermit, uint64, error) {
	if !f.admitted {
		return keymodelruntime.ForegroundBlocked, keymodelruntime.ForegroundPermit{}, 0, nil
	}
	return keymodelruntime.ForegroundAdmitted, keymodelruntime.ForegroundPermit{CapabilityHash: "hash", AttemptID: "attempt"}, 0, nil
}
func (f *fakeGate) ReleaseForeground(context.Context, keymodelruntime.ForegroundPermit) (bool, error) {
	f.released++
	return true, nil
}
func (f *fakeGate) RenewForeground(context.Context, keymodelruntime.ForegroundPermit) (keymodelruntime.ForegroundPermit, bool, error) {
	return keymodelruntime.ForegroundPermit{CapabilityHash: "hash", AttemptID: "attempt"}, true, nil
}
func (f *fakeGate) RecordFailureIntent(context.Context, keymodelruntime.FailureIntent) (keymodelruntime.MutationStatus, keymodelruntime.State, error) {
	f.unknown++
	return keymodelruntime.StatusApplied, keymodelruntime.State{}, nil
}

type fakeClient struct {
	response *http.Response
	err      error
}

func (f fakeClient) Do(*http.Request) (*http.Response, error) { return f.response, f.err }

type fakeCircuitAttempt struct {
	framing, transport, unknown int
}

func (f *fakeCircuitAttempt) ReportFramingComplete(context.Context) error { f.framing++; return nil }
func (f *fakeCircuitAttempt) ReportTransportFailure(context.Context, error) error {
	f.transport++
	return nil
}
func (f *fakeCircuitAttempt) ReportUnknown(context.Context) error { f.unknown++; return nil }

type fakeCircuitGate struct {
	decision AccountCircuitDecision
	attempt  *fakeCircuitAttempt
}

func (f *fakeCircuitGate) Prepare(context.Context, AccountCircuitInput) (AccountCircuitDecision, AccountCircuitAttempt, error) {
	if f.decision == "" {
		f.decision = AccountCircuitDispatchable
	}
	if f.attempt == nil {
		f.attempt = &fakeCircuitAttempt{}
	}
	return f.decision, f.attempt, nil
}

func dispatchCapability() keymodelruntime.Capability {
	return keymodelruntime.Capability{CredentialSourceAccountID: "a", KeyFingerprint: "k", ClientModel: "m", ClientEndpointFamily: "chat_completions", FinalUpstreamModel: "m", UpstreamEndpointMode: "chat_json", DispatchRevision: 1}
}

func TestDispatchAdmitsAndReleasesBody(t *testing.T) {
	gate := &fakeGate{admitted: true}
	d := Dispatcher{Client: fakeClient{response: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}}, KeyModel: gate}
	req, _ := http.NewRequest(http.MethodGet, "https://example.test", nil)
	result, err := d.Dispatch(context.Background(), Request{HTTP: req, Capability: dispatchCapability(), AttemptID: "attempt"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadBody(result.Response, 1024); err != nil {
		t.Fatal(err)
	}
	if err := result.CompleteSuccess(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gate.released != 1 {
		t.Fatalf("released=%d", gate.released)
	}
}

func TestDispatchTransportReportsUnknown(t *testing.T) {
	gate := &fakeGate{admitted: true}
	d := Dispatcher{Client: fakeClient{err: errors.New("timeout")}, KeyModel: gate}
	req, _ := http.NewRequest(http.MethodGet, "https://example.test", nil)
	_, err := d.Dispatch(context.Background(), Request{HTTP: req, Capability: dispatchCapability(), AttemptID: "attempt"})
	if err == nil || gate.unknown != 1 {
		t.Fatalf("err=%v unknown=%d", err, gate.unknown)
	}
	_ = time.Now()
}

func TestDispatchCircuitLifecycle(t *testing.T) {
	gate := &fakeGate{admitted: true}
	circuit := &fakeCircuitGate{}
	d := Dispatcher{Client: fakeClient{response: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}}, KeyModel: gate, Circuit: circuit}
	req, _ := http.NewRequest(http.MethodGet, "https://example.test", nil)
	result, err := d.Dispatch(context.Background(), Request{
		HTTP: req, Capability: dispatchCapability(), AttemptID: "attempt",
		AccountCircuit: &AccountCircuitInput{AccountID: "a", RequestLane: "text", Model: "m", DispatchRevision: 1, ConfirmationLeaseDuration: time.Minute, ConfirmationEligible: true, FailureEvidenceKey: "evidence"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := result.ReportFramingComplete(context.Background()); err != nil {
		t.Fatal(err)
	}
	if circuit.attempt.framing != 1 || circuit.attempt.transport != 0 || circuit.attempt.unknown != 0 {
		t.Fatalf("circuit=%+v", circuit.attempt)
	}
}

func TestDispatchTransportFailureSettlesCircuit(t *testing.T) {
	gate := &fakeGate{admitted: true}
	circuit := &fakeCircuitGate{}
	d := Dispatcher{Client: fakeClient{err: errors.New("timeout")}, KeyModel: gate, Circuit: circuit}
	req, _ := http.NewRequest(http.MethodGet, "https://example.test", nil)
	_, err := d.Dispatch(context.Background(), Request{
		HTTP: req, Capability: dispatchCapability(), AttemptID: "attempt",
		AccountCircuit: &AccountCircuitInput{AccountID: "a", RequestLane: "text", Model: "m", DispatchRevision: 1, ConfirmationLeaseDuration: time.Minute, ConfirmationEligible: true, FailureEvidenceKey: "evidence"},
	})
	if err == nil {
		t.Fatal("expected transport error")
	}
	if circuit.attempt.transport != 1 || circuit.attempt.unknown != 0 {
		t.Fatalf("circuit=%+v", circuit.attempt)
	}
}

func TestDispatchCircuitBlockPreventsKeyModelAdmission(t *testing.T) {
	gate := &fakeGate{admitted: true}
	circuit := &fakeCircuitGate{decision: AccountCircuitBlocked}
	d := Dispatcher{Client: fakeClient{}, KeyModel: gate, Circuit: circuit}
	req, _ := http.NewRequest(http.MethodGet, "https://example.test", nil)
	_, err := d.Dispatch(context.Background(), Request{HTTP: req, Capability: dispatchCapability(), AttemptID: "attempt", AccountCircuit: &AccountCircuitInput{AccountID: "a"}})
	if !errors.Is(err, ErrAccountCircuitBlocked) {
		t.Fatalf("err=%v", err)
	}
	if gate.unknown != 0 || gate.released != 0 {
		t.Fatalf("key model gate unexpectedly mutated: %+v", gate)
	}
}
