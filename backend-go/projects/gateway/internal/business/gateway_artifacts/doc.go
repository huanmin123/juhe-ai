// Package gatewayartifacts is the reserved Gateway owner boundary for the
// gateway-artifacts transaction group (OpenAI-compatible vector-store/file
// metadata and search).
//
// The complete operation set is intentionally still outstanding. This package
// contains no fallback reader, no schema creation, and no Node bridge; callers
// must keep routing these operations to the current owner until a complete
// transaction contract and isolated SQLite/PG verification are delivered.
package gatewayartifacts

// OutstandingManifestOperations is an explicit migration gap. It must remain
// non-empty until every operation in the transaction group has an equivalent
// Gateway implementation and evidence.
var OutstandingManifestOperations = []string{
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
