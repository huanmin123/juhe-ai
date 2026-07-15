package managementexternalintegrationsources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/modules/publicapi"
	"juhe-ai/backend-go/internal/secretcrypto"
	"juhe-ai/backend-go/internal/store/port"
)

const (
	defaultPageSize           = 20
	maxPageSize               = 100
	maxListWindowRows         = 1000
	javaScriptISOStringLayout = "2006-01-02T15:04:05.000Z"
)

var ErrInvalidListStatus = errors.New("management external integration source list status invalid")

type Service struct {
	store        port.ManagementExternalIntegrationSourceListReader
	detailStore  port.ManagementExternalIntegrationSourceDetailReader
	secretReader port.ManagementExternalIntegrationSourceTokenSecretReader
	secretCodec  tokenSecretJSONCodec
}

type ServiceOptions struct {
	ListReader   port.ManagementExternalIntegrationSourceListReader
	DetailReader port.ManagementExternalIntegrationSourceDetailReader
	SecretReader port.ManagementExternalIntegrationSourceTokenSecretReader
	Secret       string
}

type tokenSecretJSONCodec interface {
	DecryptJSON(value string) (map[string]any, error)
}

type ListInput struct {
	Page             int
	PageSize         int
	PageSizeProvided bool
	Status           string
	Keyword          string
}

type RateLimitRule struct {
	WindowSeconds int `json:"windowSeconds"`
	MaxRequests   int `json:"maxRequests"`
}

type Token struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	TokenPrefix string   `json:"tokenPrefix"`
	TokenSuffix string   `json:"tokenSuffix"`
	Status      string   `json:"status"`
	Scopes      []string `json:"scopes"`
	ExpiresAt   *string  `json:"expiresAt,omitempty"`
	LastUsedAt  *string  `json:"lastUsedAt,omitempty"`
	CreatedAt   string   `json:"createdAt"`
	UpdatedAt   string   `json:"updatedAt"`
	RevokedAt   *string  `json:"revokedAt,omitempty"`
	IsBuiltIn   bool     `json:"isBuiltIn"`
}

type Source struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Status           string          `json:"status"`
	Scopes           []string        `json:"scopes"`
	RateLimits       []RateLimitRule `json:"rateLimits"`
	ExpiresAt        *string         `json:"expiresAt,omitempty"`
	Notes            *string         `json:"notes,omitempty"`
	LastUsedAt       *string         `json:"lastUsedAt,omitempty"`
	CreatedAt        string          `json:"createdAt"`
	UpdatedAt        string          `json:"updatedAt"`
	TokenCount       int64           `json:"tokenCount"`
	ActiveTokenCount int64           `json:"activeTokenCount"`
	PrimaryToken     *Token          `json:"primaryToken,omitempty"`
	IsBuiltIn        bool            `json:"isBuiltIn"`
}

type ListResult struct {
	Items          []Source `json:"items"`
	Page           int      `json:"page"`
	PageSize       int      `json:"pageSize"`
	PageUpperBound int      `json:"pageUpperBound"`
	HasMore        bool     `json:"hasMore"`
}

type Detail struct {
	Source
	Tokens []Token `json:"tokens"`
}

type TokenSecret struct {
	Token string `json:"token"`
}

func NewService(store port.ManagementExternalIntegrationSourceListReader) *Service {
	options := ServiceOptions{ListReader: store}
	if detailStore, ok := store.(port.ManagementExternalIntegrationSourceDetailReader); ok {
		options.DetailReader = detailStore
	}
	return NewServiceWithOptions(options)
}

func NewServiceWithOptions(options ServiceOptions) *Service {
	secretReader := options.SecretReader
	var secretCodec tokenSecretJSONCodec
	if secretReader != nil && strings.TrimSpace(options.Secret) != "" {
		secretCodec = secretcrypto.NewJSONCodec(options.Secret)
	} else {
		secretReader = nil
	}
	return &Service{
		store:        options.ListReader,
		detailStore:  options.DetailReader,
		secretReader: secretReader,
		secretCodec:  secretCodec,
	}
}

func (s *Service) RevealTokenSecret(ctx context.Context, sourceID string, tokenID string) (*TokenSecret, error) {
	sourceID = trimECMAScriptWhitespace(sourceID)
	tokenID = trimECMAScriptWhitespace(tokenID)
	if sourceID == "" || tokenID == "" {
		return nil, nil
	}
	if s.secretReader == nil {
		return nil, fmt.Errorf("management external integration source token secret reader is required")
	}
	encrypted, found, err := s.secretReader.FindManagementExternalIntegrationSourceTokenSecret(ctx, sourceID, tokenID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	if encrypted == "" {
		return nil, fmt.Errorf("external integration source token ciphertext is missing")
	}
	payload, err := s.secretCodec.DecryptJSON(encrypted)
	if err != nil {
		return nil, fmt.Errorf("decrypt external integration source token secret: %w", err)
	}
	token, ok := payload["token"].(string)
	if !ok || token == "" {
		return nil, fmt.Errorf("external integration source token ciphertext is missing complete token")
	}
	return &TokenSecret{Token: token}, nil
}

func (s *Service) Get(ctx context.Context, id string) (*Detail, error) {
	sourceID := trimECMAScriptWhitespace(id)
	if sourceID == "" {
		return nil, nil
	}
	if s.detailStore == nil {
		return nil, fmt.Errorf("management external integration source detail reader is required")
	}
	row, found, err := s.detailStore.FindManagementExternalIntegrationSource(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	source, err := sourceFromStore(row)
	if err != nil {
		return nil, err
	}
	tokenRows, err := s.detailStore.ListManagementExternalIntegrationSourceTokens(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	tokens := make([]Token, 0, len(tokenRows))
	var activeTokenCount int64
	for _, tokenRow := range tokenRows {
		token, err := tokenFromStore(tokenRow)
		if err != nil {
			return nil, err
		}
		if token.Status == publicapi.TokenStatusActive {
			activeTokenCount++
		}
		tokens = append(tokens, token)
	}
	source.TokenCount = int64(len(tokens))
	source.ActiveTokenCount = activeTokenCount
	source.PrimaryToken = nil
	return &Detail{Source: source, Tokens: tokens}, nil
}

func (s *Service) List(ctx context.Context, input ListInput) (ListResult, error) {
	if s.store == nil {
		return ListResult{}, fmt.Errorf("management external integration source list reader is required")
	}
	status, err := normalizeListStatus(input.Status)
	if err != nil {
		return ListResult{}, err
	}
	pageSize := normalizePageSize(input.PageSize, input.PageSizeProvided || input.PageSize != 0)
	page := normalizePage(input.Page, pageSize)
	offset := (page - 1) * pageSize
	rows, err := s.store.ListManagementExternalIntegrationSources(ctx, port.ManagementExternalIntegrationSourceListInput{
		Status:  status,
		Keyword: strings.ToLower(trimECMAScriptWhitespace(input.Keyword)),
		Limit:   pageSize + 1,
		Offset:  offset,
	})
	if err != nil {
		return ListResult{}, err
	}
	hasMore := len(rows) > pageSize
	if hasMore {
		rows = rows[:pageSize]
	}

	items := make([]Source, 0, len(rows))
	sourceIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		item, err := sourceFromStore(row)
		if err != nil {
			return ListResult{}, err
		}
		items = append(items, item)
		sourceIDs = append(sourceIDs, row.ID)
	}
	if len(items) == 0 {
		return listResult(items, page, pageSize, offset, hasMore), nil
	}

	statsRows, err := s.store.ListManagementExternalIntegrationSourceTokenStats(ctx, sourceIDs)
	if err != nil {
		return ListResult{}, err
	}
	statsBySourceID := make(map[string]port.ManagementExternalIntegrationSourceTokenStatsRow, len(statsRows))
	for _, row := range statsRows {
		statsBySourceID[row.SourceRefID] = row
	}

	primaryRows, err := s.store.ListManagementExternalIntegrationSourcePrimaryTokens(ctx, sourceIDs)
	if err != nil {
		return ListResult{}, err
	}
	pageSourceIDs := make(map[string]struct{}, len(sourceIDs))
	for _, sourceID := range sourceIDs {
		pageSourceIDs[sourceID] = struct{}{}
	}
	primaryBySourceID := make(map[string]Token, len(primaryRows))
	for _, row := range primaryRows {
		if _, belongsToPage := pageSourceIDs[row.SourceRefID]; !belongsToPage {
			continue
		}
		if _, exists := primaryBySourceID[row.SourceRefID]; exists {
			continue
		}
		item, err := tokenFromStore(row)
		if err != nil {
			return ListResult{}, err
		}
		primaryBySourceID[row.SourceRefID] = item
	}

	for index := range items {
		if stats, ok := statsBySourceID[items[index].ID]; ok {
			items[index].TokenCount = stats.TokenCount
			items[index].ActiveTokenCount = stats.ActiveTokenCount
		}
		if primary, ok := primaryBySourceID[items[index].ID]; ok {
			value := primary
			items[index].PrimaryToken = &value
		}
	}
	return listResult(items, page, pageSize, offset, hasMore), nil
}

func listResult(items []Source, page int, pageSize int, offset int, hasMore bool) ListResult {
	pageUpperBound := offset + len(items)
	if hasMore {
		pageUpperBound++
	}
	return ListResult{
		Items:          items,
		Page:           page,
		PageSize:       pageSize,
		PageUpperBound: pageUpperBound,
		HasMore:        hasMore,
	}
}

func sourceFromStore(row port.ManagementExternalIntegrationSourceListRow) (Source, error) {
	status, err := normalizeSourceStatus(row.Status)
	if err != nil {
		return Source{}, fmt.Errorf("management external integration source %q: %w", row.ID, err)
	}
	scopes, err := decodeScopes(row.ScopesJSON)
	if err != nil {
		return Source{}, fmt.Errorf("decode management external integration source %q scopes: %w", row.ID, err)
	}
	rateLimits, err := decodeRateLimits(row.RateLimitsJSON)
	if err != nil {
		return Source{}, fmt.Errorf("decode management external integration source %q rate limits: %w", row.ID, err)
	}
	createdAt, err := formatRequiredTime(row.CreatedAt, "source", row.ID, "createdAt")
	if err != nil {
		return Source{}, err
	}
	updatedAt, err := formatRequiredTime(row.UpdatedAt, "source", row.ID, "updatedAt")
	if err != nil {
		return Source{}, err
	}
	expiresAt, err := formatOptionalTime(row.ExpiresAt, "source", row.ID, "expiresAt")
	if err != nil {
		return Source{}, err
	}
	lastUsedAt, err := formatOptionalTime(row.LastUsedAt, "source", row.ID, "lastUsedAt")
	if err != nil {
		return Source{}, err
	}
	return Source{
		ID:         row.ID,
		Name:       row.Name,
		Status:     status,
		Scopes:     scopes,
		RateLimits: rateLimits,
		ExpiresAt:  expiresAt,
		Notes:      cloneString(row.Notes),
		LastUsedAt: lastUsedAt,
		CreatedAt:  createdAt,
		UpdatedAt:  updatedAt,
		IsBuiltIn:  publicapi.IsBuiltInTestSource(row.ID),
	}, nil
}

func tokenFromStore(row port.ManagementExternalIntegrationSourcePrimaryTokenRow) (Token, error) {
	status, err := normalizeTokenStatus(row.Status)
	if err != nil {
		return Token{}, fmt.Errorf("management external integration source token %q: %w", row.ID, err)
	}
	scopes, err := decodeScopes(row.ScopesJSON)
	if err != nil {
		return Token{}, fmt.Errorf("decode management external integration source token %q scopes: %w", row.ID, err)
	}
	createdAt, err := formatRequiredTime(row.CreatedAt, "token", row.ID, "createdAt")
	if err != nil {
		return Token{}, err
	}
	updatedAt, err := formatRequiredTime(row.UpdatedAt, "token", row.ID, "updatedAt")
	if err != nil {
		return Token{}, err
	}
	expiresAt, err := formatOptionalTime(row.ExpiresAt, "token", row.ID, "expiresAt")
	if err != nil {
		return Token{}, err
	}
	lastUsedAt, err := formatOptionalTime(row.LastUsedAt, "token", row.ID, "lastUsedAt")
	if err != nil {
		return Token{}, err
	}
	revokedAt, err := formatOptionalTime(row.RevokedAt, "token", row.ID, "revokedAt")
	if err != nil {
		return Token{}, err
	}
	return Token{
		ID:          row.ID,
		Name:        row.Name,
		TokenPrefix: row.TokenPrefix,
		TokenSuffix: row.TokenSuffix,
		Status:      status,
		Scopes:      scopes,
		ExpiresAt:   expiresAt,
		LastUsedAt:  lastUsedAt,
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
		RevokedAt:   revokedAt,
		IsBuiltIn:   publicapi.IsBuiltInTestToken(row.ID),
	}, nil
}

func normalizeListStatus(value string) (string, error) {
	status := strings.TrimSpace(value)
	if status == "" {
		return "all", nil
	}
	switch status {
	case "all", publicapi.SourceStatusActive, publicapi.SourceStatusDisabled:
		return status, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidListStatus, status)
	}
}

func normalizeSourceStatus(value string) (string, error) {
	switch value {
	case publicapi.SourceStatusActive, publicapi.SourceStatusDisabled:
		return value, nil
	default:
		return "", fmt.Errorf("来源系统状态无效: %q", value)
	}
}

func normalizeTokenStatus(value string) (string, error) {
	switch value {
	case publicapi.TokenStatusActive, publicapi.TokenStatusDisabled, publicapi.TokenStatusRevoked:
		return value, nil
	default:
		return "", fmt.Errorf("来源系统 token 状态无效: %q", value)
	}
}

func normalizePageSize(value int, provided bool) int {
	if !provided {
		return defaultPageSize
	}
	return min(max(1, value), maxPageSize)
}

func normalizePage(value int, pageSize int) int {
	pageUpperBound := max(1, maxListWindowRows/pageSize)
	return min(max(1, value), pageUpperBound)
}

var supportedScopes = func() map[string]struct{} {
	options := publicapi.ScopeOptions()
	values := make(map[string]struct{}, len(options))
	for _, option := range options {
		values[option.Value] = struct{}{}
	}
	return values
}()

func decodeScopes(raw string) ([]string, error) {
	value, err := decodeJSON(raw)
	if err != nil {
		return nil, err
	}
	input, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("来源系统 scopes_json 必须是数组")
	}
	seen := make(map[string]struct{}, len(input))
	values := make([]string, 0, len(input))
	for _, item := range input {
		scope, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("来源系统 scopes_json 必须是字符串数组")
		}
		scope = strings.TrimSpace(scope)
		if _, supported := supportedScopes[scope]; !supported {
			continue
		}
		if _, exists := seen[scope]; exists {
			continue
		}
		seen[scope] = struct{}{}
		values = append(values, scope)
	}
	sort.Strings(values)
	return values, nil
}

func decodeRateLimits(raw string) ([]RateLimitRule, error) {
	value, err := decodeJSON(raw)
	if err != nil {
		return nil, err
	}
	input, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("来源系统 rate_limits_json 必须是数组")
	}
	if len(input) > 8 {
		return nil, fmt.Errorf("来源系统限频规则最多 8 条")
	}
	seenWindows := make(map[int]struct{}, len(input))
	values := make([]RateLimitRule, 0, len(input))
	for _, item := range input {
		record, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("来源系统限频规则必须是对象")
		}
		if len(record) != 2 {
			return nil, fmt.Errorf("来源系统限频规则只能包含 windowSeconds 和 maxRequests")
		}
		windowValue, hasWindow := record["windowSeconds"]
		maxRequestsValue, hasMaxRequests := record["maxRequests"]
		if !hasWindow || !hasMaxRequests {
			return nil, fmt.Errorf("来源系统限频规则只能包含 windowSeconds 和 maxRequests")
		}
		windowSeconds, err := normalizeJSONInteger(windowValue, 1, 86_400, "来源系统限频窗口")
		if err != nil {
			return nil, err
		}
		maxRequests, err := normalizeJSONInteger(maxRequestsValue, 1, 100_000, "来源系统限频次数")
		if err != nil {
			return nil, err
		}
		if _, exists := seenWindows[windowSeconds]; exists {
			return nil, fmt.Errorf("来源系统限频窗口不能重复")
		}
		seenWindows[windowSeconds] = struct{}{}
		values = append(values, RateLimitRule{
			WindowSeconds: windowSeconds,
			MaxRequests:   maxRequests,
		})
	}
	sort.Slice(values, func(i int, j int) bool {
		return values[i].WindowSeconds < values[j].WindowSeconds
	})
	return values, nil
}

func decodeJSON(raw string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("JSON 只能包含一个值")
		}
		return nil, err
	}
	return value, nil
}

func normalizeJSONInteger(value any, minimum int, maximum int, label string) (int, error) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, fmt.Errorf("%s必须是整数", label)
	}
	numeric, err := strconv.ParseFloat(number.String(), 64)
	if err != nil || math.IsInf(numeric, 0) || math.IsNaN(numeric) || math.Trunc(numeric) != numeric {
		return 0, fmt.Errorf("%s必须是整数", label)
	}
	if numeric < float64(minimum) || numeric > float64(maximum) {
		return 0, fmt.Errorf("%s必须在 %d 到 %d 之间", label, minimum, maximum)
	}
	return int(numeric), nil
}

func formatRequiredTime(value time.Time, kind string, id string, field string) (string, error) {
	if value.IsZero() {
		return "", fmt.Errorf("management external integration source %s %q has invalid %s", kind, id, field)
	}
	return formatTime(value), nil
}

func formatOptionalTime(value *time.Time, kind string, id string, field string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	if value.IsZero() {
		return nil, fmt.Errorf("management external integration source %s %q has invalid %s", kind, id, field)
	}
	formatted := formatTime(*value)
	return &formatted, nil
}

func formatTime(value time.Time) string {
	return value.UTC().Truncate(time.Millisecond).Format(javaScriptISOStringLayout)
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func trimECMAScriptWhitespace(value string) string {
	return strings.TrimFunc(value, isECMAScriptWhitespace)
}

func isECMAScriptWhitespace(character rune) bool {
	switch character {
	case '\u0009', '\u000B', '\u000C', '\u0020', '\u00A0', '\u1680',
		'\u2000', '\u2001', '\u2002', '\u2003', '\u2004', '\u2005',
		'\u2006', '\u2007', '\u2008', '\u2009', '\u200A', '\u202F',
		'\u205F', '\u3000', '\uFEFF', '\u000A', '\u000D', '\u2028',
		'\u2029':
		return true
	default:
		return false
	}
}
