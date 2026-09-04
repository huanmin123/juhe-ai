package openaicompat

import (
	"context"
	"net/http"
	"strings"
)

// Vector store routes port openai-compatible-vector-stores/vector-stores.routes.ts.

type fileCountsObject struct {
	InProgress int `json:"in_progress"`
	Completed  int `json:"completed"`
	Failed     int `json:"failed"`
	Cancelled  int `json:"cancelled"`
	Total      int `json:"total"`
}

type expiresAfterObject struct {
	Anchor string `json:"anchor"`
	Days   int    `json:"days"`
}

type vectorStoreObject struct {
	ID           string              `json:"id"`
	Object       string              `json:"object"`
	CreatedAt    int64               `json:"created_at"`
	Name         *string             `json:"name"`
	Description  *string             `json:"description"`
	Bytes        int64               `json:"bytes"`
	FileCounts   fileCountsObject    `json:"file_counts"`
	Status       string              `json:"status"`
	Metadata     map[string]any      `json:"metadata"`
	ExpiresAfter *expiresAfterObject `json:"expires_after,omitempty"`
	ExpiresAt    *int64              `json:"expires_at,omitempty"`
}

var defaultChunkingStrategy = map[string]any{
	"type": "static",
	"static": map[string]any{
		"max_chunk_size_tokens": 800,
		"chunk_overlap_tokens":  400,
	},
}

type vectorStoreFileObject struct {
	ID               string         `json:"id"`
	Object           string         `json:"object"`
	UsageBytes       int64          `json:"usage_bytes"`
	CreatedAt        int64          `json:"created_at"`
	VectorStoreID    string         `json:"vector_store_id"`
	Status           string         `json:"status"`
	LastError        map[string]any `json:"last_error"`
	ChunkingStrategy map[string]any `json:"chunking_strategy"`
	Attributes       map[string]any `json:"attributes"`
}

type vectorStoreFileContentObject struct {
	ID         string `json:"id"`
	Object     string `json:"object"`
	Type       string `json:"type"`
	Text       string `json:"text"`
	FileID     string `json:"file_id"`
	Filename   string `json:"filename"`
	ChunkIndex int    `json:"chunk_index"`
}

type searchContentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type searchResultItem struct {
	FileID     string              `json:"file_id"`
	Filename   string              `json:"filename"`
	Score      float64             `json:"score"`
	Attributes map[string]any      `json:"attributes"`
	Content    []searchContentItem `json:"content"`
}

type searchResponseObject struct {
	Object      string             `json:"object"`
	SearchQuery string             `json:"search_query"`
	Data        []searchResultItem `json:"data"`
	HasMore     bool               `json:"has_more"`
	NextPage    any                `json:"next_page"`
}

type deletedVectorStoreEnvelope struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Deleted bool   `json:"deleted"`
}

// MountVectorStores registers the /v1/vector_stores family (Node registers
// the file-content route before the parameterized file route; Go ServeMux
// resolves specificity itself).
func (d *Deps) MountVectorStores(k interface {
	Register(pattern string, handler http.Handler)
}) {
	k.Register("GET /v1/vector_stores", d.guard(d.listVectorStores))
	k.Register("POST /v1/vector_stores", d.guard(d.createVectorStore))
	k.Register("GET /v1/vector_stores/{vectorStoreId}/files/{fileId}/content", d.guard(d.listVectorStoreFileContent))
	k.Register("GET /v1/vector_stores/{vectorStoreId}/files/{fileId}", d.guard(d.getVectorStoreFile))
	k.Register("DELETE /v1/vector_stores/{vectorStoreId}/files/{fileId}", d.guard(d.deleteVectorStoreFile))
	k.Register("GET /v1/vector_stores/{vectorStoreId}/files", d.guard(d.listVectorStoreFiles))
	k.Register("POST /v1/vector_stores/{vectorStoreId}/files", d.guard(d.createVectorStoreFile))
	k.Register("POST /v1/vector_stores/{vectorStoreId}/search", d.guard(d.searchVectorStore))
	k.Register("GET /v1/vector_stores/{vectorStoreId}", d.guard(d.getVectorStore))
	k.Register("DELETE /v1/vector_stores/{vectorStoreId}", d.guard(d.deleteVectorStore))
}

// pathVectorStoreID mirrors pathVectorStoreId.
func pathVectorStoreID(r *http.Request) (string, error) {
	value := strings.TrimSpace(r.PathValue("vectorStoreId"))
	if value == "" {
		return "", badRequest("缺少向量存储 ID", "missing_vector_store_id")
	}
	return value, nil
}

func pathVectorStoreFileID(r *http.Request) (string, error) {
	value := strings.TrimSpace(r.PathValue("fileId"))
	if value == "" {
		return "", badRequest("缺少文件 ID", "missing_file_id")
	}
	return value, nil
}

// newVectorStoreObject mirrors openAICompatibleVectorStoreObject: Node
// includes expires_after only when anchor or a positive day count is truthy.
func newVectorStoreObject(record VectorStoreRecord) (vectorStoreObject, error) {
	createdAt, err := openAITimestamp(record.CreatedAt)
	if err != nil {
		return vectorStoreObject{}, err
	}
	object := vectorStoreObject{
		ID:          record.ID,
		Object:      "vector_store",
		CreatedAt:   createdAt,
		Name:        record.Name,
		Description: record.Description,
		Bytes:       record.Bytes,
		FileCounts: fileCountsObject{
			InProgress: record.FileCounts.InProgress,
			Completed:  record.FileCounts.Completed,
			Failed:     record.FileCounts.Failed,
			Cancelled:  record.FileCounts.Cancelled,
			Total:      record.FileCounts.Total,
		},
		Status:   "completed",
		Metadata: record.Metadata,
	}
	if record.FileCounts.InProgress > 0 {
		object.Status = "in_progress"
	}
	if record.Metadata == nil {
		object.Metadata = map[string]any{}
	}
	if (record.ExpiresAfterAnchor != nil && *record.ExpiresAfterAnchor != "") ||
		(record.ExpiresAfterDays != nil && *record.ExpiresAfterDays != 0) {
		anchor := "last_active_at"
		if record.ExpiresAfterAnchor != nil && *record.ExpiresAfterAnchor != "" {
			anchor = *record.ExpiresAfterAnchor
		}
		days := 0
		if record.ExpiresAfterDays != nil {
			days = *record.ExpiresAfterDays
		}
		object.ExpiresAfter = &expiresAfterObject{Anchor: anchor, Days: days}
	}
	if record.ExpiresAt != nil {
		expiresAt, err := openAITimestamp(*record.ExpiresAt)
		if err != nil {
			return vectorStoreObject{}, err
		}
		object.ExpiresAt = &expiresAt
	}
	return object, nil
}

// newVectorStoreFileObject mirrors openAICompatibleVectorStoreFileObject: an
// empty stored chunking strategy renders the static default.
func newVectorStoreFileObject(record VectorStoreFileRecord) (vectorStoreFileObject, error) {
	createdAt, err := openAITimestamp(record.CreatedAt)
	if err != nil {
		return vectorStoreFileObject{}, err
	}
	chunking := record.ChunkingStrategy
	if len(chunking) == 0 {
		chunking = defaultChunkingStrategy
	}
	attributes := record.Attributes
	if attributes == nil {
		attributes = map[string]any{}
	}
	var lastError map[string]any
	if record.HasLastError {
		lastError = record.LastError
		if lastError == nil {
			lastError = map[string]any{}
		}
	}
	return vectorStoreFileObject{
		ID:               record.FileID,
		Object:           "vector_store.file",
		UsageBytes:       record.UsageBytes,
		CreatedAt:        createdAt,
		VectorStoreID:    record.VectorStoreID,
		Status:           record.Status,
		LastError:        lastError,
		ChunkingStrategy: chunking,
		Attributes:       attributes,
	}, nil
}

func (d *Deps) createVectorStore(w http.ResponseWriter, r *http.Request) error {
	scope := d.requireScope(w, r)
	if scope == nil {
		return nil
	}
	body, err := readJSONObjectBody(r)
	if err != nil {
		return err
	}
	expiresAfter := objectValue(body["expires_after"])
	expiresAfterDays := queryIntegerValue(expiresAfter["days"])
	input := VectorStoreCreateInput{
		SystemAccountID:    scope.SystemAccountID,
		APIKeyID:           scope.APIKeyID,
		Name:               stringValue(body["name"]),
		Description:        stringValue(body["description"]),
		Metadata:           objectValue(body["metadata"]),
		ExpiresAfterAnchor: stringValue(expiresAfter["anchor"]),
		ExpiresAfterDays:   expiresAfterDays,
		ExpiresAt:          expiresAtFromDays(expiresAfterDays, d.Store.now()),
	}
	created, err := d.Store.CreateVectorStore(r.Context(), d.Store.generateID("vector_store"), input)
	if err != nil {
		return err
	}
	object, err := newVectorStoreObject(created)
	if err != nil {
		return err
	}
	writeJSONBody(w, http.StatusOK, object)
	return nil
}

func (d *Deps) listVectorStores(w http.ResponseWriter, r *http.Request) error {
	scope := d.requireScope(w, r)
	if scope == nil {
		return nil
	}
	result, err := d.Store.ListVectorStores(r.Context(), VectorStoreListOptions{
		SystemAccountID: scope.SystemAccountID,
		APIKeyID:        scope.APIKeyID,
		Limit:           queryIntegerParam(r.URL.Query(), "limit"),
		Order:           textParam(r.URL.Query(), "order"),
		After:           textParamOrEmpty(r.URL.Query(), "after"),
		Before:          textParamOrEmpty(r.URL.Query(), "before"),
	})
	if err != nil {
		return err
	}
	data := make([]any, 0, len(result.Items))
	for _, item := range result.Items {
		object, err := newVectorStoreObject(item)
		if err != nil {
			return err
		}
		data = append(data, object)
	}
	writeListEnvelope(w, data)
	return nil
}

func (d *Deps) findVectorStoreForRequest(w http.ResponseWriter, r *http.Request) (*VectorStoreRecord, error) {
	scope := d.scope(r)
	vectorStoreID, err := pathVectorStoreID(r)
	if err != nil {
		return nil, err
	}
	return d.Store.FindVectorStore(r.Context(), vectorStoreID, scope.SystemAccountID, scope.APIKeyID)
}

func (d *Deps) requireVectorStoreForRequest(w http.ResponseWriter, r *http.Request) (*VectorStoreRecord, error) {
	record, err := d.findVectorStoreForRequest(w, r)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, notFound("向量存储不存在", "vector_store_not_found")
	}
	return record, nil
}

func (d *Deps) getVectorStore(w http.ResponseWriter, r *http.Request) error {
	if _, ok := d.requireScopeForLookup(w, r); !ok {
		return nil
	}
	record, err := d.findVectorStoreForRequest(w, r)
	if err != nil {
		return err
	}
	if record == nil {
		return notFound("向量存储不存在", "vector_store_not_found")
	}
	object, err := newVectorStoreObject(*record)
	if err != nil {
		return err
	}
	writeJSONBody(w, http.StatusOK, object)
	return nil
}

func (d *Deps) deleteVectorStore(w http.ResponseWriter, r *http.Request) error {
	scope := d.requireScope(w, r)
	if scope == nil {
		return nil
	}
	vectorStoreID, err := pathVectorStoreID(r)
	if err != nil {
		return err
	}
	record, err := d.Store.DeleteVectorStore(r.Context(), vectorStoreID, scope.SystemAccountID, scope.APIKeyID)
	if err != nil {
		return err
	}
	if record == nil {
		return notFound("向量存储不存在", "vector_store_not_found")
	}
	writeJSONBody(w, http.StatusOK, deletedVectorStoreEnvelope{ID: record.ID, Object: "vector_store.deleted", Deleted: true})
	return nil
}

func (d *Deps) createVectorStoreFile(w http.ResponseWriter, r *http.Request) error {
	scope := d.requireScope(w, r)
	if scope == nil {
		return nil
	}
	vectorStoreID, err := pathVectorStoreID(r)
	if err != nil {
		return err
	}
	body, err := readJSONObjectBody(r)
	if err != nil {
		return err
	}
	fileID := stringValue(body["file_id"])
	if fileID == nil {
		return badRequest("缺少必填字段：file_id", "missing_file_id")
	}
	store, err := d.findVectorStoreForRequest(w, r)
	if err != nil {
		return err
	}
	if store == nil {
		return notFound("向量存储不存在", "vector_store_not_found")
	}
	file, err := d.Store.FindFile(r.Context(), *fileID, scope.SystemAccountID, scope.APIKeyID)
	if err != nil {
		return err
	}
	if file == nil {
		return notFound("文件不存在", "file_not_found")
	}
	created, err := d.Store.CreateVectorStoreFile(r.Context(), VectorStoreFileCreateInput{
		VectorStoreID:    vectorStoreID,
		FileID:           *fileID,
		SystemAccountID:  scope.SystemAccountID,
		APIKeyID:         scope.APIKeyID,
		Attributes:       objectValue(body["attributes"]),
		ChunkingStrategy: objectValue(body["chunking_strategy"]),
		Status:           VectorStoreFileStatusInProgress,
	})
	if err != nil {
		return err
	}
	if created == nil {
		return notFound("向量存储或文件不存在", "vector_store_file_not_found")
	}
	d.queueVectorStoreFileIndexing(vectorStoreID, *file, *scope, objectValue(body["attributes"]), objectValue(body["chunking_strategy"]))
	object, err := newVectorStoreFileObject(*created)
	if err != nil {
		return err
	}
	writeJSONBody(w, http.StatusOK, object)
	return nil
}

// queueVectorStoreFileIndexing mirrors queueOpenAICompatibleVectorStoreFileIndexing.
func (d *Deps) queueVectorStoreFileIndexing(vectorStoreID string, file FileRecord, scope GatewayScope, attributes, chunkingStrategy map[string]any) {
	task := func() { d.indexVectorStoreFile(vectorStoreID, file, scope, attributes, chunkingStrategy) }
	if d.IndexAsync != nil {
		d.IndexAsync(task)
		return
	}
	go task()
}

// indexVectorStoreFile mirrors indexOpenAICompatibleVectorStoreFile: build
// chunks from the physical file, then upsert the completed state; failures
// persist the failed status with a last_error payload.
func (d *Deps) indexVectorStoreFile(vectorStoreID string, file FileRecord, scope GatewayScope, attributes, chunkingStrategy map[string]any) {
	input := VectorStoreFileCreateInput{
		VectorStoreID:    vectorStoreID,
		FileID:           file.ID,
		SystemAccountID:  scope.SystemAccountID,
		APIKeyID:         scope.APIKeyID,
		Attributes:       attributes,
		ChunkingStrategy: chunkingStrategy,
	}
	chunks, err := BuildVectorStoreChunks(d.filesRoot(), file)
	if err == nil {
		input.Status = VectorStoreFileStatusCompleted
		input.Chunks = chunks
		_, err = d.Store.CreateVectorStoreFile(context.Background(), input)
	}
	if err != nil {
		// Node catch: persist the failed state; a persist failure surfaces
		// through the queue's warn handler.
		input.Status = VectorStoreFileStatusFailed
		input.Chunks = nil
		input.LastError = indexingLastError(err)
		if _, createErr := d.Store.CreateVectorStoreFile(context.Background(), input); createErr != nil && d.Warn != nil {
			d.Warn(createErr, map[string]any{"vectorStoreId": vectorStoreID, "fileId": file.ID})
		}
	}
}

func indexingLastError(err error) map[string]any {
	if indexingErr, ok := err.(*IndexingError); ok {
		return map[string]any{
			"code":    indexingErr.Code,
			"type":    indexingErr.Type,
			"message": indexingErr.Message,
		}
	}
	return map[string]any{
		"code":    "openai_compatible_vector_store_index_failed",
		"type":    "invalid_request_error",
		"message": "向量存储文件建立索引失败",
	}
}

func (d *Deps) listVectorStoreFiles(w http.ResponseWriter, r *http.Request) error {
	scope := d.requireScope(w, r)
	if scope == nil {
		return nil
	}
	if _, err := d.requireVectorStoreForRequest(w, r); err != nil {
		return err
	}
	vectorStoreID, err := pathVectorStoreID(r)
	if err != nil {
		return err
	}
	result, err := d.Store.ListVectorStoreFiles(r.Context(), VectorStoreFileListOptions{
		VectorStoreID:   vectorStoreID,
		SystemAccountID: scope.SystemAccountID,
		APIKeyID:        scope.APIKeyID,
		Limit:           queryIntegerParam(r.URL.Query(), "limit"),
		Order:           textParam(r.URL.Query(), "order"),
		After:           textParamOrEmpty(r.URL.Query(), "after"),
	})
	if err != nil {
		return err
	}
	data := make([]any, 0, len(result.Items))
	for _, item := range result.Items {
		object, err := newVectorStoreFileObject(item)
		if err != nil {
			return err
		}
		data = append(data, object)
	}
	writeListEnvelope(w, data)
	return nil
}

func (d *Deps) findVectorStoreFileForRequest(w http.ResponseWriter, r *http.Request) (*VectorStoreFileRecord, error) {
	scope := d.scope(r)
	vectorStoreID, err := pathVectorStoreID(r)
	if err != nil {
		return nil, err
	}
	fileID, err := pathVectorStoreFileID(r)
	if err != nil {
		return nil, err
	}
	return d.Store.FindVectorStoreFile(r.Context(), vectorStoreID, fileID, scope.SystemAccountID, scope.APIKeyID)
}

func (d *Deps) getVectorStoreFile(w http.ResponseWriter, r *http.Request) error {
	if _, ok := d.requireScopeForLookup(w, r); !ok {
		return nil
	}
	record, err := d.findVectorStoreFileForRequest(w, r)
	if err != nil {
		return err
	}
	if record == nil {
		return notFound("向量存储文件不存在", "vector_store_file_not_found")
	}
	object, err := newVectorStoreFileObject(*record)
	if err != nil {
		return err
	}
	writeJSONBody(w, http.StatusOK, object)
	return nil
}

func (d *Deps) deleteVectorStoreFile(w http.ResponseWriter, r *http.Request) error {
	scope := d.requireScope(w, r)
	if scope == nil {
		return nil
	}
	vectorStoreID, err := pathVectorStoreID(r)
	if err != nil {
		return err
	}
	fileID, err := pathVectorStoreFileID(r)
	if err != nil {
		return err
	}
	record, err := d.Store.DeleteVectorStoreFile(r.Context(), vectorStoreID, fileID, scope.SystemAccountID, scope.APIKeyID)
	if err != nil {
		return err
	}
	if record == nil {
		return notFound("向量存储文件不存在", "vector_store_file_not_found")
	}
	writeJSONBody(w, http.StatusOK, deletedVectorStoreEnvelope{ID: fileID, Object: "vector_store.file.deleted", Deleted: true})
	return nil
}

func (d *Deps) listVectorStoreFileContent(w http.ResponseWriter, r *http.Request) error {
	if _, ok := d.requireScopeForLookup(w, r); !ok {
		return nil
	}
	record, err := d.findVectorStoreFileForRequest(w, r)
	if err != nil {
		return err
	}
	if record == nil {
		return notFound("向量存储文件不存在", "vector_store_file_not_found")
	}
	scope := d.scope(r)
	vectorStoreID, err := pathVectorStoreID(r)
	if err != nil {
		return err
	}
	fileID, err := pathVectorStoreFileID(r)
	if err != nil {
		return err
	}
	chunks, err := d.Store.ListVectorStoreFileChunks(r.Context(), vectorStoreID, fileID, scope.SystemAccountID, scope.APIKeyID, queryIntegerParam(r.URL.Query(), "limit"))
	if err != nil {
		return err
	}
	data := make([]any, 0, len(chunks))
	for _, chunk := range chunks {
		data = append(data, vectorStoreFileContentObject{
			ID:         chunk.ChunkID,
			Object:     "vector_store.file_content",
			Type:       "text",
			Text:       chunk.ContentText,
			FileID:     chunk.FileID,
			Filename:   chunk.Filename,
			ChunkIndex: chunk.ChunkIndex,
		})
	}
	writeListEnvelope(w, data)
	return nil
}

// requireVectorStoreReadyForSearch mirrors
// requireOpenAICompatibleVectorStoreReadyForSearch.
func requireVectorStoreReadyForSearch(store *VectorStoreRecord) error {
	if store.FileCounts.InProgress > 0 {
		return newRequestError("向量存储文件仍在建立索引", 409, "invalid_request_error", "openai_compatible_vector_store_not_ready")
	}
	if store.FileCounts.Completed <= 0 && store.FileCounts.Failed > 0 {
		return badRequest("向量存储没有可检索的已完成文件", "openai_compatible_vector_store_file_failed")
	}
	return nil
}

func (d *Deps) searchVectorStore(w http.ResponseWriter, r *http.Request) error {
	scope := d.requireScope(w, r)
	if scope == nil {
		return nil
	}
	store, err := d.requireVectorStoreForRequest(w, r)
	if err != nil {
		return err
	}
	if err := requireVectorStoreReadyForSearch(store); err != nil {
		return err
	}
	body, err := readJSONObjectBody(r)
	if err != nil {
		return err
	}
	query := stringValue(body["query"])
	if query == nil {
		return badRequest("缺少必填字段：query", "missing_query")
	}
	rankingOptions := objectValue(body["ranking_options"])
	filters := objectValue(body["filters"])
	if filters == nil {
		filters = objectValue(body["attribute_filter"])
	}
	vectorStoreID, err := pathVectorStoreID(r)
	if err != nil {
		return err
	}
	results, err := d.Store.SearchVectorStore(r.Context(), SearchOptions{
		VectorStoreID:   vectorStoreID,
		SystemAccountID: scope.SystemAccountID,
		APIKeyID:        scope.APIKeyID,
		Query:           *query,
		MaxNumResults:   queryIntegerValue(body["max_num_results"]),
		Filters:         filters,
		ScoreThreshold:  queryNumberValue(rankingOptions["score_threshold"]),
	})
	if err != nil {
		return err
	}
	items := make([]searchResultItem, 0, len(results))
	for _, result := range results {
		attributes := result.Attributes
		if attributes == nil {
			attributes = map[string]any{}
		}
		items = append(items, searchResultItem{
			FileID:     result.FileID,
			Filename:   result.Filename,
			Score:      result.Score,
			Attributes: attributes,
			Content:    []searchContentItem{{Type: "text", Text: result.ContentText}},
		})
	}
	writeJSONBody(w, http.StatusOK, searchResponseObject{
		Object:      "vector_store.search_results.page",
		SearchQuery: *query,
		Data:        items,
		HasMore:     false,
		NextPage:    nil,
	})
	return nil
}
