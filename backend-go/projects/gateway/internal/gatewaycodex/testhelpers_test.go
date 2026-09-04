package gatewaycodex

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// ---------------------------------------------------------------------------
// shared test doubles
// ---------------------------------------------------------------------------

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(start time.Time) *fakeClock { return &fakeClock{now: start} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type recordedAudit struct {
	mu        sync.Mutex
	metadata  []gatewayMetadataCall
	finalizes []gatewaypreauth.AuditFinalizeInput
}

type gatewayMetadataCall struct {
	label    string
	metadata map[string]any
}

func (a *recordedAudit) BindContext(context gatewaypreauth.AuditGatewayContext) {}

func (a *recordedAudit) AddGatewayMetadata(label string, metadata map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.metadata = append(a.metadata, gatewayMetadataCall{label: label, metadata: metadata})
}

func (a *recordedAudit) Finalize(input gatewaypreauth.AuditFinalizeInput) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.finalizes = append(a.finalizes, input)
}

func (a *recordedAudit) lastMetadata(label string) (map[string]any, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for index := len(a.metadata) - 1; index >= 0; index-- {
		if a.metadata[index].label == label {
			return a.metadata[index].metadata, true
		}
	}
	return nil, false
}

type recordedSink struct {
	mu       sync.Mutex
	failures []gatewaypreauth.FailureResponseInput
}

func (s *recordedSink) SendGatewayFailureResponse(input gatewaypreauth.FailureResponseInput) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = append(s.failures, input)
}

func (s *recordedSink) FinalizeGatewayAuthFailureAudit(req *gatewaypreauth.GatewayRequest, res gatewaypreauth.GatewayResponseWriter, auditCapture gatewaypreauth.AuditCaptureContext) {
}

func (s *recordedSink) SendAuthenticatedModelsGatewayResponse(input gatewaypreauth.ModelsResponseInput) {
}

func (s *recordedSink) SendOpenAIModelsGatewayResponse(input gatewaypreauth.ModelsResponseInput) {}

func (s *recordedSink) SendAnthropicModelsGatewayResponse(input gatewaypreauth.ModelsResponseInput) {}

func (s *recordedSink) SendGeminiModelsGatewayResponse(input gatewaypreauth.ModelsResponseInput) {}

func (s *recordedSink) lastFailure() (gatewaypreauth.FailureResponseInput, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.failures) == 0 {
		return gatewaypreauth.FailureResponseInput{}, false
	}
	return s.failures[len(s.failures)-1], true
}

type recordingLogger struct {
	mu       sync.Mutex
	warnings []string
}

func (l *recordingLogger) Warn(event string, fields map[string]any, message string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warnings = append(l.warnings, event)
}

func newTestRequest(t *testing.T, method, target string, body []byte, headers map[string]string) *gatewaypreauth.GatewayRequest {
	t.Helper()
	var request *http.Request
	if body != nil {
		request = httptest.NewRequest(method, target, bytes.NewReader(body))
	} else {
		request = httptest.NewRequest(method, target, nil)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	return gatewaypreauth.NewGatewayRequest(request)
}

func newTrackedWriter() (*httptest.ResponseRecorder, *gatewaypreauth.TrackingWriter) {
	recorder := httptest.NewRecorder()
	return recorder, gatewaypreauth.NewTrackingWriter(recorder)
}

func fixedIDGenerator(prefix string) IDGenerator {
	counter := 0
	return func() string {
		counter++
		// UUID-shaped so isSourceFenceId accepts the generated fence ids.
		return "00000000-0000-4000-8000-" + fmt.Sprintf("%011d", counter) + prefix[:1]
	}
}
