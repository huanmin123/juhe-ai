package managementauditlogs

import (
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	defaultPageSize              = 100
	maxPageSize                  = 100
	maxListWindowRows            = 1001
	defaultPayloadReadLimitBytes = 256 * 1024
	maxPayloadReadLimitBytes     = 1024 * 1024
	maxGzipPayloadBlobRawBytes   = 1024 * 1024
	maxAuditPayloadBlobRawBytes  = 2 * 1024 * 1024
)

var auditPayloadReadSlots = make(chan struct{}, 4)

type Service struct {
	store           port.ManagementAuditLogReader
	payloadBlobRoot string
}

type Options struct {
	PayloadBlobRoot string
}

type ListInput struct {
	TraceID, ErrorGroupID, Outcome, Path, Model, SystemAccountID string
	APIKeyID, GroupID, AccountID, ClientIP, StartAt, EndAt       string
	TrafficSource                                                string
	StatusCode, Page, PageSize                                   int
	PageSizeProvided                                             bool
}

type ListResult struct {
	Items    []Summary `json:"items"`
	Total    int       `json:"total"`
	HasMore  bool      `json:"hasMore"`
	Page     int       `json:"page"`
	PageSize int       `json:"pageSize"`
}

type Summary struct {
	ID                     string `json:"id"`
	TraceID                string `json:"traceId"`
	TrafficSource          string `json:"trafficSource"`
	SystemAccountID        string `json:"systemAccountId,omitempty"`
	SystemAccountName      string `json:"systemAccountName,omitempty"`
	APIKeyID               string `json:"apiKeyId,omitempty"`
	APIKeyName             string `json:"apiKeyName,omitempty"`
	GroupID                string `json:"groupId,omitempty"`
	GroupName              string `json:"groupName,omitempty"`
	AccountID              string `json:"accountId,omitempty"`
	AccountName            string `json:"accountName,omitempty"`
	ProviderCode           string `json:"providerCode,omitempty"`
	Method                 string `json:"method"`
	Path                   string `json:"path"`
	QueryString            string `json:"queryString,omitempty"`
	Model                  string `json:"model,omitempty"`
	UpstreamModel          string `json:"upstreamModel,omitempty"`
	PricingModel           string `json:"pricingModel,omitempty"`
	ModelMappingApplied    bool   `json:"modelMappingApplied"`
	ModelMappingSource     string `json:"modelMappingSource,omitempty"`
	SourceEndpointFamily   string `json:"sourceEndpointFamily,omitempty"`
	UpstreamEndpointFamily string `json:"upstreamEndpointFamily,omitempty"`
	Stream                 bool   `json:"stream"`
	ClientIP               string `json:"clientIp,omitempty"`
	UserAgent              string `json:"userAgent,omitempty"`
	AuditOutcome           string `json:"auditOutcome"`
	Success                bool   `json:"success"`
	FinalStatusCode        *int   `json:"finalStatusCode,omitempty"`
	ErrorPhase             string `json:"errorPhase,omitempty"`
	ErrorCode              string `json:"errorCode,omitempty"`
	ErrorMessage           string `json:"errorMessage,omitempty"`
	SampleBucket           int    `json:"sampleBucket"`
	SampleReason           string `json:"sampleReason"`
	AttemptCount           int    `json:"attemptCount"`
	PayloadCount           int    `json:"payloadCount"`
	RawPayloadBytes        int64  `json:"rawPayloadBytes"`
	CompressedPayloadBytes int64  `json:"compressedPayloadBytes"`
	CompressionSavedBytes  int64  `json:"compressionSavedBytes"`
	ErrorGroupID           string `json:"errorGroupId,omitempty"`
	CaptureStatus          string `json:"captureStatus"`
	StartedAt              string `json:"startedAt"`
	EndedAt                string `json:"endedAt"`
	DurationMs             *int64 `json:"durationMs,omitempty"`
	HTTPCompletedAt        string `json:"httpCompletedAt,omitempty"`
	HTTPDurationMs         *int64 `json:"httpDurationMs,omitempty"`
	FirstTokenMs           *int64 `json:"firstTokenMs,omitempty"`
	CreatedAt              string `json:"createdAt"`
}

type Attempt struct {
	ID                          string `json:"id"`
	AttemptIndex                int    `json:"attemptIndex"`
	AccountID                   string `json:"accountId,omitempty"`
	AccountName                 string `json:"accountName,omitempty"`
	AccountOwnerSystemAccountID string `json:"accountOwnerSystemAccountId,omitempty"`
	GroupID                     string `json:"groupId,omitempty"`
	GroupName                   string `json:"groupName,omitempty"`
	ProxyURL                    string `json:"proxyUrl,omitempty"`
	ProviderCode                string `json:"providerCode,omitempty"`
	Model                       string `json:"model,omitempty"`
	UpstreamModel               string `json:"upstreamModel,omitempty"`
	PricingModel                string `json:"pricingModel,omitempty"`
	ModelMappingApplied         bool   `json:"modelMappingApplied"`
	ModelMappingSource          string `json:"modelMappingSource,omitempty"`
	SourceEndpointFamily        string `json:"sourceEndpointFamily,omitempty"`
	UpstreamEndpointFamily      string `json:"upstreamEndpointFamily,omitempty"`
	UpstreamMethod              string `json:"upstreamMethod"`
	UpstreamURL                 string `json:"upstreamUrl"`
	UpstreamStatusCode          *int   `json:"upstreamStatusCode,omitempty"`
	Success                     bool   `json:"success"`
	ErrorPhase                  string `json:"errorPhase,omitempty"`
	ErrorCode                   string `json:"errorCode,omitempty"`
	ErrorMessage                string `json:"errorMessage,omitempty"`
	StartedAt                   string `json:"startedAt"`
	EndedAt                     string `json:"endedAt,omitempty"`
	DurationMs                  *int64 `json:"durationMs,omitempty"`
}

type PayloadSummary struct {
	ID                  string `json:"id"`
	AttemptID           string `json:"attemptId,omitempty"`
	PartType            string `json:"partType"`
	SequenceIndex       int    `json:"sequenceIndex"`
	ContentType         string `json:"contentType,omitempty"`
	ContentEncoding     string `json:"contentEncoding,omitempty"`
	HeadersSHA256       string `json:"headersSha256,omitempty"`
	BodySHA256          string `json:"bodySha256,omitempty"`
	SizeBytes           int64  `json:"sizeBytes"`
	CompressedSizeBytes int64  `json:"compressedSizeBytes"`
	CaptureStatus       string `json:"captureStatus"`
	CreatedAt           string `json:"createdAt"`
	HasHeaders          bool   `json:"hasHeaders"`
	HasBody             bool   `json:"hasBody"`
}

type ErrorGroup struct {
	ID                 string `json:"id"`
	Fingerprint        string `json:"fingerprint"`
	WindowStartedAt    string `json:"windowStartedAt"`
	WindowEndedAt      string `json:"windowEndedAt"`
	SystemAccountID    string `json:"systemAccountId,omitempty"`
	SystemAccountName  string `json:"systemAccountName,omitempty"`
	APIKeyID           string `json:"apiKeyId,omitempty"`
	APIKeyName         string `json:"apiKeyName,omitempty"`
	GroupID            string `json:"groupId,omitempty"`
	GroupName          string `json:"groupName,omitempty"`
	AccountID          string `json:"accountId,omitempty"`
	AccountName        string `json:"accountName,omitempty"`
	ProviderCode       string `json:"providerCode,omitempty"`
	Path               string `json:"path,omitempty"`
	Model              string `json:"model,omitempty"`
	StatusCode         *int   `json:"statusCode,omitempty"`
	ErrorPhase         string `json:"errorPhase,omitempty"`
	ErrorCode          string `json:"errorCode,omitempty"`
	ErrorType          string `json:"errorType,omitempty"`
	RequestFingerprint string `json:"requestFingerprint,omitempty"`
	ErrorFingerprint   string `json:"errorFingerprint,omitempty"`
	Count              int    `json:"count"`
	FirstEventID       string `json:"firstEventId,omitempty"`
	LastEventID        string `json:"lastEventId,omitempty"`
	SampleEventID      string `json:"sampleEventId,omitempty"`
	LastMessage        string `json:"lastMessage,omitempty"`
	CreatedAt          string `json:"createdAt"`
	UpdatedAt          string `json:"updatedAt"`
}

type Detail struct {
	Summary
	Attempts   []Attempt        `json:"attempts"`
	ErrorGroup *ErrorGroup      `json:"errorGroup,omitempty"`
	Payloads   []PayloadSummary `json:"payloads"`
}

type PayloadInput struct{ Offset, Limit int64 }

type PayloadDetail struct {
	PayloadSummary
	Headers              map[string]any `json:"headers,omitempty"`
	BodyText             string         `json:"bodyText,omitempty"`
	BodyBase64           string         `json:"bodyBase64,omitempty"`
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

func NewService(store port.ManagementAuditLogReader) *Service {
	return NewServiceWithOptions(store, Options{})
}
func NewServiceWithOptions(store port.ManagementAuditLogReader, options Options) *Service {
	return &Service{store: store, payloadBlobRoot: strings.TrimSpace(options.PayloadBlobRoot)}
}

func (s *Service) List(ctx context.Context, input ListInput) (ListResult, error) {
	if s.store == nil {
		return ListResult{}, fmt.Errorf("management audit log reader is required")
	}
	pageSize := defaultPageSize
	if input.PageSizeProvided {
		pageSize = min(max(input.PageSize, 1), maxPageSize)
	}
	page := min(max(input.Page, 1), max(1, (maxListWindowRows-1)/pageSize))
	result, err := s.store.ListManagementAuditLogs(ctx, port.ManagementAuditLogListInput{
		TraceID: trim(input.TraceID), ErrorGroupID: trim(input.ErrorGroupID), Outcome: normalizeOutcome(input.Outcome),
		StatusCode: normalizeStatusCode(input.StatusCode), Path: normalizePath(input.Path), Model: trim(input.Model),
		SystemAccountID: trim(input.SystemAccountID), APIKeyID: trim(input.APIKeyID), GroupID: trim(input.GroupID),
		AccountID: trim(input.AccountID), ClientIP: trim(input.ClientIP), StartAt: trim(input.StartAt), EndAt: trim(input.EndAt),
		TrafficSource: normalizeTrafficSource(input.TrafficSource), Limit: pageSize, Offset: (page - 1) * pageSize,
	})
	if err != nil {
		return ListResult{}, err
	}
	items := make([]Summary, 0, len(result.Items))
	for _, row := range result.Items {
		item, err := summary(row)
		if err != nil {
			return ListResult{}, err
		}
		items = append(items, item)
	}
	total := (page-1)*pageSize + len(items)
	if result.HasMore {
		total++
	}
	return ListResult{Items: items, Total: total, HasMore: result.HasMore, Page: page, PageSize: pageSize}, nil
}

func (s *Service) Detail(ctx context.Context, id string) (Detail, bool, error) {
	if s.store == nil {
		return Detail{}, false, fmt.Errorf("management audit log reader is required")
	}
	row, found, err := s.store.GetManagementAuditLog(ctx, id)
	if err != nil || !found {
		return Detail{}, found, err
	}
	summaryResult, err := summary(row.ManagementAuditLogSummary)
	if err != nil {
		return Detail{}, false, err
	}
	result := Detail{
		Summary:  summaryResult,
		Attempts: make([]Attempt, 0, len(row.Attempts)),
		Payloads: make([]PayloadSummary, 0, len(row.Payloads)),
	}
	for _, attempt := range row.Attempts {
		result.Attempts = append(result.Attempts, Attempt{
			ID: attempt.ID, AttemptIndex: attempt.AttemptIndex,
			AccountID: text(attempt.AccountID), AccountName: text(attempt.AccountName), AccountOwnerSystemAccountID: text(attempt.AccountOwnerSystemAccountID),
			GroupID: text(attempt.GroupID), GroupName: text(attempt.GroupName), ProxyURL: trimURL(attempt.ProxyURL), ProviderCode: text(attempt.ProviderCode),
			Model: text(attempt.Model), UpstreamModel: text(attempt.UpstreamModel), PricingModel: text(attempt.PricingModel),
			ModelMappingApplied: attempt.ModelMappingApplied, ModelMappingSource: text(attempt.ModelMappingSource),
			SourceEndpointFamily: text(attempt.SourceEndpointFamily), UpstreamEndpointFamily: text(attempt.UpstreamEndpointFamily),
			UpstreamMethod: attempt.UpstreamMethod, UpstreamURL: trimRequiredURL(attempt.UpstreamURL), UpstreamStatusCode: attempt.UpstreamStatusCode,
			Success: attempt.Success, ErrorPhase: text(attempt.ErrorPhase), ErrorCode: text(attempt.ErrorCode), ErrorMessage: text(attempt.ErrorMessage),
			StartedAt: attempt.StartedAt, EndedAt: text(attempt.EndedAt), DurationMs: attempt.DurationMs,
		})
	}
	if row.ErrorGroup != nil {
		group := row.ErrorGroup
		result.ErrorGroup = &ErrorGroup{
			ID: group.ID, Fingerprint: group.Fingerprint, WindowStartedAt: group.WindowStartedAt, WindowEndedAt: group.WindowEndedAt,
			SystemAccountID: text(group.SystemAccountID), SystemAccountName: text(group.SystemAccountName), APIKeyID: text(group.APIKeyID), APIKeyName: text(group.APIKeyName),
			GroupID: text(group.GroupID), GroupName: text(group.GroupName), AccountID: text(group.AccountID), AccountName: text(group.AccountName),
			ProviderCode: text(group.ProviderCode), Path: text(group.Path), Model: text(group.Model), StatusCode: group.StatusCode,
			ErrorPhase: text(group.ErrorPhase), ErrorCode: text(group.ErrorCode), ErrorType: text(group.ErrorType),
			RequestFingerprint: text(group.RequestFingerprint), ErrorFingerprint: text(group.ErrorFingerprint), Count: group.Count,
			FirstEventID: text(group.FirstEventID), LastEventID: text(group.LastEventID), SampleEventID: text(group.SampleEventID), LastMessage: text(group.LastMessage),
			CreatedAt: group.CreatedAt, UpdatedAt: group.UpdatedAt,
		}
	}
	for _, payload := range row.Payloads {
		result.Payloads = append(result.Payloads, PayloadSummary{
			ID: payload.ID, AttemptID: text(payload.AttemptID), PartType: payload.PartType, SequenceIndex: payload.SequenceIndex,
			ContentType: text(payload.ContentType), ContentEncoding: text(payload.ContentEncoding), HeadersSHA256: text(payload.HeadersSHA256), BodySHA256: text(payload.BodySHA256),
			SizeBytes: payload.SizeBytes, CompressedSizeBytes: payload.CompressedSizeBytes, CaptureStatus: payload.CaptureStatus,
			CreatedAt: payload.CreatedAt, HasHeaders: payload.HasHeaders, HasBody: payload.HasBody,
		})
	}
	return result, true, nil
}

func (s *Service) Payload(ctx context.Context, auditLogID, payloadID string, input PayloadInput) (PayloadDetail, bool, error) {
	if s.store == nil {
		return PayloadDetail{}, false, fmt.Errorf("management audit log reader is required")
	}
	payloadStore, ok := s.store.(port.ManagementAuditLogPayloadReader)
	if !ok {
		return PayloadDetail{}, false, fmt.Errorf("management audit log payload reader is required")
	}
	row, found, err := payloadStore.GetManagementAuditLogPayload(ctx, trim(auditLogID), trim(payloadID))
	if err != nil || !found {
		return PayloadDetail{}, found, err
	}
	offset := max(input.Offset, 0)
	limit := input.Limit
	if limit <= 0 {
		limit = defaultPayloadReadLimitBytes
	}
	limit = min(limit, maxPayloadReadLimitBytes)
	headersStorageStatus := "not_saved"
	if offset == 0 {
		headersStorageStatus = payloadStorageStatus(row.Summary.HasHeaders, row.HeadersBlob)
	}
	result := PayloadDetail{
		PayloadSummary:       payloadSummary(row.Summary),
		HeadersIncluded:      offset == 0,
		HeadersStorageStatus: headersStorageStatus,
		BodyStorageStatus:    payloadStorageStatus(row.Summary.HasBody, row.BodyBlob),
		BodyOffset:           offset,
		BodyLimit:            limit,
	}
	if offset == 0 && row.HeadersBlob != nil {
		headerWindow, err := s.readBlobWindow(ctx, row.HeadersBlob, 0, maxPayloadReadLimitBytes, maxPayloadReadLimitBytes)
		if err != nil {
			return PayloadDetail{}, false, fmt.Errorf("read audit payload headers: %w", err)
		}
		result.HeadersStorageStatus = headerWindow.status
		result.Headers = auditHeaders(headerWindow.bytes)
	}
	if row.BodyBlob == nil {
		return result, true, nil
	}
	bodyWindow, err := s.readBlobWindow(ctx, row.BodyBlob, offset, limit, maxAuditPayloadBlobRawBytes)
	if err != nil {
		return PayloadDetail{}, false, fmt.Errorf("read audit payload body: %w", err)
	}
	result.BodyStorageStatus = bodyWindow.status
	result.BodyTotalBytes = bodyWindow.total
	result.BodyBytesReturned = int64(len(bodyWindow.bytes))
	nextOffset := offset + result.BodyBytesReturned
	result.BodyTruncated = nextOffset < bodyWindow.total
	if result.BodyTruncated && result.BodyBytesReturned > 0 {
		result.BodyNextOffset = &nextOffset
	}
	if len(bodyWindow.bytes) > 0 {
		if utf8.Valid(bodyWindow.bytes) {
			result.BodyText = string(bodyWindow.bytes)
		} else {
			result.BodyBase64 = base64.StdEncoding.EncodeToString(bodyWindow.bytes)
		}
	}
	return result, true, nil
}

type payloadBlobWindow struct {
	bytes  []byte
	total  int64
	status string
}

func (s *Service) readBlobWindow(ctx context.Context, blob *port.ManagementAuditPayloadBlob, offset, limit, maxRawBytes int64) (payloadBlobWindow, error) {
	if blob.RawSizeBytes < 0 || blob.RawSizeBytes > maxRawBytes {
		return payloadBlobWindow{}, fmt.Errorf("invalid raw payload size: %d", blob.RawSizeBytes)
	}
	if blob.CompressedSizeBytes < 0 || blob.CompressedSizeBytes > maxAuditPayloadBlobRawBytes+maxPayloadReadLimitBytes {
		return payloadBlobWindow{}, fmt.Errorf("invalid compressed payload size: %d", blob.CompressedSizeBytes)
	}
	if blob.Compression != "none" && blob.Compression != "gzip" {
		return payloadBlobWindow{}, fmt.Errorf("unsupported payload compression: %q", blob.Compression)
	}
	if blob.Compression == "gzip" && blob.RawSizeBytes > maxGzipPayloadBlobRawBytes {
		return payloadBlobWindow{}, fmt.Errorf("gzip payload blob exceeds writer compression boundary")
	}
	if blob.Compression == "none" && blob.RawSizeBytes != blob.CompressedSizeBytes {
		return payloadBlobWindow{}, fmt.Errorf("plain payload blob size mismatch")
	}
	if strings.TrimSpace(s.payloadBlobRoot) == "" {
		return payloadBlobWindow{}, fmt.Errorf("audit payload blob root is required")
	}
	select {
	case auditPayloadReadSlots <- struct{}{}:
		defer func() { <-auditPayloadReadSlots }()
	case <-ctx.Done():
		return payloadBlobWindow{}, ctx.Err()
	}
	file, found, err := openAuditPayloadBlob(s.payloadBlobRoot, blob.StorageKey)
	if err != nil {
		return payloadBlobWindow{}, err
	}
	if !found {
		return payloadBlobWindow{total: blob.RawSizeBytes, status: "file_missing"}, nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return payloadBlobWindow{}, fmt.Errorf("stat payload blob: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() != blob.CompressedSizeBytes {
		return payloadBlobWindow{}, fmt.Errorf("payload blob size mismatch")
	}
	if offset >= blob.RawSizeBytes || limit <= 0 {
		return payloadBlobWindow{total: blob.RawSizeBytes, status: "available"}, nil
	}
	select {
	case <-ctx.Done():
		return payloadBlobWindow{}, ctx.Err()
	default:
	}
	var bytes []byte
	if blob.Compression == "gzip" {
		bytes, err = readGzipAuditPayloadWindow(ctx, file, blob.RawSizeBytes, offset, limit)
	} else {
		bytes, err = readPlainAuditPayloadWindow(file, blob.RawSizeBytes, offset, limit)
	}
	if err != nil {
		return payloadBlobWindow{}, err
	}
	return payloadBlobWindow{bytes: bytes, total: blob.RawSizeBytes, status: "available"}, nil
}

func openAuditPayloadBlob(root, storageKey string) (*os.File, bool, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, false, fmt.Errorf("resolve audit payload root: %w", err)
	}
	key := filepath.FromSlash(storageKey)
	if strings.TrimSpace(key) == "" || filepath.IsAbs(key) || filepath.Clean(key) == "." {
		return nil, false, fmt.Errorf("invalid audit payload storage path")
	}
	targetAbs, err := filepath.Abs(filepath.Join(rootAbs, key))
	if err != nil || !pathWithin(rootAbs, targetAbs) {
		return nil, false, fmt.Errorf("invalid audit payload storage path")
	}
	if _, err = os.Stat(targetAbs); os.IsNotExist(err) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, fmt.Errorf("stat audit payload storage path: %w", err)
	}
	rootReal := rootAbs
	if resolved, resolveErr := filepath.EvalSymlinks(rootAbs); resolveErr == nil {
		rootReal = resolved
	}
	targetReal, err := filepath.EvalSymlinks(targetAbs)
	if err != nil {
		return nil, false, fmt.Errorf("resolve audit payload storage path: %w", err)
	}
	if !pathWithin(rootReal, targetReal) {
		return nil, false, fmt.Errorf("invalid audit payload storage path")
	}
	file, err := os.Open(targetReal)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("open audit payload blob: %w", err)
	}
	return file, true, nil
}

func pathWithin(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func readPlainAuditPayloadWindow(file *os.File, total, offset, limit int64) ([]byte, error) {
	want := min(limit, total-offset)
	bytes := make([]byte, want)
	n, err := file.ReadAt(bytes, offset)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("read payload blob: %w", err)
	}
	if int64(n) != want {
		return nil, fmt.Errorf("payload blob ended before declared size")
	}
	return bytes, nil
}

func readGzipAuditPayloadWindow(ctx context.Context, file *os.File, total, offset, limit int64) ([]byte, error) {
	reader, err := gzip.NewReader(file)
	if err != nil {
		return nil, fmt.Errorf("open gzip payload blob: %w", err)
	}
	defer reader.Close()
	bounded := io.LimitReader(reader, total+1)
	if offset > 0 {
		copied, err := io.CopyN(io.Discard, &contextReader{ctx: ctx, reader: bounded}, offset)
		if err != nil || copied != offset {
			return nil, fmt.Errorf("gzip payload ended before declared offset")
		}
	}
	want := min(limit, total-offset)
	bytes, err := io.ReadAll(io.LimitReader(&contextReader{ctx: ctx, reader: bounded}, want))
	if err != nil {
		return nil, fmt.Errorf("read gzip payload blob: %w", err)
	}
	if int64(len(bytes)) != want {
		return nil, fmt.Errorf("gzip payload ended before declared size")
	}
	if offset+want == total {
		var extra [1]byte
		n, tailErr := (&contextReader{ctx: ctx, reader: bounded}).Read(extra[:])
		if n != 0 || (tailErr != nil && tailErr != io.EOF) {
			return nil, fmt.Errorf("gzip payload exceeds declared size")
		}
	}
	return bytes, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(bytes []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.reader.Read(bytes)
	}
}

func auditHeaders(bytes []byte) map[string]any {
	if len(bytes) == 0 {
		return nil
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(bytes, &raw) != nil {
		return nil
	}
	result := make(map[string]any, len(raw))
	for key, value := range raw {
		var single string
		if json.Unmarshal(value, &single) == nil {
			result[key] = single
			continue
		}
		var multiple []string
		if json.Unmarshal(value, &multiple) != nil {
			return nil
		}
		result[key] = multiple
	}
	return result
}

func payloadStorageStatus(saved bool, blob *port.ManagementAuditPayloadBlob) string {
	if !saved {
		return "not_saved"
	}
	if blob == nil {
		return "metadata_missing"
	}
	return "available"
}

func payloadSummary(row port.ManagementAuditLogPayloadSummary) PayloadSummary {
	return PayloadSummary{
		ID: row.ID, AttemptID: text(row.AttemptID), PartType: row.PartType, SequenceIndex: row.SequenceIndex,
		ContentType: text(row.ContentType), ContentEncoding: text(row.ContentEncoding), HeadersSHA256: text(row.HeadersSHA256), BodySHA256: text(row.BodySHA256),
		SizeBytes: row.SizeBytes, CompressedSizeBytes: row.CompressedSizeBytes, CaptureStatus: row.CaptureStatus,
		CreatedAt: row.CreatedAt, HasHeaders: row.HasHeaders, HasBody: row.HasBody,
	}
}

func summary(row port.ManagementAuditLogSummary) (Summary, error) {
	if !validTrafficSource(row.TrafficSource) {
		return Summary{}, fmt.Errorf("invalid management audit log traffic source: %q", row.TrafficSource)
	}
	return Summary{
		ID: row.ID, TraceID: row.TraceID, TrafficSource: row.TrafficSource,
		SystemAccountID: text(row.SystemAccountID), SystemAccountName: text(row.SystemAccountName), APIKeyID: text(row.APIKeyID), APIKeyName: text(row.APIKeyName),
		GroupID: text(row.GroupID), GroupName: text(row.GroupName), AccountID: text(row.AccountID), AccountName: text(row.AccountName), ProviderCode: text(row.ProviderCode),
		Method: row.Method, Path: row.Path, QueryString: text(row.QueryString), Model: text(row.Model), UpstreamModel: text(row.UpstreamModel), PricingModel: text(row.PricingModel),
		ModelMappingApplied: row.ModelMappingApplied, ModelMappingSource: text(row.ModelMappingSource), SourceEndpointFamily: text(row.SourceEndpointFamily), UpstreamEndpointFamily: text(row.UpstreamEndpointFamily),
		Stream: row.Stream, ClientIP: text(row.ClientIP), UserAgent: text(row.UserAgent), AuditOutcome: row.AuditOutcome, Success: row.Success, FinalStatusCode: row.FinalStatusCode,
		ErrorPhase: text(row.ErrorPhase), ErrorCode: text(row.ErrorCode), ErrorMessage: text(row.ErrorMessage), SampleBucket: row.SampleBucket, SampleReason: row.SampleReason,
		AttemptCount: row.AttemptCount, PayloadCount: row.PayloadCount, RawPayloadBytes: row.RawPayloadBytes, CompressedPayloadBytes: row.CompressedPayloadBytes,
		CompressionSavedBytes: row.CompressionSavedBytes, ErrorGroupID: text(row.ErrorGroupID), CaptureStatus: row.CaptureStatus, StartedAt: row.StartedAt, EndedAt: row.EndedAt,
		DurationMs: row.DurationMs, HTTPCompletedAt: text(row.HTTPCompletedAt), HTTPDurationMs: row.HTTPDurationMs, FirstTokenMs: row.FirstTokenMs, CreatedAt: row.CreatedAt,
	}, nil
}

func validTrafficSource(value string) bool {
	switch value {
	case "gateway", "manual_account_test", "account_health_check", "runtime_recovery_probe", "cooldown_retest", "hybrid_scoring", "hybrid_quality_scoring":
		return true
	default:
		return false
	}
}

func trimURL(value *string) string {
	return trim(text(value))
}

func trimRequiredURL(value string) string {
	trimmed := trim(value)
	if trimmed == "" {
		return value
	}
	return trimmed
}

func normalizeOutcome(value string) string {
	switch trim(value) {
	case "success", "success_after_retry", "gateway_succeeded", "gateway_failed", "upstream_failed", "stream_failed", "client_aborted":
		return trim(value)
	default:
		return ""
	}
}
func normalizeTrafficSource(value string) string {
	switch trim(value) {
	case "gateway", "manual_account_test", "account_health_check", "runtime_recovery_probe", "cooldown_retest", "hybrid_scoring", "hybrid_quality_scoring":
		return trim(value)
	default:
		return ""
	}
}
func normalizeStatusCode(value int) *int {
	if value < 100 || value > 599 {
		return nil
	}
	return &value
}
func normalizePath(value string) string {
	value = trim(value)
	for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"} {
		if len(value) > len(method) && strings.EqualFold(value[:len(method)], method) {
			r, _ := utf8.DecodeRuneInString(value[len(method):])
			if isWhitespace(r) {
				value = strings.TrimLeftFunc(value[len(method):], isWhitespace)
				break
			}
		}
	}
	if i := strings.IndexByte(value, '?'); i >= 0 {
		value = value[:i]
	}
	return trim(value)
}
func text(value *string) string {
	if value == nil || *value == "" {
		return ""
	}
	return *value
}
func trim(value string) string { return strings.TrimFunc(value, isWhitespace) }
func isWhitespace(r rune) bool {
	switch r {
	case '\u0009', '\u000B', '\u000C', '\u0020', '\u00A0', '\u1680', '\u2000', '\u2001', '\u2002', '\u2003', '\u2004', '\u2005', '\u2006', '\u2007', '\u2008', '\u2009', '\u200A', '\u202F', '\u205F', '\u3000', '\uFEFF', '\u000A', '\u000D', '\u2028', '\u2029':
		return true
	default:
		return false
	}
}
