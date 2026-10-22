// w14d_http_branches_test.go pins the previously uncovered HTTP branches of
// the write family: 400 body edges, the optimistic concurrency 409s, the
// capability validation 400 on the built-in fork, the delete guard and the
// dropped-table 500 arms.
//
// 不可达分支登记（w14d）：patchModel/deleteModel 的 403 canMutateCustomModel
// 分支不可达——非管理员查询带 personal-owner 谓词，只能取回自己的行（此时
// canMutate 为真），管理员则恒过 admin 分支；两处守卫保留但无 HTTP 路径。
package providers

import (
	"net/http"
	"strings"
	"testing"
)

// w14dSeedPersonalModel seeds a personal custom model owned by the dedicated
// w14d-owner account (the real system account id is used so the owner
// predicate matches).
func (env *testEnv) w14dSeedPersonalModel(t *testing.T, id, model string) string {
	t.Helper()
	ownerID := env.requireAccount(t, "w14d-owner", "owner-pass", "user")
	env.seedCustomModel(t, customModelSeed{ID: id, ProviderCode: "gpt", Model: model,
		Scope: "personal", SystemAccountID: &ownerID})
	return ownerID
}

func TestW14dPatchModelOwnershipVisibility(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.w14dSeedPersonalModel(t, "custom_model_w14d-f1", "forbidden-model")
	env.requireAccount(t, "user2", "user-pass", "user")
	env.login(t, "user2", "user-pass", "user")

	// Malformed JSON body on PATCH.
	code, bad := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/custom_model_w14d-f1", "{nope")
	if code != http.StatusBadRequest {
		t.Fatalf("bad patch body: %d %v", code, bad)
	}

	// Another user's personal model is invisible for PATCH and DELETE.
	code, invisible := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/custom_model_w14d-f1",
		`{"status":"disabled","expectedUpdatedAt":"2026-01-01T00:00:00.000Z"}`)
	if code != http.StatusNotFound || invisible["message"] != "自定义模型不存在" {
		t.Fatalf("foreign patch: %d %v", code, invisible)
	}
	code, deleteInvisible := env.do(t, http.MethodDelete, "/__aisys__/api/providers/gpt/models/custom_model_w14d-f1", "")
	if code != http.StatusNotFound {
		t.Fatalf("foreign delete: %d %v", code, deleteInvisible)
	}
}

func TestW14dPatchModelConflictAfterExternalWrite(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.w14dSeedPersonalModel(t, "custom_model_w14d-c1", "conflict-model")
	env.login(t, "w14d-owner", "owner-pass", "user")

	updatedAt := env.updatedAtOf(t, "custom_model_w14d-c1")
	env.exec(t, `UPDATE models SET updated_at = '2020-06-06T00:00:00.000Z' WHERE id = 'custom_model_w14d-c1'`)
	code, conflict := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/custom_model_w14d-c1",
		`{"status":"disabled","expectedUpdatedAt":"`+updatedAt+`"}`)
	if code != http.StatusConflict {
		t.Fatalf("stale custom patch: %d %v", code, conflict)
	}

	// A custom row seeded with an empty status still patches (the fallback
	// keeps the current status when the patch omits one).
	env.exec(t, `UPDATE models SET status = '' WHERE id = 'custom_model_w14d-c1'`)
	env.exec(t, `UPDATE models SET updated_at = ? WHERE id = 'custom_model_w14d-c1'`, updatedAt)
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/custom_model_w14d-c1",
		`{"contextWindowTokens":777,"expectedUpdatedAt":"`+updatedAt+`"}`)
	if code != http.StatusOK {
		t.Fatalf("empty-status patch: %d %v", code, patched)
	}
}

func TestW14dPatchBuiltInValidationAndConflict(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.seedWriteFixtures(t)
	env.login(t, "root", "root-pass", "super_admin")

	// Capability validation failure on the built-in fork.
	updatedAt := env.builtinUpdatedAtOf(t, "cat-2")
	code, invalid := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/cat-2",
		`{"mode":"image","supportedServiceTiers":["priority"],"expectedUpdatedAt":"`+updatedAt+`"}`)
	if code != http.StatusBadRequest || invalid["message"] != "只有文本自定义模型支持服务等级和思考能力配置" {
		t.Fatalf("built-in validation: %d %v", code, invalid)
	}

	// A row moved behind the caller renders the 409 conflict.
	env.exec(t, `UPDATE models SET updated_at = '2020-06-06T00:00:00.000Z' WHERE id = 'cat-2'`)
	code, conflict := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/cat-2",
		`{"mode":"text","expectedUpdatedAt":"`+updatedAt+`"}`)
	if code != http.StatusConflict {
		t.Fatalf("built-in conflict: %d %v", code, conflict)
	}
}

func TestW14dDeleteModelBindingGuardAndSuccess(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	ownerID := env.w14dSeedPersonalModel(t, "custom_model_w14d-d1", "delete-guard-model")
	env.login(t, "w14d-owner", "owner-pass", "user")

	// A bound AI account blocks the delete with the detailed message. The
	// binding summary joins live accounts on the owner, so the seeded account
	// carries the same system account id.
	env.exec(t, `INSERT INTO accounts (id, name, system_account_id) VALUES ('w14d-acc-del', 'acc', ?)`, ownerID)
	env.exec(t, `INSERT INTO account_supported_models (account_id, model_id)
		SELECT 'w14d-acc-del', id FROM models WHERE id = 'custom_model_w14d-d1'`)
	code, guarded := env.do(t, http.MethodDelete, "/__aisys__/api/providers/gpt/models/custom_model_w14d-d1", "")
	if code != http.StatusConflict || !strings.Contains(guarded["message"].(string), "个账户支持模型") {
		t.Fatalf("bound delete: %d %v", code, guarded)
	}

	// Without bindings the delete succeeds.
	env.exec(t, `DELETE FROM account_supported_models WHERE account_id = 'w14d-acc-del'`)
	code, deleted := env.do(t, http.MethodDelete, "/__aisys__/api/providers/gpt/models/custom_model_w14d-d1", "")
	if code != http.StatusOK || dataMap(t, deleted)["deleted"] != true {
		t.Fatalf("free delete: %d %v", code, deleted)
	}
}

func TestW14dCreateModelTemplateCatalogError(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.seedWriteFixtures(t)
	env.login(t, "user1", "user-pass", "user")

	// The template lookup fails when the built-in catalog table is gone.
	env.exec(t, `DROP TABLE models`)
	code, failed := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models",
		`{"model":"from-template","configurationTemplateId":"cat-2","inputUsdPer1M":1}`)
	if code != http.StatusInternalServerError || failed["message"] != "服务器内部错误" {
		t.Fatalf("template catalog error: %d %v", code, failed)
	}
}

func TestW14dReadFamilyServerErrorArms(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.seedWriteFixtures(t)
	env.login(t, "user1", "user-pass", "user")

	// Models list: dropping the built-in catalog surfaces the 500 arm.
	env.exec(t, `DROP TABLE models`)
	code, listed := env.do(t, http.MethodGet, "/__aisys__/api/providers/gpt/models", "")
	if code != http.StatusInternalServerError {
		t.Fatalf("models 500: %d %v", code, listed)
	}

	// Capabilities: the same drop surfaces the 500 arm after the definition
	// read.
	code, caps := env.do(t, http.MethodGet, "/__aisys__/api/providers/gpt/models/gpt-4o/capabilities", "")
	if code != http.StatusInternalServerError {
		t.Fatalf("capabilities 500: %d %v", code, caps)
	}
}
