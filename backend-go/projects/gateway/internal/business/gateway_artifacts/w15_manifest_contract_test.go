package gatewayartifacts

import "testing"

// TestOutstandingManifestOperationsContract 锁定迁移缺口契约：
// gateway-artifacts 事务组的完整操作集仍未迁移，OutstandingManifestOperations
// 必须保持非空且覆盖既有的 9 个操作（包 doc 声明的 owner 边界）。在完整事务
// 契约与隔离验证交付前，任何清空该清单的改动都应被此测试拦截。
func TestOutstandingManifestOperationsContract(t *testing.T) {
	want := []string{
		"create_openai_compatible_vector_store",
		"list_openai_compatible_vector_stores",
		"get_openai_compatible_vector_store",
		"delete_openai_compatible_vector_store",
		"create_openai_compatible_vector_store_file",
		"list_openai_compatible_vector_store_files",
		"get_openai_compatible_vector_store_file",
		"search_openai_compatible_vector_store",
		"list_openai_compatible_vector_store_file_chunks",
	}
	if len(OutstandingManifestOperations) == 0 {
		t.Fatal("OutstandingManifestOperations 在迁移完成前必须保持非空")
	}
	if len(OutstandingManifestOperations) != len(want) {
		t.Fatalf("outstanding 操作数=%d want %d（迁移推进后需同步更新本契约）", len(OutstandingManifestOperations), len(want))
	}
	for i, op := range want {
		if OutstandingManifestOperations[i] != op {
			t.Fatalf("outstanding[%d]=%q want %q", i, OutstandingManifestOperations[i], op)
		}
	}
}
