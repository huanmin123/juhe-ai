package openaicompat

import (
	"context"
	"encoding/base64"
	"os"
	"sort"
)

// Bridge executors port openai-compatible-files/file-resolver.ts and
// openai-compatible-vector-stores/file-search-executor.ts: the OpenAI ->
// Anthropic bridge consumes these through the ports below (Node types
// OpenAIToAnthropicFileResolver / OpenAIToAnthropicFileSearchExecutor).

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

type scopeFileResolver struct {
	deps  *Deps
	scope GatewayScope
}

// FileResolverForScope mirrors openAICompatibleFilesResolverForGatewayRequest:
// nil when the request carries no gateway runtime.
func (d *Deps) FileResolverForScope(scope *GatewayScope) FileResolver {
	if scope == nil {
		return nil
	}
	return &scopeFileResolver{deps: d, scope: *scope}
}

// ResolveFile mirrors the resolveFile closure: unknown ids return nil,
// oversize files render the 413 bridge error, text media types resolve as
// UTF-8 text and everything else as base64.
func (r *scopeFileResolver) ResolveFile(ctx context.Context, input FileResolveInput) (*ResolvedFile, error) {
	record, err := r.deps.Store.FindFile(ctx, input.FileID, r.scope.SystemAccountID, r.scope.APIKeyID)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, nil
	}
	if record.Bytes > BridgeMaxFileBytes {
		return nil, bridgeError(
			"文件 "+record.ID+" 超过 Anthropic bridge 单次解析大小上限",
			"openai_anthropic_bridge_file_too_large", 413, "request_too_large")
	}
	path, err := FileObjectPath(r.deps.filesRoot(), record.StorageKey)
	if err != nil {
		return nil, err
	}
	buffer, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	mediaType := ""
	if record.MediaType != nil {
		mediaType = *record.MediaType
	}
	resolved := &ResolvedFile{
		FileID:    record.ID,
		Filename:  record.Filename,
		MediaType: mediaType,
		Bytes:     record.Bytes,
	}
	if isTextBridgeMediaType(mediaType) {
		resolved.ContentText = string(buffer)
		return resolved, nil
	}
	resolved.ContentBase64 = base64.StdEncoding.EncodeToString(buffer)
	return resolved, nil
}

// isTextBridgeMediaType mirrors isTextBridgeMediaType.
func isTextBridgeMediaType(mediaType string) bool {
	if mediaType == "" {
		return false
	}
	return mediaType == "text/plain" || hasTextPrefix(mediaType)
}

func hasTextPrefix(mediaType string) bool {
	return len(mediaType) >= 5 && mediaType[:5] == "text/"
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

type scopeFileSearchExecutor struct {
	deps  *Deps
	scope GatewayScope
}

// FileSearchExecutorForScope mirrors
// openAICompatibleFileSearchExecutorForGatewayRequest.
func (d *Deps) FileSearchExecutorForScope(scope *GatewayScope) FileSearchExecutor {
	if scope == nil {
		return nil
	}
	return &scopeFileSearchExecutor{deps: d, scope: *scope}
}

// Search mirrors the search closure: per-store readiness guards with the
// bridge error codes, cross-store merge sorted by score desc then fileId.
func (e *scopeFileSearchExecutor) Search(ctx context.Context, input FileSearchInput) (*FileSearchOutput, error) {
	maxNumResults := normalizeFileSearchMaxResults(input.MaxNumResults)
	allResults := []FileSearchResult{}
	for _, vectorStoreID := range input.VectorStoreIDs {
		store, err := e.deps.Store.FindVectorStore(ctx, vectorStoreID, e.scope.SystemAccountID, e.scope.APIKeyID)
		if err != nil {
			return nil, err
		}
		if store == nil {
			return nil, bridgeError(
				"向量存储 "+vectorStoreID+" 不存在",
				"openai_anthropic_bridge_file_search_vector_store_not_found", 404, "invalid_request_error")
		}
		if store.FileCounts.InProgress > 0 {
			return nil, bridgeError(
				"向量存储 "+vectorStoreID+" 的文件仍在建立索引",
				"openai_anthropic_bridge_file_search_vector_store_not_ready", 409, "invalid_request_error")
		}
		if store.FileCounts.Completed <= 0 && store.FileCounts.Failed > 0 {
			return nil, bridgeError(
				"向量存储 "+vectorStoreID+" 没有可检索的已完成文件",
				"openai_anthropic_bridge_file_search_vector_store_failed", 400, "invalid_request_error")
		}
		results, err := e.deps.Store.SearchVectorStore(ctx, SearchOptions{
			VectorStoreID:   vectorStoreID,
			SystemAccountID: e.scope.SystemAccountID,
			APIKeyID:        e.scope.APIKeyID,
			Query:           input.Query,
			MaxNumResults:   intPtr(maxNumResults),
			Filters:         input.Filters,
			ScoreThreshold:  scoreThresholdFromRankingOptions(input.RankingOptions),
		})
		if err != nil {
			return nil, err
		}
		for _, result := range results {
			allResults = append(allResults, FileSearchResult{
				FileID:      result.FileID,
				Filename:    result.Filename,
				Score:       result.Score,
				ContentText: result.ContentText,
			})
		}
	}
	sort.SliceStable(allResults, func(left, right int) bool {
		l, r := allResults[left], allResults[right]
		if l.Score != r.Score {
			return l.Score > r.Score
		}
		return l.FileID < r.FileID
	})
	if len(allResults) > maxNumResults {
		allResults = allResults[:maxNumResults]
	}
	return &FileSearchOutput{Queries: []string{input.Query}, Results: allResults}, nil
}

// normalizeFileSearchMaxResults mirrors normalizeFileSearchMaxResults
// (default 10, clamp 1..50).
func normalizeFileSearchMaxResults(value *float64) int {
	if value == nil || *value != *value {
		return 10
	}
	truncated := int(*value)
	if truncated < 1 {
		return 1
	}
	if truncated > 50 {
		return 50
	}
	return truncated
}

// scoreThresholdFromRankingOptions mirrors scoreThresholdFromRankingOptions.
func scoreThresholdFromRankingOptions(options map[string]any) *float64 {
	if options == nil {
		return nil
	}
	return queryNumberValue(options["score_threshold"])
}

func intPtr(value int) *int { return &value }
