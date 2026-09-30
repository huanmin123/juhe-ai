// custom_model_builtin_overlap_test.go pins the 2026-09-30 contract "自定义
// 模型不得与内置模型同名（内置权威优先）"（docs/functions/自定义模型与模型映射
// 设计.md 第 4 节）：创建与整体保存（upsert 全路径）拒绝与同供应商运行时可见
// 内置目录行同名的 model，存储层 patch 改名分支同样拒绝；shutdown 到期、
// 隐藏、disabled 的内置行不属于运行时可见口径，不拦截；不同名创建与同字段
// 编辑不受影响。
package providers

import (
	"context"
	"net/http"
	"testing"
)

// TestCustomModelBuiltinOverlapCreateRejection covers POST /{code}/models:
// the 400 with the contract message on a runtime-visible built-in name, and
// the create-through for non-visible built-in names and fresh names.
func TestCustomModelBuiltinOverlapCreateRejection(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.login(t, "user1", "user-pass", "user")

	// Runtime-visible built-in rows (cat-1 gpt-4o, cat-2 gpt-4o-mini) reject.
	code, rejected := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models",
		`{"model":"gpt-4o","inputUsdPer1M":1,"outputUsdPer1M":2}`)
	if code != http.StatusBadRequest || rejected["message"] != customModelBuiltInNameConflictMessage {
		t.Fatalf("visible built-in overlap: %d %v", code, rejected)
	}
	code, rejectedGlobal := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models",
		`{"model":"gpt-4o-mini","inputUsdPer1M":1,"outputUsdPer1M":2}`)
	if code != http.StatusBadRequest || rejectedGlobal["message"] != customModelBuiltInNameConflictMessage {
		t.Fatalf("visible built-in overlap (second row): %d %v", code, rejectedGlobal)
	}

	// Non-visible built-in rows fall outside the runtime visibility predicate
	// and stay creatable: shutdown-expired (cat-7), hidden (cat-8), disabled
	// (cat-3).
	for _, model := range []string{"gpt-4o-expired", "gpt-4o-hidden", "gpt-4-secret"} {
		code, created := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models",
			`{"model":"`+model+`","inputUsdPer1M":1,"outputUsdPer1M":2}`)
		if code != http.StatusCreated {
			t.Fatalf("non-visible built-in name %s must create: %d %v", model, code, created)
		}
	}

	// A fresh name still creates.
	code, fresh := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models",
		`{"model":"my-own-overlap-free-model","inputUsdPer1M":1,"outputUsdPer1M":2}`)
	if code != http.StatusCreated {
		t.Fatalf("fresh name create: %d %v", code, fresh)
	}
}

// TestCustomModelBuiltinOverlapUpsertStorePath pins the store-level upsert
// contract: the guard fires on the full save path (create and same-row save)
// before any write.
func TestCustomModelBuiltinOverlapUpsertStorePath(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	store := env.providersDeps.Store
	ctx := context.Background()

	if _, err := store.upsertCustomProviderModel(ctx, customProviderModelUpsertInput{
		ProviderCode: "gpt", Model: "gpt-4o", Scope: catalogScopeGlobal,
		InputUsdPer1M: ptrFloat64(1), ActorSystemAccountID: "sys_admin",
	}); err == nil || err.Error() != customModelBuiltInNameConflictMessage {
		t.Fatalf("upsert visible built-in overlap: %v", err)
	}
	// A same-provider different name passes the guard (the write itself is
	// covered by the wider write suite).
	if _, err := store.upsertCustomProviderModel(ctx, customProviderModelUpsertInput{
		ProviderCode: "gpt", Model: "store-own-model", Scope: catalogScopeGlobal,
		InputUsdPer1M: ptrFloat64(1), ActorSystemAccountID: "sys_admin",
	}); err != nil {
		t.Fatalf("upsert non-overlap name: %v", err)
	}
	// Another provider's built-in name does not collide (same-name guard is
	// per provider).
	if _, err := store.upsertCustomProviderModel(ctx, customProviderModelUpsertInput{
		ProviderCode: "anthropic", Model: "gpt-4o", Scope: catalogScopeGlobal,
		InputUsdPer1M: ptrFloat64(1), ActorSystemAccountID: "sys_admin",
	}); err != nil {
		t.Fatalf("cross-provider name is not an overlap: %v", err)
	}
}

// TestCustomModelBuiltinOverlapPatchRenameGuard pins the store patch branch:
// a submitted rename onto a runtime-visible built-in name is rejected, while
// editing the same row without renaming (the only HTTP-reachable shape) stays
// untouched.
func TestCustomModelBuiltinOverlapPatchRenameGuard(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	store := env.providersDeps.Store
	ctx := context.Background()
	current, err := store.upsertCustomProviderModel(ctx, customProviderModelUpsertInput{
		ProviderCode: "gpt", Model: "patch-own-model", Scope: catalogScopeGlobal,
		InputUsdPer1M: ptrFloat64(1), ActorSystemAccountID: "sys_admin",
	})
	if err != nil {
		t.Fatalf("seed upsert: %v", err)
	}

	// Rename onto a visible built-in name is rejected before any write.
	if _, err := store.patchCustomProviderModel(ctx, current,
		customProviderModelUpsertInput{
			Model: "gpt-4o", ActorSystemAccountID: "sys_admin", Status: "disabled",
		}, []string{"status"}, current.UpdatedAt, "", nil); err == nil ||
		err.Error() != customModelBuiltInNameConflictMessage {
		t.Fatalf("patch rename onto built-in: %v", err)
	}
	// Rename onto a non-visible built-in name (shutdown-expired cat-7) passes
	// the guard and updates normally.
	outcome, err := store.patchCustomProviderModel(ctx, current,
		customProviderModelUpsertInput{
			Model: "gpt-4o-expired", ActorSystemAccountID: "sys_admin", Status: "disabled",
		}, []string{"status"}, current.UpdatedAt, "", nil)
	if err != nil || outcome.Kind != "updated" {
		t.Fatalf("patch rename onto non-visible built-in: %v %+v", err, outcome)
	}
}

// TestCustomModelBuiltinOverlapSameNameEditUnaffected covers the HTTP PATCH
// flow: editing own custom model fields without a model key answers 200 and
// never consults the built-in catalog.
func TestCustomModelBuiltinOverlapSameNameEditUnaffected(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.requireAccount(t, "user1", "user-pass", "user")
	env.login(t, "user1", "user-pass", "user")

	code, created := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models",
		`{"model":"my-editable-model","inputUsdPer1M":1,"outputUsdPer1M":2,"notes":"before"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	createdID := dataMap(t, created)["id"].(string)
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/"+createdID,
		`{"expectedUpdatedAt":"`+env.updatedAtOf(t, createdID)+`","notes":"after"}`)
	if code != http.StatusOK {
		t.Fatalf("same-name edit: %d %v", code, patched)
	}
	if dataMap(t, patched)["model"] != "my-editable-model" {
		t.Fatalf("edit must not rename: %v", patched)
	}
}
