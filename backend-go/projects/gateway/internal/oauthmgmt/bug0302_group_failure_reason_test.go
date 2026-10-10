// bug0302_group_failure_reason_test.go pins BUG-0302: the group-query 500
// arms (handleCreate / ssoToOAuth) must hand groupErr — not the always-nil
// outer err — to kernel.WriteErrorCause, so the http_request_completed
// completion log keeps a failureReason that names the original cause.
package oauthmgmt

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// bug0302Event records one kernel request lifecycle event.
type bug0302Event struct {
	level   string
	fields  map[string]any
	message string
}

// bug0302EventSink is the recording kernel.RequestEventSink mirror of the
// kernel package's own recordingSink test double (ctxobservability_test.go).
type bug0302EventSink struct {
	mu     sync.Mutex
	events []bug0302Event
}

func (s *bug0302EventSink) EmitRequestEvent(level string, fields map[string]any, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, bug0302Event{level: level, fields: fields, message: message})
}

// completedFailureReason returns the failureReason field of the
// http_request_completed record for path.
func (s *bug0302EventSink) completedFailureReason(t *testing.T, path string) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, event := range s.events {
		if event.fields["event"] == "http_request_completed" && event.fields["path"] == path {
			reason, _ := event.fields["failureReason"].(string)
			return reason
		}
	}
	t.Fatalf("no http_request_completed event for %s among %d events", path, len(s.events))
	return ""
}

// installBUG0302Sink wires the recording sink. The sink is a kernel-level
// global with no getter, so cleanup restores the package baseline (no sink
// wired, the resetObservability pattern); the package runs without
// t.Parallel.
func installBUG0302Sink(t *testing.T) *bug0302EventSink {
	t.Helper()
	sink := &bug0302EventSink{}
	kernel.SetRequestEventSink(sink)
	t.Cleanup(func() { kernel.SetRequestEventSink(nil) })
	return sink
}

// assertBUG0302GroupQuery500KeepsFailureReason drives one group-query 500 arm
// and pins the BUG-0302 contract: the client envelope stays generic while the
// completion log carries the raw SQLite cause.
func assertBUG0302GroupQuery500KeepsFailureReason(t *testing.T, path string, body string) {
	t.Helper()
	sink := installBUG0302Sink(t)
	env := newTestEnv(t)
	env.w14dLoginAdmin(t)

	// Dropping the groups table turns the group check into the 500 arm.
	env.exec(t, `DROP TABLE groups`)
	code, payload := env.do(t, http.MethodPost, path, body)
	if code != http.StatusInternalServerError || payload["message"] != "服务器内部错误" {
		t.Fatalf("group 500: %d %v", code, payload)
	}
	// The client envelope must stay generic: no raw SQLite text leaks.
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "no such table") {
		t.Fatalf("raw error leaked to the client: %s", encoded)
	}
	// BUG-0302: the arm used to pass the always-nil outer err, so the
	// completion log lost the failureReason. It must now name the cause.
	reason := sink.completedFailureReason(t, path)
	if !strings.Contains(reason, "no such table: groups") {
		t.Fatalf("failureReason must keep the raw cause, got %q", reason)
	}
}

// TestBUG0302CreateFromCodeGroupFailureReason mirrors
// TestW14dCreateGroupServerErrorArm on the create_from_code group arm
// (handleCreate, routes.go).
func TestBUG0302CreateFromCodeGroupFailureReason(t *testing.T) {
	assertBUG0302GroupQuery500KeepsFailureReason(t,
		"/__aisys__/api/openai-oauth/create-from-code",
		`{"sessionId":"s","callbackUrl":"https://cb","groupId":"grp-x","providerProtocolProfileId":"profile_gpt_openai_v1"}`)
}

// TestBUG0302GrokSSOGroupFailureReason pins the grok sso-to-oauth group arm
// (ssoToOAuth, routes.go). Request construction facts: the admin route is
// mounted as "POST /__aisys__/api/grok-oauth/sso-to-oauth" (mountProvider,
// plan.slug "grok" + plan.sso); the strict body allows ssoToken(s) plus the
// managed fields (groupId, providerProtocolProfileId); WithProxyRequired(false)
// in newTestEnv skips the proxy gate; resolveProviderProfile resolves the
// seeded profile_xai_openai_v1 (seedOAuthCatalog). The group query runs
// before any SSO transport call, so no scripted SSO steps are needed.
func TestBUG0302GrokSSOGroupFailureReason(t *testing.T) {
	assertBUG0302GroupQuery500KeepsFailureReason(t,
		"/__aisys__/api/grok-oauth/sso-to-oauth",
		`{"ssoToken":"w14d-sso-cookie","groupId":"grp-x","providerProtocolProfileId":"profile_xai_openai_v1"}`)
}
