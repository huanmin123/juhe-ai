// Package openaicompatstorage hosts the storage / REST endpoint domain of
// openaicompat (REFACTOR-0008 阶段 B): the OpenAI-compatible files and
// vector-stores stores, their HTTP route families, file storage + text
// indexing, the code-interpreter / image-generation endpoint executors and
// the Deps-backed implementations of the bridge-declared FileResolver /
// FileSearchExecutor ports. It depends on openaicompatcore and
// openaicompatbridge only; the bridge never imports this package.
package openaicompatstorage
