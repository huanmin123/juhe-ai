package openaicompat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Files routes port openai-compatible-files/files.routes.ts byte-for-byte:
// envelope key order, 中文错误文案 and status codes follow the Node contract.

type fileObject struct {
	ID        string `json:"id"`
	Object    string `json:"object"`
	Bytes     int64  `json:"bytes"`
	CreatedAt int64  `json:"created_at"`
	Filename  string `json:"filename"`
	Purpose   string `json:"purpose"`
	Status    string `json:"status"`
	ExpiresAt *int64 `json:"expires_at,omitempty"`
}

type containerFileObject struct {
	ID          string  `json:"id"`
	Object      string  `json:"object"`
	CreatedAt   int64   `json:"created_at"`
	Bytes       int64   `json:"bytes"`
	Filename    string  `json:"filename"`
	ContainerID *string `json:"container_id"`
	Purpose     string  `json:"purpose"`
	Status      string  `json:"status"`
}

type listEnvelope struct {
	Object  string `json:"object"`
	Data    []any  `json:"data"`
	FirstID any    `json:"first_id,omitempty"`
	LastID  any    `json:"last_id,omitempty"`
	HasMore bool   `json:"has_more"`
}

type deletedFileEnvelope struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Deleted bool   `json:"deleted"`
}

// MountFiles registers the /v1/files + /v1/containers file family.
func (d *Deps) MountFiles(k interface {
	Register(pattern string, handler http.Handler)
}) {
	k.Register("GET /v1/containers/{containerId}/files", d.guard(d.listContainerFiles))
	k.Register("GET /v1/containers/{containerId}/files/{fileId}/content", d.guard(d.downloadContainerFileContent))
	k.Register("GET /v1/containers/{containerId}/files/{fileId}", d.guard(d.getContainerFile))
	k.Register("GET /v1/files", d.guard(d.listFiles))
	k.Register("POST /v1/files", d.guard(d.uploadFile))
	k.Register("GET /v1/files/{fileId}/content", d.guard(d.downloadFileContent))
	k.Register("GET /v1/files/{fileId}", d.guard(d.getFile))
	k.Register("DELETE /v1/files/{fileId}", d.guard(d.deleteFile))
}

// guard wraps a handler with the shared error contract.
func (d *Deps) guard(run func(w http.ResponseWriter, r *http.Request) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handle(w, func() error { return run(w, r) })
	})
}

// newFileObject mirrors openAICompatibleFileObject.
func newFileObject(record FileRecord) (fileObject, error) {
	createdAt, err := openAITimestamp(record.CreatedAt)
	if err != nil {
		return fileObject{}, err
	}
	object := fileObject{
		ID:        record.ID,
		Object:    "file",
		Bytes:     record.Bytes,
		CreatedAt: createdAt,
		Filename:  record.Filename,
		Purpose:   record.Purpose,
		Status:    record.Status,
	}
	if record.ExpiresAt != nil {
		expiresAt, err := openAITimestamp(*record.ExpiresAt)
		if err != nil {
			return fileObject{}, err
		}
		object.ExpiresAt = &expiresAt
	}
	return object, nil
}

// newContainerFileObject mirrors openAICompatibleContainerFileObject.
func newContainerFileObject(record FileRecord) (containerFileObject, error) {
	createdAt, err := openAITimestamp(record.CreatedAt)
	if err != nil {
		return containerFileObject{}, err
	}
	return containerFileObject{
		ID:          record.ID,
		Object:      "container.file",
		CreatedAt:   createdAt,
		Bytes:       record.Bytes,
		Filename:    record.Filename,
		ContainerID: record.ContainerID,
		Purpose:     record.Purpose,
		Status:      record.Status,
	}, nil
}

func writeJSONBody(w http.ResponseWriter, status int, payload any) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		writeUnhandledError(w)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
}

func writeListEnvelope(w http.ResponseWriter, data []any) {
	envelope := listEnvelope{Object: "list", Data: data}
	if len(data) > 0 {
		if first, ok := data[0].(interface{ idValue() string }); ok {
			envelope.FirstID = first.idValue()
			envelope.LastID = data[len(data)-1].(interface{ idValue() string }).idValue()
		}
	}
	writeJSONBody(w, http.StatusOK, envelope)
}

func (o fileObject) idValue() string          { return o.ID }
func (o containerFileObject) idValue() string { return o.ID }

// pathFileID mirrors pathFileId: 缺少文件 ID -> 400 missing_file_id.
func pathFileID(r *http.Request) (string, error) {
	value := strings.TrimSpace(r.PathValue("fileId"))
	if value == "" {
		return "", badRequest("缺少文件 ID", "missing_file_id")
	}
	return value, nil
}

// pathContainerID mirrors pathContainerId: 缺少容器 ID -> 400.
func pathContainerID(r *http.Request) (string, error) {
	value := strings.TrimSpace(r.PathValue("containerId"))
	if value == "" {
		return "", badRequest("缺少容器 ID", "missing_container_id")
	}
	return value, nil
}

func (d *Deps) listFiles(w http.ResponseWriter, r *http.Request) error {
	scope := d.requireScope(w, r)
	if scope == nil {
		return nil
	}
	result, err := d.Store.ListFiles(r.Context(), FileListOptions{
		SystemAccountID: scope.SystemAccountID,
		APIKeyID:        scope.APIKeyID,
		Purpose:         queryStringParam(r.URL.Query(), "purpose"),
		Limit:           queryIntegerParam(r.URL.Query(), "limit"),
		Order:           textParam(r.URL.Query(), "order"),
		After:           textParamOrEmpty(r.URL.Query(), "after"),
	})
	if err != nil {
		return err
	}
	data := make([]any, 0, len(result.Items))
	for _, item := range result.Items {
		object, err := newFileObject(item)
		if err != nil {
			return err
		}
		data = append(data, object)
	}
	writeListEnvelope(w, data)
	return nil
}

// textParam mirrors queryString(...) consumers: only single string values
// count; repeated params become arrays in express and are ignored.
func textParam(query url.Values, name string) string {
	values := query[name]
	if len(values) != 1 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

func textParamOrEmpty(query url.Values, name string) string {
	return textParam(query, name)
}

func (d *Deps) listContainerFiles(w http.ResponseWriter, r *http.Request) error {
	scope := d.requireScope(w, r)
	if scope == nil {
		return nil
	}
	containerID, err := pathContainerID(r)
	if err != nil {
		return err
	}
	purpose := "code_interpreter_output"
	result, err := d.Store.ListFiles(r.Context(), FileListOptions{
		SystemAccountID: scope.SystemAccountID,
		APIKeyID:        scope.APIKeyID,
		Purpose:         &purpose,
		ContainerID:     &containerID,
		Limit:           queryIntegerParam(r.URL.Query(), "limit"),
		Order:           textParam(r.URL.Query(), "order"),
		After:           textParamOrEmpty(r.URL.Query(), "after"),
	})
	if err != nil {
		return err
	}
	data := make([]any, 0, len(result.Items))
	for _, item := range result.Items {
		object, err := newContainerFileObject(item)
		if err != nil {
			return err
		}
		data = append(data, object)
	}
	writeListEnvelope(w, data)
	return nil
}

func (d *Deps) getFile(w http.ResponseWriter, r *http.Request) error {
	if _, ok := d.requireScopeForLookup(w, r); !ok {
		return nil
	}
	record, err := d.findFileForRequest(w, r)
	if err != nil {
		return err
	}
	if record == nil {
		return notFound("文件不存在", "file_not_found")
	}
	object, err := newFileObject(*record)
	if err != nil {
		return err
	}
	writeJSONBody(w, http.StatusOK, object)
	return nil
}

func (d *Deps) getContainerFile(w http.ResponseWriter, r *http.Request) error {
	if _, ok := d.requireScopeForLookup(w, r); !ok {
		return nil
	}
	record, err := d.findContainerFileForRequest(w, r)
	if err != nil {
		return err
	}
	if record == nil {
		return notFound("容器文件不存在", "container_file_not_found")
	}
	object, err := newContainerFileObject(*record)
	if err != nil {
		return err
	}
	writeJSONBody(w, http.StatusOK, object)
	return nil
}

// requireScopeForLookup keeps the 401-before-404 ordering of the Node
// handlers (requireGatewayRuntime runs before the lookup).
func (d *Deps) requireScopeForLookup(w http.ResponseWriter, r *http.Request) (*GatewayScope, bool) {
	scope := d.requireScope(w, r)
	return scope, scope != nil
}

func (d *Deps) findFileForRequest(w http.ResponseWriter, r *http.Request) (*FileRecord, error) {
	scope := d.scope(r)
	fileID, err := pathFileID(r)
	if err != nil {
		return nil, err
	}
	return d.Store.FindFile(r.Context(), fileID, scope.SystemAccountID, scope.APIKeyID)
}

func (d *Deps) findContainerFileForRequest(w http.ResponseWriter, r *http.Request) (*FileRecord, error) {
	containerID, err := pathContainerID(r)
	if err != nil {
		return nil, err
	}
	record, err := d.findFileForRequest(w, r)
	if err != nil {
		return nil, err
	}
	if record == nil || record.Purpose != "code_interpreter_output" || record.ContainerID == nil || *record.ContainerID != containerID {
		return nil, nil
	}
	return record, nil
}

func (d *Deps) downloadFileContent(w http.ResponseWriter, r *http.Request) error {
	return d.downloadContent(w, r, d.findFileForRequest, "文件不存在", "file_not_found")
}

func (d *Deps) downloadContainerFileContent(w http.ResponseWriter, r *http.Request) error {
	return d.downloadContent(w, r, d.findContainerFileForRequest, "容器文件不存在", "container_file_not_found")
}

func (d *Deps) downloadContent(w http.ResponseWriter, r *http.Request, find func(w http.ResponseWriter, r *http.Request) (*FileRecord, error), message, code string) error {
	if _, ok := d.requireScopeForLookup(w, r); !ok {
		return nil
	}
	record, err := find(w, r)
	if err != nil {
		return err
	}
	if record == nil {
		return badRequest(message, code)
	}
	filePath, err := FileObjectPath(d.filesRoot(), record.StorageKey)
	if err != nil {
		return err
	}
	file, err := os.Open(filePath)
	if err != nil {
		return errUnhandled
	}
	defer file.Close()
	mediaType := "application/octet-stream"
	if record.MediaType != nil && *record.MediaType != "" {
		mediaType = *record.MediaType
	}
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Content-Length", strconv.FormatInt(record.Bytes, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, file)
	return nil
}

func (d *Deps) deleteFile(w http.ResponseWriter, r *http.Request) error {
	scope := d.requireScope(w, r)
	if scope == nil {
		return nil
	}
	fileID, err := pathFileID(r)
	if err != nil {
		return err
	}
	record, err := d.Store.DeleteFile(r.Context(), fileID, scope.SystemAccountID, scope.APIKeyID)
	if err != nil {
		return err
	}
	if record == nil {
		return notFound("文件不存在", "file_not_found")
	}
	_ = RemoveFileObject(d.filesRoot(), record.StorageKey)
	writeJSONBody(w, http.StatusOK, deletedFileEnvelope{ID: record.ID, Object: "file", Deleted: true})
	return nil
}

// uploadedFile mirrors UploadedOpenAICompatibleFile.
type uploadedFile struct {
	FileID     string
	Filename   string
	MediaType  string
	HasMedia   bool
	StorageKey string
	Bytes      int64
	SHA256     string
}

// uploadFile mirrors uploadOpenAICompatibleFile.
func (d *Deps) uploadFile(w http.ResponseWriter, r *http.Request) error {
	scope := d.requireScope(w, r)
	if scope == nil {
		return nil
	}
	contentType := r.Header.Get("Content-Type")
	if !isMultipartContentType(contentType) {
		return badRequest("文件上传请求必须使用 multipart/form-data", "invalid_content_type")
	}
	purpose, file, err := d.readMultipartUpload(r)
	if err != nil {
		return err
	}
	created, err := d.Store.CreateFile(r.Context(), FileCreateInput{
		ID:              file.FileID,
		SystemAccountID: scope.SystemAccountID,
		APIKeyID:        scope.APIKeyID,
		Purpose:         purpose,
		Filename:        file.Filename,
		Bytes:           file.Bytes,
		MediaType:       mediaTypePointer(file),
		StorageKey:      file.StorageKey,
		SHA256:          file.SHA256,
	})
	if err != nil {
		_ = RemoveFileObject(d.filesRoot(), file.StorageKey)
		return err
	}
	object, err := newFileObject(created)
	if err != nil {
		return err
	}
	writeJSONBody(w, http.StatusOK, object)
	return nil
}

func mediaTypePointer(file *uploadedFile) *string {
	if !file.HasMedia {
		return nil
	}
	return &file.MediaType
}

// readMultipartUpload mirrors readOpenAICompatibleMultipartUpload with the
// busboy limits files:1 / fields:8 / fileSize:maxBytes.
func (d *Deps) readMultipartUpload(r *http.Request) (string, *uploadedFile, error) {
	maxBytes := d.maxFileBytes()
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return "", nil, errUnhandled
	}
	reader := multipart.NewReader(r.Body, params["boundary"])
	purpose := ""
	seenFileParts := 0
	fieldsSeen := 0
	var upload *uploadedFile
	var parseError error
	for {
		part, partErr := reader.NextPart()
		if partErr == io.EOF {
			break
		}
		if partErr != nil {
			// Malformed multipart: Node's busboy 'error' rejects the parse
			// promise and the express error handler renders the 500.
			return "", nil, errUnhandled
		}
		if part.FileName() == "" {
			fieldsSeen++
			if fieldsSeen <= 8 && part.FormName() == "purpose" {
				value, readErr := io.ReadAll(part)
				if readErr != nil {
					part.Close()
					return "", nil, errUnhandled
				}
				purpose = strings.TrimSpace(string(value))
			}
			part.Close()
			continue
		}
		seenFileParts++
		if seenFileParts > 1 {
			parseError = badRequest("每次请求只能上传一个文件", "too_many_files")
			part.Close()
			continue
		}
		if part.FormName() != "file" {
			part.Close()
			continue
		}
		upload, partErr = d.writeUploadedFile(part, maxBytes)
		if partErr != nil {
			if upload != nil {
				_ = RemoveFileObject(d.filesRoot(), upload.StorageKey)
			}
			return "", nil, partErr
		}
		part.Close()
	}
	if parseError != nil {
		// Node throws the parseError before awaiting the pending file writes,
		// so an already-written upload stays on disk (orphan) — kept aligned.
		return "", nil, parseError
	}
	if purpose == "" {
		if upload != nil {
			_ = RemoveFileObject(d.filesRoot(), upload.StorageKey)
		}
		return "", nil, badRequest("缺少必填 multipart 字段：purpose", "missing_purpose")
	}
	if upload == nil {
		return "", nil, badRequest("缺少必填 multipart 文件字段：file", "missing_file")
	}
	return purpose, upload, nil
}

// writeUploadedFile mirrors writeUploadedOpenAICompatibleFile: persist under
// the storage root with a sha256 and size accounting, enforcing the upload
// limit (413) and the empty-file rule (400).
func (d *Deps) writeUploadedFile(part *multipart.Part, maxBytes int64) (*uploadedFile, error) {
	fileID := d.Store.generateID("file")
	filename := NormalizedUploadFilename(part.FileName())
	mediaType := NormalizeFileMediaType(part.Header.Get("Content-Type"), filename)
	storageKey := StorageKeyForFile(fileID)
	filePath, err := EnsureFileObjectParent(d.filesRoot(), storageKey)
	if err != nil {
		return nil, err
	}
	target, err := os.Create(filePath)
	if err != nil {
		return nil, err
	}
	hash := sha256.New()
	counter := &countingHashWriter{hash: hash}
	written, copyErr := io.Copy(io.MultiWriter(target, counter), io.LimitReader(part, maxBytes+1))
	closeErr := target.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	uploaded := &uploadedFile{
		FileID:     fileID,
		Filename:   filename,
		MediaType:  mediaType,
		HasMedia:   mediaType != "",
		StorageKey: storageKey,
		Bytes:      counter.bytes,
		SHA256:     hex.EncodeToString(hash.Sum(nil)),
	}
	if copyErr != nil || written > maxBytes {
		_ = RemoveFileObject(d.filesRoot(), storageKey)
		if written > maxBytes {
			return nil, newRequestError("上传文件过大", 413, "request_too_large", "file_too_large")
		}
		return nil, errUnhandled
	}
	if counter.bytes <= 0 {
		_ = RemoveFileObject(d.filesRoot(), storageKey)
		return nil, badRequest("上传文件为空", "empty_file")
	}
	return uploaded, nil
}

type countingHashWriter struct {
	hash  interface{ Write([]byte) (int, error) }
	bytes int64
}

func (c *countingHashWriter) Write(p []byte) (int, error) {
	n, err := c.hash.Write(p)
	c.bytes += int64(n)
	return n, err
}
