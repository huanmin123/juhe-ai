package delegated

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/groups"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

func TestW9CWriteGroupMutationErrorArms(t *testing.T) {
	f := newFixture(t, "juhe:groups.write")
	write := func(err error, fallback string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		f.env.deps.writeGroupMutationError(rec, err, fallback)
		return rec
	}
	if got := write(&groups.ConflictError{Message: "冲突"}, "fb"); got.Code != http.StatusConflict || !strings.Contains(got.Body.String(), "冲突") {
		t.Fatalf("conflict = %d %s", got.Code, got.Body.String())
	}
	if got := write(&groups.ValidationError{Message: "校验失败"}, "fb"); got.Code != http.StatusBadRequest {
		t.Fatalf("validation = %d %s", got.Code, got.Body.String())
	}
	if got := write(&groups.ValidationError{Message: "分组名称已存在"}, "fb"); got.Code != http.StatusConflict {
		t.Fatalf("validation duplicate = %d %s", got.Code, got.Body.String())
	}
	if got := write(errors.New("分组名称已存在"), "fb"); got.Code != http.StatusConflict {
		t.Fatalf("plain duplicate = %d %s", got.Code, got.Body.String())
	}
	// Non-CJK copy localizes to the 400 default (no echo of the raw text).
	if got := write(errors.New("boom"), "fb"); got.Code != http.StatusBadRequest {
		t.Fatalf("plain = %d %s", got.Code, got.Body.String())
	}
	if got := write(nil, "fallback 文案"); got.Code != http.StatusBadRequest || !strings.Contains(got.Body.String(), "fallback 文案") {
		t.Fatalf("nil error falls back = %d %s", got.Code, got.Body.String())
	}
}

func TestW9CWriteStrategyMutationErrorArms(t *testing.T) {
	f := newFixture(t, "juhe:route_strategies.write")
	write := func(err error, fallback string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		f.env.deps.writeStrategyMutationError(rec, err, fallback)
		return rec
	}
	version := write(&routestrategies.VersionConflictError{Message: "版本冲突", CurrentUpdatedAt: "2026-01-10T08:30:00.000Z"}, "fb")
	if version.Code != http.StatusConflict || !strings.Contains(version.Body.String(), "currentUpdatedAt") {
		t.Fatalf("version conflict = %d %s", version.Code, version.Body.String())
	}
	if got := write(&routestrategies.ConflictError{Message: "冲突"}, "fb"); got.Code != http.StatusConflict {
		t.Fatalf("conflict = %d %s", got.Code, got.Body.String())
	}
	if got := write(&routestrategies.ValidationError{Message: "校验失败"}, "fb"); got.Code != http.StatusBadRequest {
		t.Fatalf("validation = %d %s", got.Code, got.Body.String())
	}
	if got := write(&routestrategies.ValidationError{Message: "策略路由名称已存在"}, "fb"); got.Code != http.StatusConflict {
		t.Fatalf("validation duplicate = %d %s", got.Code, got.Body.String())
	}
	if got := write(nil, "fallback 文案"); got.Code != http.StatusBadRequest || !strings.Contains(got.Body.String(), "fallback 文案") {
		t.Fatalf("nil error falls back = %d %s", got.Code, got.Body.String())
	}
}

func TestW9CErrorTextAndSchedulingPolicyHelpers(t *testing.T) {
	if got := errorText(errors.New("real"), "fb"); got != "real" {
		t.Fatalf("errorText = %q", got)
	}
	if got := errorText(nil, "fb"); got != "fb" {
		t.Fatalf("errorText nil = %q", got)
	}
	if got := schedulingPolicyValue(&groupMutationInput{}); got != nil {
		t.Fatalf("no scheduling = %v", got)
	}
	policy := map[string]any{"mode": "weighted"}
	got := schedulingPolicyValue(&groupMutationInput{HasScheduling: true, SchedulingPolicy: policy})
	if gotMap, ok := got.(map[string]any); !ok || gotMap["mode"] != "weighted" {
		t.Fatalf("scheduling = %v", got)
	}
	input := &strategyMutationInput{HasNormal: true, HasHybrid: true}
	if normalConfigRaw(input) != nil || hybridConfigRaw(input) != nil {
		t.Fatal("nil configs must render nil raw")
	}
	nonNil := &strategyMutationInput{HasNormal: true, HasHybrid: true, NormalConfig: policy, HybridConfig: policy}
	if normalConfigRaw(nonNil) == nil || hybridConfigRaw(nonNil) == nil {
		t.Fatal("non-nil configs must pass through")
	}
	if (&apiKeyRevisionConflict{CurrentRevision: "r"}).Error() != apiKeyRevisionConflictMessage {
		t.Fatal("revision conflict message mismatch")
	}
}

// emptySlice reports whether the value is a nil slice (which would marshal
// as JSON null instead of []).
func emptySlice(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case []string:
		return typed == nil
	case []accounts.ModelMapping:
		return typed == nil
	}
	return false
}

func TestW9CAiAccountDTOVariants(t *testing.T) {
	item := accounts.ListItem{
		ID: "acct-1", ConfigRevision: 3, ProviderCode: "openai", Name: "n", Type: "api_key",
		Status: "active", Schedulable: true, ConcurrencyLimit: 5, Priority: 1,
	}
	dto := aiAccountDTO(item, accountModelFacts{})
	for _, key := range []string{"supportedModels", "modelMappings"} {
		if emptySlice(dto[key]) {
			t.Fatalf("%s must marshal as an empty list: %v (%T)", key, dto[key], dto[key])
		}
	}
	for _, key := range []string{"providerProtocolProfileId", "protocolCode", "protocolVersion", "tags"} {
		if _, has := dto[key]; has {
			t.Fatalf("empty optional %s must be omitted: %v", key, dto)
		}
	}
	full := item
	full.ProviderProtocolProfileID = "ppp-1"
	full.ProtocolCode = "openai"
	full.ProtocolVersion = "v1"
	full.Tags = []accounts.TagSummary{{ID: "tag-1", Name: "gold"}}
	dto = aiAccountDTO(full, accountModelFacts{
		supportedModels: []string{"gpt-5"},
	})
	if dto["providerProtocolProfileId"] != "ppp-1" || dto["protocolCode"] != "openai" || dto["protocolVersion"] != "v1" {
		t.Fatalf("protocol fields = %v", dto)
	}
	tags, ok := dto["tags"].([]map[string]any)
	if !ok || len(tags) != 1 || tags[0]["name"] != "gold" {
		t.Fatalf("tags = %v", dto["tags"])
	}
	if models, ok := dto["supportedModels"].([]string); !ok || len(models) != 1 {
		t.Fatalf("supportedModels = %v", dto["supportedModels"])
	}
}

func TestW9CRouteHandlers500OnBrokenSchema(t *testing.T) {
	f := newFixture(t, "juhe:request_limits.read", "juhe:ai_accounts.write")
	f.env.exec(`DROP TABLE system_accounts`)

	rec := httptest.NewRecorder()
	f.env.deps.getRequestLimitsSnapshot(rec, w9CRequestAs(t, http.MethodGet, Prefix+"/request-limits", "", "acc-1"))
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "服务器内部错误") {
		t.Fatalf("request-limits 500 = %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	f.env.deps.getProfile(rec, w9CRequestAs(t, http.MethodGet, Prefix+"/profile", "", "acc-1"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("profile 500 = %d %s", rec.Code, rec.Body.String())
	}

	// Broken accounts schema surfaces the listAiAccounts/patchAiAccount 500s.
	f.env.seedAiAccount("acct-1", f.accountID, "own-1", "active", "")
	f.env.exec(`DROP TABLE accounts`)
	rec = httptest.NewRecorder()
	f.env.deps.listAiAccounts(rec, w9CRequestAs(t, http.MethodGet, Prefix+"/ai-accounts", "", f.accountID))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("listAiAccounts 500 = %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	f.env.deps.patchAiAccount(rec, w9CRequestAs(t, http.MethodPatch, Prefix+"/ai-accounts/acct-1",
		`{"expectedConfigRevision":1,"name":"x"}`, f.accountID))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("patchAiAccount 500 = %d %s", rec.Code, rec.Body.String())
	}
}

func TestW9CDeleteGroup500OnBrokenSchema(t *testing.T) {
	f := newFixture(t, "juhe:groups.write", "juhe:route_strategies.write")
	f.env.seedGroup("grp-1", f.accountID, "g1", "openai", "personal", true)
	f.env.exec(`DROP TABLE groups`)
	request := httptest.NewRequest(http.MethodDelete, Prefix+"/groups/grp-1", nil)
	request = request.WithContext(withContext(request.Context(), &AccessContext{
		Token: f.env.tokenContext(f.token),
	}))
	rec := httptest.NewRecorder()
	f.env.deps.deleteGroup(rec, request)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("deleteGroup 500 = %d %s", rec.Code, rec.Body.String())
	}
}
