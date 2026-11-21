package authz

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// MutationGuard fingerprints must include the recipient-consent fields. The
// old granteeId-only fingerprint collapsed two different grantees into one
// dedupe key whenever the resource matched. The guard owns body parsing, so
// drive the real middleware rather than BodyField directly.
func TestCreateFingerprintDistinguishesRecipientFields(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	cases := []struct {
		name string
		a    string
		b    string
	}{
		{
			name: "granteeUsername",
			a:    `{"resourceType":"account","resourceId":"acc_1","granteeType":"system_account","granteeUsername":"alice"}`,
			b:    `{"resourceType":"account","resourceId":"acc_1","granteeType":"system_account","granteeUsername":"bob"}`,
		},
		{
			name: "teamName",
			a:    `{"resourceType":"account","resourceId":"acc_1","granteeType":"team","teamName":"alpha"}`,
			b:    `{"resourceType":"account","resourceId":"acc_1","granteeType":"team","teamName":"beta"}`,
		},
		{
			name: "adminGranteeSystemAccountId",
			a:    `{"resourceType":"account","resourceId":"acc_1","granteeType":"system_account","granteeSystemAccountId":"sys_a"}`,
			b:    `{"resourceType":"account","resourceId":"acc_1","granteeType":"system_account","granteeSystemAccountId":"sys_b"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			handler := kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
				OperationKey: "authorizations.create",
				Store:        kernel.NewDeduplicationStore(func() time.Time { return now }),
				SucceededTTL: time.Minute,
				Fingerprint: func(r *http.Request) (any, error) {
					return createFingerprint(r, "owner")
				},
			})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.WriteHeader(http.StatusCreated)
			}))
			post := func(body string) int {
				t.Helper()
				request := httptest.NewRequest(http.MethodPost, "/__aisys__/api/my-authorizations", strings.NewReader(body))
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Content-Length", strconv.Itoa(len(body)))
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, request)
				return recorder.Code
			}
			if got := post(tc.a); got != http.StatusCreated {
				t.Fatalf("first body = %d, want 201", got)
			}
			if got := post(tc.b); got != http.StatusCreated {
				t.Fatalf("different %s = %d, want 201 (same fingerprint would be 409)", tc.name, got)
			}
			if calls != 2 {
				t.Fatalf("handler calls = %d, want 2", calls)
			}
		})
	}
}
