package openaicompatbridge

import (
	"context"
)

// Bridge executors port openai-compatible-files/file-resolver.ts and
// openai-compatible-vector-stores/file-search-executor.ts: the OpenAI ->
// Anthropic bridge consumes these through the ports below (Node types
// OpenAIToAnthropicFileResolver / OpenAIToAnthropicFileSearchExecutor).
// The Deps-backed implementations (scopeFileResolver / scopeFileSearchExecutor
// and the *ForScope constructors) live in the storage domain
// (openaicompatstorage, REFACTOR-0008 阶段 B), which imports this package —
// same narrow-port pattern as the AccountWriter interface in REFACTOR-0005.

// ResolvedFile mirrors OpenAIToAnthropicResolvedFile.
type ResolvedFile struct {
	FileID        string
	Filename      string
	MediaType     string
	Bytes         int64
	ContentBase64 string
	ContentText   string
}

// FileResolveInput mirrors OpenAIToAnthropicFileResolveInput (the bridge-side
// fields this module consumes).
type FileResolveInput struct {
	FileID string
}

// FileResolver mirrors OpenAIToAnthropicFileResolver.
type FileResolver interface {
	ResolveFile(ctx context.Context, input FileResolveInput) (*ResolvedFile, error)
}

// FileSearchResult mirrors OpenAIToAnthropicFileSearchResult.
type FileSearchResult struct {
	FileID      string
	Filename    string
	Score       float64
	ContentText string
}

// FileSearchOutput mirrors the executor's {queries?, results} return.
type FileSearchOutput struct {
	Queries []string
	Results []FileSearchResult
}

// FileSearchInput mirrors OpenAIToAnthropicFileSearchInput.
type FileSearchInput struct {
	VectorStoreIDs []string
	Query          string
	MaxNumResults  *float64
	Filters        map[string]any
	RankingOptions map[string]any
}

// FileSearchExecutor mirrors OpenAIToAnthropicFileSearchExecutor.
type FileSearchExecutor interface {
	Search(ctx context.Context, input FileSearchInput) (*FileSearchOutput, error)
}
