package apikeys

import (
	"context"
	"net/http"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

// failingValidationInvalidator forces the post-commit validation-cache flush
// error the Node routes render as the log statusCode 500 arm
// (outcome.validationCacheError). Everything else delegates to the recorder.
type failingValidationInvalidator struct{ inner CacheInvalidator }

func (f failingValidationInvalidator) InvalidateValidation(apiKeyID, reason string, keys []string) error {
	_ = apiKeyID
	_ = reason
	_ = keys
	return context.DeadlineExceeded
}

func (f failingValidationInvalidator) InvalidateQuota(apiKeyID, reason string) {
	f.inner.InvalidateQuota(apiKeyID, reason)
}

func (f failingValidationInvalidator) InvalidateRuntime(apiKeyID, reason string) {
	f.inner.InvalidateRuntime(apiKeyID, reason)
}

// operationStatusByAction extracts the logged statusCode per api_keys action.
func operationStatusByAction(sink *recordingSink) map[string]int {
	out := map[string]int{}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, entry := range sink.entries {
		if entry.Module != "api_keys" || entry.StatusCode == nil {
			continue
		}
		out[entry.Module+"."+entry.Action] = *entry.StatusCode
	}
	return out
}

// TestAPIKeyOperationLogStatusCodes locks in the M07 handover: the api-keys
// log entries stamp the Node statusCode forms — create 201, refresh/patch
// 200, delete 204 — while the reveal entry stays without a statusCode exactly
// like Node (api-keys.routes.ts:122/191/262/319 and the reveal operationLog).
func TestAPIKeyOperationLogStatusCodes(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, adminID, "rs-default")

	code, created := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"status-key"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	id := dataMap(t, created)["id"].(string)
	revision := dataMap(t, created)["revision"].(string)

	code, _ = env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/"+id,
		`{"expectedRevision":"`+revision+`","description":"updated"}`)
	if code != http.StatusOK {
		t.Fatalf("patch: %d", code)
	}
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/api-keys/"+id+"/refresh-key", "")
	if code != http.StatusOK {
		t.Fatalf("refresh: %d", code)
	}
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/api-keys/"+id+"/secret", "")
	if code != http.StatusOK {
		t.Fatalf("reveal: %d", code)
	}
	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/api-keys/"+id, "")
	if code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}

	statuses := operationStatusByAction(env.sink)
	for action, want := range map[string]int{
		"api_keys.create":      http.StatusCreated,
		"api_keys.update":      http.StatusOK,
		"api_keys.refresh_key": http.StatusOK,
		"api_keys.delete":      http.StatusNoContent,
	} {
		if statuses[action] != want {
			t.Fatalf("%s statusCode=%d want %d (all: %v)", action, statuses[action], want, statuses)
		}
	}
	if _, revealed := statuses["api_keys.reveal_secret"]; revealed {
		t.Fatal("reveal_secret must not carry a statusCode (Node parity)")
	}

	// The delete change keeps the native boolean forms of
	// safeChange('deleted', '删除状态', false, true).
	env.sink.mu.Lock()
	var deletedChange *authsys.OperationLogChange
	for i := range env.sink.entries {
		entry := &env.sink.entries[i]
		if entry.Module == "api_keys" && entry.Action == "delete" && len(entry.Changes) > 0 {
			deletedChange = &entry.Changes[0]
		}
	}
	env.sink.mu.Unlock()
	if deletedChange == nil || deletedChange.Field != "deleted" ||
		deletedChange.BeforeValue != false || deletedChange.AfterValue != true {
		t.Fatalf("delete change drift: %+v", deletedChange)
	}
}

// TestAPIKeyValidationCacheFailureLogs500 pins the 500 arm: when the
// validation-cache flush fails after the mutation committed, the refresh and
// delete log entries carry statusCode 500 and the route surfaces the failure
// (Node outcome.validationCacheError ? 500 : ...).
func TestAPIKeyValidationCacheFailureLogs500(t *testing.T) {
	env := newTestEnv(t, func(inner CacheInvalidator) CacheInvalidator {
		return failingValidationInvalidator{inner: inner}
	})
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, adminID, "rs-default")

	code, created := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"failing-key"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	id := dataMap(t, created)["id"].(string)
	revision := dataMap(t, created)["revision"].(string)

	code, _ = env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/"+id,
		`{"expectedRevision":"`+revision+`","status":"disabled"}`)
	if code != http.StatusInternalServerError {
		t.Fatalf("patch must surface the validation cache failure: %d", code)
	}
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/api-keys/"+id+"/refresh-key", "")
	if code != http.StatusInternalServerError {
		t.Fatalf("refresh must surface the validation cache failure: %d", code)
	}
	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/api-keys/"+id, "")
	if code != http.StatusInternalServerError {
		t.Fatalf("delete must surface the validation cache failure: %d", code)
	}
	statuses := operationStatusByAction(env.sink)
	if statuses["api_keys.update"] != http.StatusInternalServerError {
		t.Fatalf("update statusCode=%d want 500", statuses["api_keys.update"])
	}
	if statuses["api_keys.refresh_key"] != http.StatusInternalServerError {
		t.Fatalf("refresh statusCode=%d want 500", statuses["api_keys.refresh_key"])
	}
	if statuses["api_keys.delete"] != http.StatusInternalServerError {
		t.Fatalf("delete statusCode=%d want 500", statuses["api_keys.delete"])
	}
}
