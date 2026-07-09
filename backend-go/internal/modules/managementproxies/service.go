package managementproxies

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	defaultListPageSize     = 20
	maxListPageSize         = 200
	defaultListWindow       = 1001
	maxDescriptionRunes     = 200
	defaultCredentialSecret = "juhe-ai-go-development-secret"

	proxyUsagePreviewLimit = 3
	proxyUsageWindowLimit  = proxyUsagePreviewLimit + 1

	ProxyCreatedReason = "proxy_created"
	ProxyUpdatedReason = "proxy_updated"
	ProxyDeletedReason = "proxy_deleted"
)

type Service struct {
	store       port.ManagementProxyReader
	now         func() time.Time
	newID       func(prefix string) string
	codec       CredentialCodec
	invalidator ProxyInvalidator
}

type ServiceOptions struct {
	Store       port.ManagementProxyReader
	Now         func() time.Time
	NewID       func(prefix string) string
	Codec       CredentialCodec
	Secret      string
	Invalidator ProxyInvalidator
}

type CredentialCodec interface {
	EncryptJSON(value map[string]any) (string, error)
}

type ProxyInvalidator interface {
	InvalidateProxyChanged(ctx context.Context, reason string) error
}

var (
	ErrProxyNotFound                = errors.New("management proxy not found")
	ErrProxyCredentialCodecUnusable = errors.New("management proxy credential codec unusable")
)

type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string {
	return e.Message
}

type NameExistsError struct {
	Name string
}

func (e *NameExistsError) Error() string {
	if strings.TrimSpace(e.Name) == "" {
		return "代理名称已存在"
	}
	return "代理名称已存在：" + e.Name
}

type InUseError struct {
	AccountCount             int
	AccountCountIsLowerBound bool
	AccountNames             []string
}

func (e *InUseError) Error() string {
	names := ""
	if len(e.AccountNames) > 0 {
		names = "：" + strings.Join(e.AccountNames, "、")
		if e.AccountCountIsLowerBound || e.AccountCount > len(e.AccountNames) {
			names += " 等"
		}
	}
	countText := fmt.Sprintf("%d", e.AccountCount)
	if e.AccountCountIsLowerBound {
		countText = fmt.Sprintf("至少 %d", e.AccountCount)
	}
	return fmt.Sprintf("这个代理仍被 %s 个账户使用，请先在账户管理中解绑或改绑后再删除%s", countText, names)
}

func ValidationMessage(err error) (string, bool) {
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		return "", false
	}
	if strings.TrimSpace(validationErr.Message) == "" {
		return "代理参数无效", true
	}
	return validationErr.Message, true
}

func NameExistsMessage(err error) (string, bool) {
	var existsErr *NameExistsError
	if !errors.As(err, &existsErr) {
		return "", false
	}
	return existsErr.Error(), true
}

func InUseMessage(err error) (string, bool) {
	var inUseErr *InUseError
	if !errors.As(err, &inUseErr) {
		return "", false
	}
	return inUseErr.Error(), true
}

type ListInput struct {
	Page     int
	PageSize int
	Keyword  string
}

type OptionListInput struct {
	Keyword string
	Limit   int
}

type Summary struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Description     *string    `json:"description,omitempty"`
	Type            string     `json:"type"`
	Host            string     `json:"host"`
	Port            int        `json:"port"`
	Username        *string    `json:"username,omitempty"`
	Enabled         bool       `json:"enabled"`
	TestStatus      string     `json:"testStatus"`
	LatencyMs       *int       `json:"latencyMs,omitempty"`
	OutboundIP      *string    `json:"outboundIp,omitempty"`
	OutboundRegion  *string    `json:"outboundRegion,omitempty"`
	LastTestMessage *string    `json:"lastTestMessage,omitempty"`
	LastTestedAt    *time.Time `json:"lastTestedAt,omitempty"`
}

type ListResult struct {
	Items    []Summary `json:"items"`
	Total    int       `json:"total"`
	HasMore  bool      `json:"hasMore"`
	Page     int       `json:"page"`
	PageSize int       `json:"pageSize"`
}

type Option struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Enabled bool   `json:"enabled"`
}

type OptionalText struct {
	Set   bool
	Value *string
}

type CreateInput struct {
	SystemAccountID string
	Name            string
	Description     *string
	Type            string
	Host            string
	Port            int
	Username        *string
	Password        *string
	Enabled         *bool
}

type CreateResult struct {
	Proxy       Summary `json:"proxy"`
	PasswordSet bool    `json:"passwordSet"`
}

type UpdateInput struct {
	ID          string
	Name        *string
	Description OptionalText
	Type        *string
	Host        *string
	Port        *int
	Username    OptionalText
	Password    *string
	Enabled     *bool
}

type UpdateResult struct {
	Before          Summary `json:"before"`
	Proxy           Summary `json:"proxy"`
	Changed         bool    `json:"changed"`
	PasswordChanged bool    `json:"passwordChanged"`
	ResetTestState  bool    `json:"resetTestState"`
}

type DeleteInput struct {
	ID string
}

type DeleteResult struct {
	Before  Summary `json:"before"`
	Deleted bool    `json:"deleted"`
}

func NewService(store port.ManagementProxyReader) *Service {
	return NewServiceWithOptions(ServiceOptions{Store: store})
}

func NewServiceWithOptions(opts ServiceOptions) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	newID := opts.NewID
	if newID == nil {
		newID = func(prefix string) string {
			return prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		}
	}
	codec := opts.Codec
	if codec == nil {
		secret := strings.TrimSpace(opts.Secret)
		if secret == "" {
			secret = defaultCredentialSecret
		}
		codec = newAESGCMCredentialCodec(secret)
	}
	return &Service{
		store:       opts.Store,
		now:         now,
		newID:       newID,
		codec:       codec,
		invalidator: opts.Invalidator,
	}
}

func (s *Service) List(ctx context.Context, input ListInput) (ListResult, error) {
	if s.store == nil {
		return ListResult{}, fmt.Errorf("management proxy store is required")
	}
	pageSize := normalizeListPageSize(input.PageSize)
	page := normalizeListPage(input.Page, pageSize)
	result, err := s.store.ListManagementProxies(ctx, port.ManagementProxyListInput{
		Keyword: strings.TrimSpace(input.Keyword),
		Limit:   pageSize + 1,
		Offset:  (page - 1) * pageSize,
	})
	if err != nil {
		return ListResult{}, err
	}
	items := make([]Summary, 0, min(len(result.Items), pageSize))
	for index, row := range result.Items {
		if index >= pageSize {
			break
		}
		items = append(items, proxySummaryFromPort(row))
	}
	hasMore := result.HasMore || len(result.Items) > pageSize
	return ListResult{
		Items:    items,
		Total:    pagedTotalUpperBound(page, pageSize, len(items), hasMore),
		HasMore:  hasMore,
		Page:     page,
		PageSize: pageSize,
	}, nil
}

func (s *Service) Options(ctx context.Context, input OptionListInput) ([]Option, error) {
	if s.store == nil {
		return nil, fmt.Errorf("management proxy option store is required")
	}
	rows, err := s.store.ListManagementProxyOptions(ctx, port.ManagementProxyOptionListInput{
		Keyword: strings.TrimSpace(input.Keyword),
		Limit:   normalizeOptionLimit(input.Limit),
	})
	if err != nil {
		return nil, err
	}
	items := make([]Option, 0, len(rows))
	for _, row := range rows {
		items = append(items, Option{
			ID:      row.ID,
			Name:    row.Name,
			Type:    row.Type,
			Enabled: row.Enabled,
		})
	}
	return items, nil
}

func (s *Service) Create(ctx context.Context, input CreateInput) (CreateResult, error) {
	writer, err := s.proxyWriter()
	if err != nil {
		return CreateResult{}, err
	}
	systemAccountID := strings.TrimSpace(input.SystemAccountID)
	if systemAccountID == "" {
		return CreateResult{}, &ValidationError{Message: "缺少系统账户上下文"}
	}
	name, err := normalizedRequiredText(input.Name, "代理名称不能为空")
	if err != nil {
		return CreateResult{}, err
	}
	description, err := normalizeOptionalText(input.Description, "代理描述", maxDescriptionRunes)
	if err != nil {
		return CreateResult{}, err
	}
	proxyType, err := normalizedProxyType(input.Type)
	if err != nil {
		return CreateResult{}, err
	}
	host, err := normalizedRequiredText(input.Host, "代理主机不能为空")
	if err != nil {
		return CreateResult{}, err
	}
	portValue, err := normalizedProxyPort(input.Port)
	if err != nil {
		return CreateResult{}, err
	}
	username, err := normalizeOptionalText(input.Username, "代理用户名", 0)
	if err != nil {
		return CreateResult{}, err
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	password, passwordSet, err := normalizeProxyPassword(input.Password)
	if err != nil {
		return CreateResult{}, err
	}
	var encrypted *string
	if passwordSet {
		encrypted, err = s.encryptPassword(password)
		if err != nil {
			return CreateResult{}, err
		}
	}
	now := s.now().UTC()
	created, err := writer.CreateManagementProxy(ctx, port.ManagementProxyCreateInput{
		ID:                s.newID("proxy"),
		SystemAccountID:   systemAccountID,
		Name:              name,
		Description:       description,
		Type:              proxyType,
		Host:              host,
		Port:              portValue,
		Username:          username,
		PasswordEncrypted: encrypted,
		Enabled:           enabled,
		CreatedAt:         now,
		UpdatedAt:         now,
	})
	if errors.Is(err, port.ErrManagementProxyNameExists) {
		return CreateResult{}, &NameExistsError{Name: name}
	}
	if err != nil {
		return CreateResult{}, err
	}
	s.invalidate(ctx, ProxyCreatedReason)
	return CreateResult{Proxy: proxySummaryFromPort(created), PasswordSet: passwordSet}, nil
}

func (s *Service) Update(ctx context.Context, input UpdateInput) (UpdateResult, error) {
	writer, err := s.proxyWriter()
	if err != nil {
		return UpdateResult{}, err
	}
	current, found, err := writer.FindManagementProxy(ctx, strings.TrimSpace(input.ID))
	if err != nil {
		return UpdateResult{}, err
	}
	if !found {
		return UpdateResult{}, ErrProxyNotFound
	}
	nextName := current.Name
	if input.Name != nil {
		nextName, err = normalizedRequiredText(*input.Name, "代理名称不能为空")
		if err != nil {
			return UpdateResult{}, err
		}
	}
	nextDescription := current.Description
	if input.Description.Set {
		nextDescription, err = normalizeOptionalText(input.Description.Value, "代理描述", maxDescriptionRunes)
		if err != nil {
			return UpdateResult{}, err
		}
	}
	nextType := current.Type
	if input.Type != nil {
		nextType, err = normalizedProxyType(*input.Type)
		if err != nil {
			return UpdateResult{}, err
		}
	}
	nextHost := current.Host
	if input.Host != nil {
		nextHost, err = normalizedRequiredText(*input.Host, "代理主机不能为空")
		if err != nil {
			return UpdateResult{}, err
		}
	}
	nextPort := current.Port
	if input.Port != nil {
		nextPort, err = normalizedProxyPort(*input.Port)
		if err != nil {
			return UpdateResult{}, err
		}
	}
	nextUsername := current.Username
	if input.Username.Set {
		nextUsername, err = normalizeOptionalText(input.Username.Value, "代理用户名", 0)
		if err != nil {
			return UpdateResult{}, err
		}
	}
	nextEnabled := current.Enabled
	if input.Enabled != nil {
		nextEnabled = *input.Enabled
	}
	password, passwordChanged, err := normalizeProxyPassword(input.Password)
	if err != nil {
		return UpdateResult{}, err
	}
	nextEncrypted := current.PasswordEncrypted
	if passwordChanged {
		nextEncrypted, err = s.encryptPassword(password)
		if err != nil {
			return UpdateResult{}, err
		}
	}
	resetTestState := nextType != current.Type ||
		nextHost != current.Host ||
		nextPort != current.Port ||
		!stringPtrEqual(nextUsername, current.Username) ||
		passwordChanged
	updated, found, err := writer.UpdateManagementProxy(ctx, port.ManagementProxyUpdateInput{
		ID:                strings.TrimSpace(input.ID),
		Name:              nextName,
		Description:       nextDescription,
		Type:              nextType,
		Host:              nextHost,
		Port:              nextPort,
		Username:          nextUsername,
		PasswordEncrypted: nextEncrypted,
		Enabled:           nextEnabled,
		ResetTestState:    resetTestState,
		UpdatedAt:         s.now().UTC(),
	})
	if errors.Is(err, port.ErrManagementProxyNameExists) {
		return UpdateResult{}, &NameExistsError{Name: nextName}
	}
	if err != nil {
		return UpdateResult{}, err
	}
	if !found {
		return UpdateResult{}, ErrProxyNotFound
	}
	s.invalidate(ctx, ProxyUpdatedReason)
	before := proxySummaryFromPort(current)
	after := proxySummaryFromPort(updated)
	return UpdateResult{
		Before:          before,
		Proxy:           after,
		Changed:         proxySummaryChanged(before, after) || passwordChanged,
		PasswordChanged: passwordChanged,
		ResetTestState:  resetTestState,
	}, nil
}

func (s *Service) Delete(ctx context.Context, input DeleteInput) (DeleteResult, error) {
	writer, err := s.proxyWriter()
	if err != nil {
		return DeleteResult{}, err
	}
	proxyID := strings.TrimSpace(input.ID)
	current, found, err := writer.FindManagementProxy(ctx, proxyID)
	if err != nil {
		return DeleteResult{}, err
	}
	if !found {
		return DeleteResult{}, ErrProxyNotFound
	}
	bindings, err := writer.ListManagementProxyAccountBindings(ctx, port.ManagementProxyAccountBindingListInput{
		ProxyID: proxyID,
		Limit:   proxyUsageWindowLimit,
	})
	if err != nil {
		return DeleteResult{}, err
	}
	if len(bindings) > 0 {
		return DeleteResult{}, proxyInUseError(bindings)
	}
	deleted, err := writer.DeleteManagementProxy(ctx, proxyID)
	if err != nil {
		return DeleteResult{}, err
	}
	if !deleted {
		return DeleteResult{}, ErrProxyNotFound
	}
	s.invalidate(ctx, ProxyDeletedReason)
	return DeleteResult{Before: proxySummaryFromPort(current), Deleted: true}, nil
}

func (s *Service) proxyWriter() (port.ManagementProxyWriter, error) {
	if s.store == nil {
		return nil, fmt.Errorf("management proxy store is required")
	}
	writer, ok := s.store.(port.ManagementProxyWriter)
	if !ok {
		return nil, fmt.Errorf("management proxy writer store is required")
	}
	return writer, nil
}

func (s *Service) encryptPassword(password string) (*string, error) {
	if s.codec == nil {
		return nil, fmt.Errorf("%w: codec is required", ErrProxyCredentialCodecUnusable)
	}
	encrypted, err := s.codec.EncryptJSON(map[string]any{"password": password})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProxyCredentialCodecUnusable, err)
	}
	return &encrypted, nil
}

func (s *Service) invalidate(ctx context.Context, reason string) {
	if s.invalidator == nil {
		return
	}
	_ = s.invalidator.InvalidateProxyChanged(ctx, reason)
}

func normalizeListPageSize(value int) int {
	if value <= 0 {
		return defaultListPageSize
	}
	if value > maxListPageSize {
		return maxListPageSize
	}
	return value
}

func normalizeListPage(value int, pageSize int) int {
	if value <= 0 {
		return 1
	}
	return min(value, pageUpperBoundForWindow(pageSize))
}

func normalizeOptionLimit(value int) int {
	if value <= 0 {
		return 50
	}
	if value > 50 {
		return 50
	}
	return value
}

func pagedTotalUpperBound(page int, pageSize int, itemCount int, hasMore bool) int {
	total := (max(1, page) - 1) * max(0, pageSize)
	total += max(0, itemCount)
	if hasMore {
		total++
	}
	return total
}

func pageUpperBoundForWindow(pageSize int) int {
	return max(1, (defaultListWindow-1)/max(1, pageSize))
}

func proxySummaryFromPort(row port.ManagementProxySummary) Summary {
	return Summary{
		ID:              row.ID,
		Name:            row.Name,
		Description:     row.Description,
		Type:            row.Type,
		Host:            row.Host,
		Port:            row.Port,
		Username:        row.Username,
		Enabled:         row.Enabled,
		TestStatus:      row.TestStatus,
		LatencyMs:       row.LatencyMs,
		OutboundIP:      row.OutboundIP,
		OutboundRegion:  row.OutboundRegion,
		LastTestMessage: row.LastTestMessage,
		LastTestedAt:    row.LastTestedAt,
	}
}

func normalizedRequiredText(value string, message string) (string, error) {
	text := strings.TrimSpace(value)
	if text == "" {
		return "", &ValidationError{Message: message}
	}
	return text, nil
}

func normalizeOptionalText(value *string, label string, maxRunes int) (*string, error) {
	if value == nil {
		return nil, nil
	}
	text := strings.TrimSpace(*value)
	if text == "" {
		return nil, nil
	}
	if maxRunes > 0 && utf8.RuneCountInString(text) > maxRunes {
		return nil, &ValidationError{Message: fmt.Sprintf("%s不能超过 %d 个字符", label, maxRunes)}
	}
	return &text, nil
}

func normalizedProxyType(value string) (string, error) {
	switch value {
	case "http", "https", "socks5", "socks5h":
		return value, nil
	default:
		return "", &ValidationError{Message: "代理类型无效"}
	}
}

func normalizedProxyPort(value int) (int, error) {
	if value < 1 || value > 65535 {
		return 0, &ValidationError{Message: "代理端口必须是 1-65535 的整数"}
	}
	return value, nil
}

func normalizeProxyPassword(value *string) (string, bool, error) {
	if value == nil {
		return "", false, nil
	}
	if strings.TrimSpace(*value) == "" {
		return "", false, &ValidationError{Message: "代理密码不能为空"}
	}
	return *value, true, nil
}

func stringPtrEqual(left *string, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func proxySummaryChanged(before Summary, after Summary) bool {
	return before.Name != after.Name ||
		!stringPtrEqual(before.Description, after.Description) ||
		before.Type != after.Type ||
		before.Host != after.Host ||
		before.Port != after.Port ||
		!stringPtrEqual(before.Username, after.Username) ||
		before.Enabled != after.Enabled ||
		before.TestStatus != after.TestStatus ||
		!intPtrEqual(before.LatencyMs, after.LatencyMs) ||
		!stringPtrEqual(before.OutboundIP, after.OutboundIP) ||
		!stringPtrEqual(before.OutboundRegion, after.OutboundRegion) ||
		!stringPtrEqual(before.LastTestMessage, after.LastTestMessage) ||
		!timePtrEqual(before.LastTestedAt, after.LastTestedAt)
}

func intPtrEqual(left *int, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func timePtrEqual(left *time.Time, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func proxyInUseError(bindings []port.ManagementProxyAccountBinding) error {
	names := make([]string, 0, min(len(bindings), proxyUsagePreviewLimit))
	for _, binding := range bindings {
		if len(names) >= proxyUsagePreviewLimit {
			break
		}
		name := strings.TrimSpace(binding.Name)
		if name != "" {
			names = append(names, name)
		}
	}
	return &InUseError{
		AccountCount:             len(bindings),
		AccountCountIsLowerBound: len(bindings) >= proxyUsageWindowLimit,
		AccountNames:             names,
	}
}

type aesGCMCredentialCodec struct {
	key [32]byte
}

func newAESGCMCredentialCodec(secret string) aesGCMCredentialCodec {
	return aesGCMCredentialCodec{key: sha256.Sum256([]byte(secret))}
}

func (c aesGCMCredentialCodec) EncryptJSON(value map[string]any) (string, error) {
	block, err := aes.NewCipher(c.key[:])
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	plain, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sealed := aead.Seal(nil, nonce, plain, nil)
	tagSize := aead.Overhead()
	ciphertext := sealed[:len(sealed)-tagSize]
	tag := sealed[len(sealed)-tagSize:]
	encode := base64.RawURLEncoding.EncodeToString
	return "v1:" + encode(nonce) + ":" + encode(tag) + ":" + encode(ciphertext), nil
}
