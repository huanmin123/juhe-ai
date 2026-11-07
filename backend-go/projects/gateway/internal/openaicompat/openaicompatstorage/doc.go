// Package openaicompatstorage hosts the storage / REST endpoint domain of
// openaicompat (REFACTOR-0008 阶段 B): the OpenAI-compatible files and
// vector-stores stores, their HTTP route families, file storage + text
// indexing, the code-interpreter / image-generation endpoint executors. It depends on
// openaicompatcore; the retired bridge ports (FileResolver / FileSearchExecutor)
// and their implementations were removed on 2026-11-07 with the Chat bridge.
package openaicompatstorage
