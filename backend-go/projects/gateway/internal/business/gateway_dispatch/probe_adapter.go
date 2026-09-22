package gatewaydispatch

import (
	"context"
	"net/http"
	"sync"
	"time"

	keymodelruntime "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/key_model_runtime"
)

// ProbeAdapter exposes Dispatcher through modelcheckprobe's transport port
// without importing the probe package or introducing a package cycle.
type ProbeAdapter struct {
	Dispatcher *Dispatcher
}

func (a ProbeAdapter) Dispatch(ctx context.Context, request *http.Request, capability keymodelruntime.Capability, attemptID string) (*http.Response, func(bool), error) {
	return a.dispatch(ctx, request, capability, attemptID, nil)
}

// DispatchWithClient preserves the model-check target's resolved HTTP client
// for this attempt while retaining the legacy DispatcherPort method above.
func (a ProbeAdapter) DispatchWithClient(ctx context.Context, request *http.Request, capability keymodelruntime.Capability, attemptID string, client *http.Client) (*http.Response, func(bool), error) {
	return a.dispatch(ctx, request, capability, attemptID, client)
}

func (a ProbeAdapter) dispatch(ctx context.Context, request *http.Request, capability keymodelruntime.Capability, attemptID string, client *http.Client) (*http.Response, func(bool), error) {
	if a.Dispatcher == nil {
		return nil, nil, ErrClientRequired
	}
	circuitInput := &AccountCircuitInput{
		AccountID:                 capability.CredentialSourceAccountID,
		RequestLane:               capability.ClientEndpointFamily,
		Model:                     capability.FinalUpstreamModel,
		DispatchRevision:          capability.DispatchRevision,
		ConfirmationLeaseDuration: time.Minute,
		ConfirmationEligible:      true,
		FailureEvidenceKey:        attemptID,
	}
	// A nil *http.Client must stay a nil interface here: wrapping it would
	// create a typed-nil Client that passes Dispatcher.Dispatch's nil check
	// and panics on Do. The nil check below is what lets Dispatch fall back
	// to its configured default client.
	input := Request{HTTP: request, Capability: capability, AttemptID: attemptID, AccountCircuit: circuitInput}
	if client != nil {
		input.Client = client
	}
	result, err := a.Dispatcher.Dispatch(ctx, input)
	if err != nil {
		return result.Response, func(bool) {}, err
	}
	var once sync.Once
	settle := func(success bool) {
		once.Do(func() {
			if success {
				_ = result.CompleteSuccess(context.Background())
				return
			}
			_ = result.ReportUnknown(context.Background())
			_ = result.Attempt.Release(context.Background())
		})
	}
	return result.Response, settle, nil
}
