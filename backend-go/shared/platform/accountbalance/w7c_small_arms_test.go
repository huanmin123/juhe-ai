package accountbalance

// w7c small direct arms: jsonPointer escapes, eligibility manual rejection,
// ToInput failure path, OpenStore URL validation, and the LiteLLM unsupported
// diagnostic.

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestW7CSmallDirectArms(t *testing.T) {
	// jsonPointer rejects malformed escape sequences.
	if _, err := ParseCustom(w7cJSONValue(t, `{"a":1}`), "/a~x", "", "", ""); err == nil {
		t.Fatal("malformed escape must fail")
	}
	// candidateEligible rejects manual triggers outright.
	store := w7cNewSQLiteStore(t)
	candidate := w7cBalanceCandidate(t, store, "w7c-direct", nil)
	if err := candidateEligible(candidate, TriggerManual); err == nil || !strings.Contains(err.Error(), "manual 不接受") {
		t.Fatalf("manual eligibility: %v", err)
	}
	// An eligible candidate without a Base URL fails inside ToInput.
	noBase := w7cBalanceCandidate(t, store, "w7c-nobase", func(c *Candidate) { c.BaseURL = " " })
	report, err := newRunnerForTest(store).RunPeriodic(context.Background(), []Candidate{noBase})
	if err != nil || len(report.Errors) != 1 || report.Executed != 0 {
		t.Fatalf("ToInput failure path: %#v %v", report, err)
	}
	// NewService surfaces OpenStore validation failures.
	if _, err := NewService(RuntimeConfig{
		Enabled: true, OwnerID: "w7c", CredentialSecret: "s",
		Store: StoreConfig{Mode: StoreMode("weird")},
	}, nil); err == nil || !strings.Contains(err.Error(), "sqlite 或 postgres") {
		t.Fatalf("service store mode: %v", err)
	}
}

func newRunnerForTest(store *Store) *Runner {
	runner, err := NewRunner(RunnerConfig{
		Store: store, OwnerID: "w7c-direct-runner", CredentialSecret: "w7c-balance-secret",
		HTTPClient: &w7cScriptedHTTP{behavior: w7cFreshBehavior}, MaxConcurrent: 2,
	})
	if err != nil {
		panic(err)
	}
	return runner
}

func TestW7CQueryAdapterLiteLLMUnsupportedDiagnostic(t *testing.T) {
	client := &w7cScriptedHTTP{behavior: func(*http.Request) (int, string) {
		return http.StatusOK, `{"info":{}}`
	}}
	requester := &balanceRequester{
		ctx: context.Background(), input: w7cQueryInput(t, "https://example.test", nil),
		key: "k", doer: client, maxBytes: defaultMaxBodyBytes,
	}
	if _, diagnostic := requester.queryAdapter(AdapterLiteLLM); diagnostic == nil || diagnostic.code != "unsupported" {
		t.Fatalf("litellm unsupported diagnostic: %#v", diagnostic)
	}
}
