// Package openaicompatbridge hosts the cross-protocol conversion domain of
// openaicompat (REFACTOR-0008 阶段 A-2): the request-body build matrix, the
// response / SSE transform matrix, stream state machines, JSON helpers and
// the hosted-tool executor vocabulary. It only depends on the neutral
// openaicompatcore; the storage endpoint domain (openaicompatstorage)
// implements the FileResolver / FileSearchExecutor ports declared here.
package openaicompatbridge
