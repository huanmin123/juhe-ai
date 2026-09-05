package logreads

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// ---------------------------------------------------------------------------
// Audit payload detail + by-id hydration (audit-log-f3-query.repository.ts
// getAuditLogPayload / listAuditLogsByIds).
//
// The payload route always reads the full body window (Node route passes
// { full: true }): blob bytes are decompressed once, validated against the
// audit_payload_blobs metadata and surfaced as bodyText (lossless UTF-8) or
// bodyBase64, mirroring auditPayloadBodyDetail.
// ---------------------------------------------------------------------------

// auditPayloadBlobStorageStatus values (AuditPayloadBlobStorageStatus).
const (
	auditPayloadStatusNotSaved        = "not_saved"
	auditPayloadStatusMetadataMissing = "metadata_missing"
	auditPayloadStatusFileMissing     = "file_missing"
	auditPayloadStatusAvailable       = "available"
)

// auditPayloadReadLimit is f3MaxPayloadReadLimit: the header window cap.
const auditPayloadReadLimit = 1024 * 1024

// AuditLogPayloadDetail mirrors AuditLogPayloadDetail: the payload summary
// plus the header object and the full body window. BodyText/BodyBase64 are
// pointers so an empty body still serializes as "" exactly like Node while a
// missing blob omits both keys.
type AuditLogPayloadDetail struct {
	AuditLogPayloadSummary
	Headers              map[string]any `json:"headers,omitempty"`
	BodyText             *string        `json:"bodyText,omitempty"`
	BodyBase64           *string        `json:"bodyBase64,omitempty"`
	HeadersIncluded      bool           `json:"headersIncluded"`
	HeadersStorageStatus string         `json:"headersStorageStatus"`
	BodyStorageStatus    string         `json:"bodyStorageStatus"`
	BodyOffset           int64          `json:"bodyOffset"`
	BodyLimit            int64          `json:"bodyLimit"`
	BodyBytesReturned    int64          `json:"bodyBytesReturned"`
	BodyTotalBytes       int64          `json:"bodyTotalBytes"`
	BodyNextOffset       *int64         `json:"bodyNextOffset,omitempty"`
	BodyTruncated        bool           `json:"bodyTruncated"`
}

// ListAuditLogsByID mirrors listAuditLogsByIds: dedupe + trim, chunked IN
// lookups over the persisted-traffic rows and the caller's order restored
// afterwards (missing ids simply drop out).
func (s *auditLogSQLReader) ListAuditLogsByID(ctx context.Context, ids []string) ([]AuditLogListItem, error) {
	unique := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		text := strings.TrimSpace(id)
		if text == "" || seen[text] {
			continue
		}
		seen[text] = true
		unique = append(unique, text)
	}
	if len(unique) == 0 {
		return []AuditLogListItem{}, nil
	}
	items := make([]AuditLogListItem, 0, len(unique))
	for start := 0; start < len(unique); start += auditLogIDChunkSize {
		end := min(start+auditLogIDChunkSize, len(unique))
		chunk := unique[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		query := `SELECT ` + auditLogListSelectColumns + ` FROM ` + s.mode.table("audit_logs") + ` al ` +
			`WHERE al.id IN (` + placeholders + `) AND ` + s.persistedTrafficClause("al")
		params := append([]any{}, toAnySlice(chunk)...)
		params = append(params, auditLogNonPersistedTrafficSources[0], auditLogNonPersistedTrafficSources[1], auditLogNonPersistedTrafficSources[2])
		rows, err := readQueryMaps(ctx, s.db, s.mode, query, params...)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			items = append(items, auditLogListItemFromRow(row))
		}
	}
	order := make(map[string]int, len(unique))
	for index, id := range unique {
		order[id] = index
	}
	byID := make(map[string]AuditLogListItem, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	ordered := make([]AuditLogListItem, 0, len(items))
	for _, id := range unique {
		if item, ok := byID[id]; ok {
			ordered = append(ordered, item)
		}
	}
	return ordered, nil
}

// auditLogIDChunkSize mirrors the Node 900-parameter IN chunk.
const auditLogIDChunkSize = 900

func toAnySlice(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

// GetAuditLogPayload mirrors getAuditLogPayload(id, payloadId, { full: true }).
// nil (not found) maps to the route 404 审计原文不存在; blob size mismatches
// stay opaque internal failures like the Node throw.
func (s *auditLogSQLReader) GetAuditLogPayload(ctx context.Context, auditLogID string, payloadID string) (*AuditLogPayloadDetail, error) {
	logID := strings.TrimSpace(auditLogID)
	payloadKey := strings.TrimSpace(payloadID)
	if logID == "" || payloadKey == "" {
		return nil, nil
	}
	query := `SELECT refs.* FROM ` + s.mode.table("audit_payload_refs") + ` refs ` +
		`WHERE refs.audit_log_id = ? AND refs.id = ? AND EXISTS (` +
		`SELECT 1 FROM ` + s.mode.table("audit_logs") + ` al WHERE al.id = refs.audit_log_id AND ` +
		s.persistedTrafficClause("al") + `)`
	params := append([]any{logID, payloadKey},
		auditLogNonPersistedTrafficSources[0], auditLogNonPersistedTrafficSources[1], auditLogNonPersistedTrafficSources[2])
	row, err := readQueryOneMap(ctx, s.db, s.mode, query, params...)
	if err != nil || row == nil {
		return nil, err
	}
	summary := auditLogPayloadSummaryFromRow(row)
	detail := &AuditLogPayloadDetail{
		AuditLogPayloadSummary: summary,
		// full:true always includes headers when a header blob is attached.
		HeadersIncluded:      summary.HasHeaders,
		HeadersStorageStatus: auditPayloadStatusNotSaved,
		BodyStorageStatus:    auditPayloadStatusNotSaved,
	}
	if headerBlobID := readOptionalText(row["headers_blob_id"]); headerBlobID != "" {
		window, windowErr := s.readBlobWindow(ctx, headerBlobID, 0, auditPayloadReadLimit, false)
		if windowErr != nil {
			return nil, windowErr
		}
		detail.HeadersStorageStatus = window.status
		if window.bytes != nil {
			var headers map[string]any
			if jsonErr := json.Unmarshal(window.bytes, &headers); jsonErr == nil && headers != nil {
				detail.Headers = headers
			}
		}
	}
	bodyBlobID := readOptionalText(row["body_blob_id"])
	if bodyBlobID == "" {
		// Node emptyBlobWindow(full ? 0 : ..., requestedLimit, 0, 'not_saved').
		detail.BodyOffset = 0
		detail.BodyLimit = 0
		detail.BodyTotalBytes = 0
		return detail, nil
	}
	window, windowErr := s.readBlobWindow(ctx, bodyBlobID, 0, 0, true)
	if windowErr != nil {
		return nil, windowErr
	}
	detail.BodyStorageStatus = window.status
	detail.BodyOffset = window.offset
	detail.BodyLimit = window.limit
	detail.BodyBytesReturned = int64(len(window.bytes))
	detail.BodyTotalBytes = window.totalBytes
	detail.BodyTruncated = window.truncated
	if window.nextOffset != nil {
		detail.BodyNextOffset = window.nextOffset
	}
	if window.bytes != nil {
		if utf8RoundTrips(window.bytes) {
			text := string(window.bytes)
			detail.BodyText = &text
		} else {
			encoded := base64.StdEncoding.EncodeToString(window.bytes)
			detail.BodyBase64 = &encoded
		}
	}
	return detail, nil
}

// auditBlobWindow mirrors AuditPayloadBlobWindow.
type auditBlobWindow struct {
	bytes      []byte
	offset     int64
	limit      int64
	totalBytes int64
	nextOffset *int64
	truncated  bool
	status     string
}

// emptyAuditBlobWindow mirrors emptyBlobWindow: a full read keeps offset 0
// and derives the limit from rawSize only when the metadata said so.
func emptyAuditBlobWindow(full bool, requestedOffset, requestedLimit, totalBytes int64, status string) auditBlobWindow {
	if full {
		return auditBlobWindow{limit: requestedLimit, totalBytes: totalBytes, status: status}
	}
	return auditBlobWindow{offset: requestedOffset, limit: requestedLimit, totalBytes: totalBytes, status: status}
}

// readBlobWindow mirrors readBlobWindow: metadata lookup in
// audit_payload_blobs, blob file read under the payload root, gzip decode,
// and the Node size validations. full reads ignore requestedOffset/Limit and
// surface the whole raw body; a bounded read caps the window (header read).
func (s *auditLogSQLReader) readBlobWindow(ctx context.Context, blobID string, requestedOffset, requestedLimit int64, full bool) (auditBlobWindow, error) {
	if blobID == "" {
		return emptyAuditBlobWindow(full, requestedOffset, requestedLimit, 0, auditPayloadStatusNotSaved), nil
	}
	row, err := readQueryOneMap(ctx, s.db, s.mode,
		`SELECT storage_key, compression, raw_size_bytes, compressed_size_bytes FROM `+s.mode.table("audit_payload_blobs")+` WHERE id = ?`, blobID)
	if err != nil {
		return auditBlobWindow{}, err
	}
	if row == nil {
		return emptyAuditBlobWindow(full, requestedOffset, requestedLimit, 0, auditPayloadStatusMetadataMissing), nil
	}
	storageKey := readOptionalText(row["storage_key"])
	rawSize := max(int64(0), readNumberOr(row["raw_size_bytes"], 0))
	compressedSize := max(int64(0), readNumberOr(row["compressed_size_bytes"], 0))
	if storageKey == "" || s.blobDir == "" {
		limit := requestedLimit
		if full {
			limit = rawSize
		}
		return emptyAuditBlobWindow(full, requestedOffset, limit, rawSize, auditPayloadStatusFileMissing), nil
	}
	path, err := resolveBlobPath(s.blobDir, storageKey)
	if err != nil {
		return auditBlobWindow{}, err
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			limit := requestedLimit
			if full {
				limit = rawSize
			}
			return emptyAuditBlobWindow(full, requestedOffset, limit, rawSize, auditPayloadStatusFileMissing), nil
		}
		return auditBlobWindow{}, err
	}
	if compressedSize > 0 && int64(len(stored)) != compressedSize {
		return auditBlobWindow{}, fmt.Errorf("F3 审计 payload blob 文件尺寸与 metadata 不一致：id=%s, expected=%d, actual=%d", blobID, compressedSize, len(stored))
	}
	switch compression := readOptionalText(row["compression"]); compression {
	case "", "none":
		// stored as-is
	case "gzip":
		reader, gzipErr := gzip.NewReader(bytes.NewReader(stored))
		if gzipErr != nil {
			return auditBlobWindow{}, gzipErr
		}
		decoded, decodeErr := io.ReadAll(reader)
		closeErr := reader.Close()
		if decodeErr != nil {
			return auditBlobWindow{}, decodeErr
		}
		if closeErr != nil {
			return auditBlobWindow{}, closeErr
		}
		stored = decoded
	default:
		return auditBlobWindow{}, errors.New("F3 审计 payload 使用未知压缩方式：" + compression)
	}
	if rawSize > 0 && int64(len(stored)) != rawSize {
		return auditBlobWindow{}, fmt.Errorf("F3 审计 payload 解压后尺寸与 metadata 不一致：id=%s, expected=%d, actual=%d", blobID, rawSize, len(stored))
	}
	offset := requestedOffset
	limit := requestedLimit
	if full {
		offset = 0
		limit = rawSize
	}
	end := min(int64(len(stored)), offset+limit)
	window := stored[offset:end]
	next := offset + int64(len(window))
	truncated := next < rawSize
	out := auditBlobWindow{
		bytes:      window,
		offset:     offset,
		limit:      limit,
		totalBytes: rawSize,
		truncated:  truncated,
		status:     auditPayloadStatusAvailable,
	}
	if !full && truncated && len(window) > 0 {
		out.nextOffset = &next
	}
	return out, nil
}

// resolveBlobPath mirrors resolveBlobPath: the storage key must stay inside
// the dedicated blob directory.
func resolveBlobPath(root, storageKey string) (string, error) {
	normalizedRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	target, err := filepath.Abs(filepath.Join(normalizedRoot, filepath.FromSlash(storageKey)))
	if err != nil {
		return "", err
	}
	if target != normalizedRoot && !strings.HasPrefix(target, normalizedRoot+string(filepath.Separator)) {
		return "", errors.New("F3 审计 payload storage_key 越出专用 blob 目录")
	}
	return target, nil
}

// utf8RoundTrips mirrors Buffer.from(text,'utf8').equals(bytes): the UTF-8
// decode is lossless exactly when the bytes are valid UTF-8 (invalid
// sequences would be replaced with U+FFFD by both runtimes).
func utf8RoundTrips(bytes []byte) bool {
	return utf8.Valid(bytes)
}

// handleAuditLogSubtree dispatches the audit-logs paths that cannot be
// registered as ServeMux patterns next to the literal error-groups family:
// /{id}, /{id}/payloads/{payloadId} and /error-groups/{id}/events, in the
// Node registration order. Unknown shapes answer the Node 404 contract.
func (d *ReadsDeps) handleAuditLogSubtree(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, auditSubtreePrefix)
	if rest == "" {
		// Node's router tolerates the trailing slash on the list route.
		d.handleListAuditLogs(w, r)
		return
	}
	segments := strings.Split(rest, "/")
	switch {
	case len(segments) == 3 && segments[0] == "error-groups" && segments[2] == "events":
		r.SetPathValue("id", segments[1])
		d.handleListAuditErrorGroupEvents(w, r)
	case len(segments) == 1:
		r.SetPathValue("id", segments[0])
		d.handleGetAuditLogDetail(w, r)
	case len(segments) == 3 && segments[1] == "payloads":
		r.SetPathValue("id", segments[0])
		r.SetPathValue("payloadId", segments[2])
		d.handleGetAuditLogPayload(w, r)
	default:
		kernel.WriteNotFound(w, "资源不存在")
	}
}

// auditSubtreePrefix is the registered subtree pattern minus the trailing
// slash (kernel Mount prefix /__aisys__/api/audit-logs/).
const auditSubtreePrefix = "/__aisys__/api/audit-logs/"

func (d *ReadsDeps) handleGetAuditLogPayload(w http.ResponseWriter, r *http.Request) {
	detail, err := d.Audit.GetAuditLogPayload(r.Context(), r.PathValue("id"), r.PathValue("payloadId"))
	if readWriteStoreError(w, err) {
		return
	}
	if detail == nil {
		kernel.WriteNotFound(w, "审计原文不存在")
		return
	}
	kernel.WriteOK(w, detail, "")
}
