// Package openaicompat ports the five Node openai-compatible modules to a
// single Go vertical slice:
//
//	openai-compatible-files              -> files store/routes + file storage
//	openai-compatible-vector-stores      -> vector-store store/routes + text indexer
//	openai-compatible-images             -> image generation bridge executor
//	openai-compatible-computer           -> computer browser HTTP adapter executor
//	openai-compatible-code-interpreter   -> local python code interpreter executor
//
// plus the two storage repositories on the import chain
// (storage/openai-compatible-files.repository.ts and
// storage/openai-compatible-vector-stores.repository.ts).
//
// Route family: GET/POST /v1/files, GET/DELETE /v1/files/{fileId},
// GET /v1/files/{fileId}/content, GET /v1/containers/{containerId}/files[/...],
// GET/POST /v1/vector_stores, GET/DELETE /v1/vector_stores/{id},
// GET/POST /v1/vector_stores/{id}/files[/...], POST /v1/vector_stores/{id}/search.
// Error envelopes are the OpenAI gateway shape {"error":{message,type,code?}}
// with the Node 中文文案 byte-for-byte; list envelopes are
// {object:'list',data,first_id?,last_id?,has_more}.
//
// The gateway runtime identity (Node req.gatewayRuntime.apiKey) is injected as
// a ScopeResolver so the gatewaypreauth slice can wire real pre-auth later;
// nil scope mirrors the missing-runtime 401 contract.
package openaicompat
