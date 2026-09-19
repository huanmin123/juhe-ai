// Package openaicompat is the composition facade of the openai-compatible
// vertical slice (port of the five Node openai-compatible modules plus the
// two storage repositories). Since REFACTOR-0008 the implementation lives in
// two domain sub-packages with a neutral core; this root keeps only the
// package documentation and the domain map:
//
//	openaicompatcore     (shared/platform)  neutral vocabulary: config, scope,
//	                                            error envelope, parse/time primitives
//	openaicompatbridge   cross-protocol conversion domain: request-body build
//	                                            matrix, response / SSE transform
//	                                            matrix, stream state machines,
//	                                            hosted-tool executor vocabulary
//	openaicompatstorage  storage / REST endpoint domain: files + vector-stores
//	                                            stores and route families, file
//	                                            storage + text indexing, the
//	                                            code-interpreter / image-generation
//	                                            endpoint executors, and the
//	                                            Deps-backed implementations of the
//	                                            bridge FileResolver /
//	                                            FileSearchExecutor ports
//
// Dependency direction is one-way: core <- bridge <- storage, core <- storage;
// the bridge never imports this root or the storage domain.
//
// Domain coverage (moved with the implementation, kept here as the record):
//
//	openai-compatible-files              -> openaicompatstorage files store/routes + file storage
//	openai-compatible-vector-stores      -> openaicompatstorage vector-store store/routes + text indexer
//	openai-compatible-images             -> openaicompatstorage image generation bridge executor
//	openai-compatible-computer           -> openaicompatbridge computer browser HTTP adapter executor
//	openai-compatible-code-interpreter   -> openaicompatstorage local python code interpreter executor
//
// Route family (all mounted by openaicompatstorage.Deps.Mount):
// GET/POST /v1/files, GET/DELETE /v1/files/{fileId},
// GET /v1/files/{fileId}/content, GET /v1/containers/{containerId}/files[/...],
// GET/POST /v1/vector_stores, GET/DELETE /v1/vector_stores/{id},
// GET/POST /v1/vector_stores/{id}/files[/...], POST /v1/vector_stores/{id}/search.
// Error envelopes are the OpenAI gateway shape {"error":{message,type,code?}}
// with the Node 中文文案 byte-for-byte; list envelopes are
// {object:'list',data,first_id?,last_id?,has_more}.
//
// The gateway runtime identity (Node req.gatewayRuntime.apiKey) is injected as
// the core ScopeResolver so the gatewaypreauth slice can wire real pre-auth
// later; nil scope mirrors the missing-runtime 401 contract.
package openaicompat
