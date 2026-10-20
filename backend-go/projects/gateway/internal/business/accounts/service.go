package accounts

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
)

type Account struct {
	ID                        string          `json:"id"`
	SystemAccountID           string          `json:"systemAccountId"`
	ProviderCode              string          `json:"providerCode"`
	ProviderProtocolProfileID string          `json:"providerProtocolProfileId"`
	ProtocolCode              string          `json:"protocolCode"`
	ProtocolVersion           string          `json:"protocolVersion"`
	Name                      string          `json:"name"`
	Type                      string          `json:"type"`
	Status                    string          `json:"status"`
	ConfigRevision            int64           `json:"configRevision"`
	DispatchRevision          int64           `json:"dispatchRevision"`
	Schedulable               bool            `json:"schedulable"`
	AvailabilityScheduleJSON  *string         `json:"availabilityScheduleJson,omitempty"`
	CreatedAt                 string          `json:"createdAt"`
	UpdatedAt                 string          `json:"updatedAt"`
	SupportedModels           []string        `json:"supportedModels,omitempty"`
	ModelMappings             []ModelMapping  `json:"modelMappings,omitempty"`
	Tags                      []string        `json:"tags,omitempty"`
	APIKeyBindings            []APIKeyBinding `json:"apiKeyBindings,omitempty"`
}

// ModelMapping 是账户入口映射的聚合 DTO（供应商与上游协议面重构设计 §5.1/§8）。
// 连接维度（B3 剩余）：TargetConnectionPresetCode 是账户内自然键——连接 ID
// 永不由客户端提交，服务端按 (账户, preset) 解析 account_connections.id 后写
// account_entry_mappings.target_connection_id。UpstreamModel 语义即设计 §8
// entryMappings[].targetModelId 的入参载体（按 models.upstream_model 身份键解析
// 为 target_model_id，与 resolveSupportedModelID 同键）；UpstreamEndpointFamily
// 即 target_endpoint_family（旧字段名保留为写侧兼容入参，接受但不再承载
// provider_code/upstream 字符串语义）。缺省 preset 的存量调用方（导入器等）
// 按显式错误（connection 解析失败 → ErrMappingTargetUnsupported）拒绝，不静默
// 跳过、不造兼容层（迁移窗口裁决归主代理）。
type ModelMapping struct {
	SourceModel                string `json:"sourceModel"`
	SourceEndpointFamily       string `json:"sourceEndpointFamily"`
	UpstreamModel              string `json:"upstreamModel"`
	UpstreamEndpointFamily     string `json:"upstreamEndpointFamily"`
	TargetConnectionPresetCode string `json:"targetConnectionPresetCode"`
	Enabled                    bool   `json:"enabled"`
}

type APIKeyBinding struct {
	ID          string `json:"id"`
	Fingerprint string `json:"fingerprint"`
	Status      string `json:"status"`
}

type CreateInput struct {
	ID, SystemAccountID, ProviderCode, ProtocolCode, ProtocolVersion string
	Name, Type, Status, CredentialsEncrypted                                                    string
	Schedulable                                                                                 bool
	AvailabilityScheduleJSON                                                                    *string
	SupportedModels                                                                             []string
	ModelMappings                                                                                          []ModelMapping
	Tags                                                                                                   []string
	APIKeyBindings                                                                                         []APIKeyBinding
	// Connections 是 §8 连接行载荷（B1 断点批共享 DTO）：非空时由 create 在同一
	// 事务内先于映射写入 account_connections + account_endpoint_modes（确定性
	// 行 ID），使 writeEntryMapping 的 preset 解析在本事务内可命中。完整 §8
	// registry/validator 校验归 internal/accounts 写链（本聚合为 Port 边界
	// 形态，不复制允许集合）。
	Connections []accountscore.ConnectionInput
}

type Patch struct {
	ExpectedConfigRevision   int64
	Name                     *string
	Status                   *string
	Schedulable              *bool
	CredentialsEncrypted     *string
	AvailabilityScheduleJSON **string
	SupportedModels          *[]string
	ModelMappings            *[]ModelMapping
	Tags                     *[]string
	APIKeyBindings           *[]APIKeyBinding
}

type Port interface {
	CheckContract(context.Context) error
	Create(context.Context, CreateInput) (Account, error)
	Get(context.Context, string, string) (Account, error)
	List(context.Context, string) ([]Account, error)
	Patch(context.Context, string, string, Patch) (Account, error)
	Delete(context.Context, string, string, int64) (Account, error)
}

type Service struct{ store *Store }

// ErrModelNotFound 是支持模型解析失败的管理面 400 语义哨兵（错误码风格对齐
// 网关既有 model_not_found）：请求 supportedModels 中的某个模型名在 models
// 表按 (vendor_code, upstream_model) + owner 可见性解析不到时返回，不静默跳过。
var ErrModelNotFound = errors.New("model not found")

// ErrMappingTargetUnsupported 是入口映射 target 解析/联动校验失败的管理面 400
// 语义哨兵（错误码对齐设计 §5.1 校验 4 / §8 mapping_target_unsupported）：
// targetConnectionPresetCode 解析不到本账户 enabled 连接、target 模型解析失败、
// 或 §5.2/§5.3 family 联动校验不满足时返回，不静默跳过。
var ErrMappingTargetUnsupported = errors.New("mapping_target_unsupported")

func New(db *sql.DB, postgres bool, gate OwnerGate) (*Service, error) {
	s, err := NewStore(db, postgres, gate)
	if err != nil {
		return nil, err
	}
	return &Service{store: s}, nil
}

var _ Port = (*Service)(nil)

func (s *Service) CheckContract(ctx context.Context) error { return s.store.CheckContract(ctx) }
func (s *Service) Create(ctx context.Context, in CreateInput) (Account, error) {
	return s.store.create(ctx, in)
}
func (s *Service) Get(ctx context.Context, systemID, id string) (Account, error) {
	return s.store.get(ctx, systemID, id)
}
func (s *Service) List(ctx context.Context, systemID string) ([]Account, error) {
	return s.store.list(ctx, systemID)
}
func (s *Service) Patch(ctx context.Context, systemID, id string, p Patch) (Account, error) {
	return s.store.patch(ctx, systemID, id, p)
}
func (s *Service) Delete(ctx context.Context, systemID, id string, expected int64) (Account, error) {
	return s.store.delete(ctx, systemID, id, expected)
}

func normalize(in CreateInput) error {
	if strings.TrimSpace(in.ID) == "" || strings.TrimSpace(in.SystemAccountID) == "" || strings.TrimSpace(in.ProviderCode) == "" || strings.TrimSpace(in.ProtocolCode) == "" || strings.TrimSpace(in.ProtocolVersion) == "" || strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.Type) == "" || strings.TrimSpace(in.CredentialsEncrypted) == "" {
		return errors.New("account identity and credentials are required")
	}
	if in.Status == "" {
		return errors.New("account status is required")
	}
	return nil
}

// realVendorCodes 是供应商与上游协议面重构设计 §1.1 注册表的 9 个真实厂商值域
// （与 internal/accounts 写入面、maintenance 迁移命令同词表）。本包只依赖
// database/sql，不跨包复用注册表，词表以字面量维护。
var realVendorCodes = map[string]bool{
	"gpt": true, "xai": true, "deepseek": true, "anthropic": true, "gemini": true,
	"glm": true, "minimax": true, "volcengine": true, "qwen": true,
}

// vendorIdentityForProvider 把 provider 语义推导为账户行 (vendor_code,
// connection_driver_code) 写入值（B1 写入面缺口）：创建请求体不携带
// vendor/driver 字段，推导规则与迁移命令 vendorOfProvider / driver 推导
// （providerconnectionmigration/rules.go:770、:919）同口径——
//   - provider_code 命中 9 厂商：vendor_code = provider_code，
//     connection_driver_code = provider_code（注册表 driver 名与 vendor 同名；
//     OpenAI OAuth 账户现以 provider gpt 创建，天然落 vendor gpt）；
//   - 其余（openai / hybrid 等 generic 语义 provider）：vendor_code 为空串
//     （SQL NULL）、connection_driver_code = 'generic_http'（设计 §2.1：
//     generic_http 账户 vendor 恒 NULL）。
func vendorIdentityForProvider(providerCode string) (vendor, driver string) {
	if realVendorCodes[providerCode] {
		return providerCode, providerCode
	}
	return "", "generic_http"
}

// resolveSupportedModelID 按 models 目标形态身份键把支持模型名解析为
// models.id（B3 写入路径）。语义决策：supportedModels 携带上游模型名，解析键
// 取 vendor_code + upstream_model——models 身份唯一索引即
// (coalesce(vendor_code,”), scope, coalesce(system_account_id,”),
// upstream_model)（pg_seed.go:359），健康检查守卫同样按 upstream_model 名匹配
// （pg_seed.go:413-415 注释）；public_model 只是展示投影，不作身份键。
// scope 可见性按资源账户 owner：personal 行必须归属该 owner，builtin/global
// 全局可见，owner personal 行优先于全局行；status 限定 active（种子停用行
// 不得新建支持关系）。解析不到返回 ErrModelNotFound。
func resolveSupportedModelID(ctx context.Context, tx *sql.Tx, s *Store, vendor, owner, model string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("models")+`
		WHERE COALESCE(vendor_code,'') = ? AND upstream_model = ? AND status = 'active'
		  AND (scope IN ('builtin','global') OR (scope = 'personal' AND system_account_id = ?))
		ORDER BY CASE scope WHEN 'personal' THEN 0 WHEN 'global' THEN 1 ELSE 2 END, id
		LIMIT 1`), vendor, model, owner).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: %s", ErrModelNotFound, model)
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

// mappingPairTargetModes 是设计 §5.2 固定 endpoint family 矩阵的 17 个允许
// (source, target) pair 及其全部可执行 target mode 形态（§5.3 唯一选择函数的
// 写侧输入）。矩阵外的 pair 一律拒绝（设计 §5.2 尾注的 mapping_pair_unsupported
// 语义在本包折叠为同一 ErrMappingTargetUnsupported 400 口径——任务契约「对齐
// mapping_target_unsupported 风格」；错误码细分归 HTTP 装配层切片）。
var mappingPairTargetModes = map[string][]string{
	"chat_completions\x00chat_completions":        {"chat_json", "chat_sse"},
	"responses\x00chat_completions":               {"chat_sse"},
	"messages\x00chat_completions":                {"chat_json", "chat_sse"},
	"generate_content\x00chat_completions":        {"chat_json"},
	"stream_generate_content\x00chat_completions": {"chat_sse"},
	"messages\x00messages":                        {"messages_json", "messages_sse"},
	"chat_completions\x00messages":                {"messages_json", "messages_sse"},
	"responses\x00messages":                       {"messages_json", "messages_sse"},
	"generate_content\x00messages":                {"messages_json"},
	"stream_generate_content\x00messages":         {"messages_sse"},
	"generate_content\x00generate_content":        {"generate_content_json"},
	"stream_generate_content\x00generate_content": {"generate_content_sse"},
	"chat_completions\x00generate_content":        {"generate_content_json", "generate_content_sse"},
	"responses\x00generate_content":               {"generate_content_json", "generate_content_sse"},
	"messages\x00generate_content":                {"generate_content_json", "generate_content_sse"},
	// §5.2 #16：视频映射 target 必须同时声明并启用全部四个 video mode——
	// 不能把单一 video_create 当作完整可轮询的视频连接。
	"video_generation\x00video_generation": {"video_create", "video_get", "video_content", "video_cancel"},
	// §5.2 #17：TTS 只有一个同步语音形态。
	"tts\x00tts": {"audio_speech"},
}

// validateEntryMappingFamilyLinkage 是映射写侧的 family 联动校验（设计 §5.3）：
// 文本目标要求至少一个 source shape 的 target mode 同时落在目标连接
// account_endpoint_modes 与 target 模型 supported_endpoint_modes_json 的交集内；
// 视频目标必须四个 mode 在该交集中全部可执行（声明 + 启用）；TTS 目标必须有
// audio_speech。不满足返回 ErrMappingTargetUnsupported。这是 W2 拆 hybrid 门
// 的前置（任务契约）。
func validateEntryMappingFamilyLinkage(sourceFamily, targetFamily string, connectionModes, modelModes []string) error {
	pairModes, ok := mappingPairTargetModes[sourceFamily+"\x00"+targetFamily]
	if !ok {
		return fmt.Errorf("%w: family pair %s->%s is not in the §5.2 matrix", ErrMappingTargetUnsupported, sourceFamily, targetFamily)
	}
	connectionSet := stringSet(connectionModes)
	modelSet := stringSet(modelModes)
	if targetFamily == "video_generation" {
		for _, mode := range pairModes {
			if !connectionSet[mode] || !modelSet[mode] {
				return fmt.Errorf("%w: video target requires all four modes declared and enabled, missing %s", ErrMappingTargetUnsupported, mode)
			}
		}
		return nil
	}
	for _, mode := range pairModes {
		if connectionSet[mode] && modelSet[mode] {
			return nil
		}
	}
	return fmt.Errorf("%w: no source shape of %s->%s is executable in connection∩model endpoint modes", ErrMappingTargetUnsupported, sourceFamily, targetFamily)
}

func stringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

// connectionEndpointModes 读目标连接的 account_endpoint_modes 实际集合（§5.3
// 联动校验的连接侧输入）。
func connectionEndpointModes(ctx context.Context, tx *sql.Tx, s *Store, connectionID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT endpoint_mode FROM `+s.table("account_endpoint_modes")+` WHERE connection_id = ? ORDER BY endpoint_mode`), connectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var modes []string
	for rows.Next() {
		var mode string
		if err := rows.Scan(&mode); err != nil {
			return nil, err
		}
		modes = append(modes, mode)
	}
	return modes, rows.Err()
}

// modelEndpointModes 读 target 模型的 supported_endpoint_modes_json（§3：JSON
// 字符串数组）。行缺失返回错误；NULL/空数组按空集参与交集（active 模型至少
// 1 个 mode 是写侧上游契约，这里不重复防御）。
func modelEndpointModes(ctx context.Context, tx *sql.Tx, s *Store, modelID string) ([]string, error) {
	var raw sql.NullString
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT supported_endpoint_modes_json FROM `+s.table("models")+` WHERE id = ?`), modelID).Scan(&raw); err != nil {
		return nil, err
	}
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil, nil
	}
	var modes []string
	if err := json.Unmarshal([]byte(raw.String), &modes); err != nil {
		return nil, fmt.Errorf("model %s supported_endpoint_modes_json: %w", modelID, err)
	}
	return modes, nil
}

// writeEntryMapping 把一条入口映射写入 account_entry_mappings（设计 §5.1，B3
// 剩余）：按 (账户, TargetConnectionPresetCode) 解析 enabled 连接行（preset 是
// 账户内唯一键，连接 ID 永不由客户端提交，§8），复用 resolveSupportedModelID
// 把 target 模型名解析为 target_model_id，再执行 §5.3 family 联动校验后落行。
// 任一步失败返回既有 400 语义（ErrMappingTargetUnsupported / ErrModelNotFound
// 链），不静默跳过；旧行写 account_model_mappings 的循环已删除（该表随 A 路线
// 撤除）。前置契约：account_connections 行由连接写入面（同账户创建事务的
// 连接切片/导入器）先于本写路径存在。
func writeEntryMapping(ctx context.Context, tx *sql.Tx, s *Store, accountID, owner, vendor string, m ModelMapping, now string) error {
	preset := strings.TrimSpace(m.TargetConnectionPresetCode)
	var connectionID string
	err := tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("account_connections")+`
		WHERE account_id = ? AND connection_preset_code = ? AND enabled = 1`), accountID, preset).Scan(&connectionID)
	if errors.Is(err, sql.ErrNoRows) {
		// §8：targetConnectionPresetCode 必须解析到最终连接集合中一行；缺省
		// （存量导入器旧格式无 preset）、未知或停用都按显式错误拒绝。
		return fmt.Errorf("%w: connection preset %q resolves to no enabled account connection", ErrMappingTargetUnsupported, preset)
	}
	if err != nil {
		return err
	}
	modelID, err := resolveSupportedModelID(ctx, tx, s, vendor, owner, m.UpstreamModel)
	if err != nil {
		// §5.1 校验 4：target 模型不满足与连接解析同一 400 语义；%w 链保留
		// ErrModelNotFound 供上层甄别具体失败模型。
		return fmt.Errorf("%w: %w", ErrMappingTargetUnsupported, err)
	}
	connectionModes, err := connectionEndpointModes(ctx, tx, s, connectionID)
	if err != nil {
		return err
	}
	modelModes, err := modelEndpointModes(ctx, tx, s, modelID)
	if err != nil {
		return err
	}
	if err := validateEntryMappingFamilyLinkage(m.SourceEndpointFamily, m.UpstreamEndpointFamily, connectionModes, modelModes); err != nil {
		return err
	}
	// 行 ID 按自然键 (account_id, source_model, source_endpoint_family) 确定性
	// 生成（与 account_tags 同款 sha256 截断先例；UNIQUE 约束保证同键唯一）。
	summary := sha256.Sum256([]byte(accountID + "\x00" + m.SourceModel + "\x00" + m.SourceEndpointFamily))
	entryID := "entmap-" + hex.EncodeToString(summary[:12])
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_entry_mappings")+`
		(id,account_id,source_model,source_endpoint_family,target_connection_id,target_model_id,target_endpoint_family,enabled,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`),
		entryID, accountID, m.SourceModel, m.SourceEndpointFamily, connectionID, modelID, m.UpstreamEndpointFamily, boolInt(m.Enabled), now, now)
	return err
}

// writeAccountConnections 落 §8 连接行集合（B1 断点批，写入序先于映射）：
// 每行 preset/baseUrl 非空、endpointModes 非空无重复，行 ID 按 (账户, preset)
// 确定性生成（与 writeEntryMapping 的 entmap 先例同款 sha256 截断）。
// protocol_surface_code 空串落 SQL NULL（纯媒体连接，§2.1）。
func writeAccountConnections(ctx context.Context, tx *sql.Tx, s *Store, accountID string, connections []accountscore.ConnectionInput, now string) error {
	for _, connection := range connections {
		preset := strings.TrimSpace(connection.ConnectionPresetCode)
		baseURL := strings.TrimSpace(connection.BaseURL)
		if preset == "" || baseURL == "" {
			return errors.New("connection preset and baseUrl are required")
		}
		if len(connection.EndpointModes) == 0 {
			return errors.New("connection endpointModes are required")
		}
		seen := map[string]bool{}
		for _, mode := range connection.EndpointModes {
			mode = strings.TrimSpace(mode)
			if mode == "" || seen[mode] {
				return errors.New("connection endpointModes must be unique non-empty values")
			}
			seen[mode] = true
		}
		summary := sha256.Sum256([]byte(accountID + "\x00" + preset))
		connectionID := "conn-" + hex.EncodeToString(summary[:12])
		var surface sql.NullString
		if trimmed := strings.TrimSpace(connection.ProtocolSurfaceCode); trimmed != "" {
			surface = sql.NullString{String: trimmed, Valid: true}
		}
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_connections")+`
			(id,account_id,connection_preset_code,protocol_surface_code,base_url,enabled,created_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?)`),
			connectionID, accountID, preset, surface, baseURL, boolInt(connection.Enabled), now, now); err != nil {
			return err
		}
		for _, mode := range connection.EndpointModes {
			if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_endpoint_modes")+`
				(connection_id,endpoint_mode) VALUES (?,?)`), connectionID, strings.TrimSpace(mode)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) create(ctx context.Context, in CreateInput) (Account, error) {
	if err := s.requireOwner(); err != nil {
		return Account{}, err
	}
	if err := normalize(in); err != nil {
		return Account{}, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback()
	now := s.stamp()
	// B1 写入面：accounts 行补 vendor_code / connection_driver_code 两列，
	// 推导规则见 vendorIdentityForProvider 注释；generic 账户 vendor 落 SQL
	// NULL（设计 §2.1），driver 恒非空。
	vendorCode, driverCode := vendorIdentityForProvider(strings.TrimSpace(in.ProviderCode))
	vendorColumn := sql.NullString{String: vendorCode, Valid: vendorCode != ""}
	// 目标 accounts 无 provider_code / provider_protocol_profile_id 两列
	// （设计 §2.1/§11）：身份只落 vendor_code + connection_driver_code，
	// provider 入参仅用于推导这对值。
	q := `INSERT INTO ` + s.table("accounts") + ` (id,system_account_id,protocol_code,protocol_version,vendor_code,connection_driver_code,name,type,status,credentials_encrypted,config_revision,dispatch_revision,schedulable,availability_schedule_json,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,1,1,?,?,?,?)`
	_, err = tx.ExecContext(ctx, s.bind(q), in.ID, in.SystemAccountID, in.ProtocolCode, in.ProtocolVersion, vendorColumn, driverCode, in.Name, in.Type, in.Status, in.CredentialsEncrypted, boolInt(in.Schedulable), nullableString(in.AvailabilityScheduleJSON), now, now)
	if err != nil {
		// Client retries use the caller-provided account ID as a receipt key.
		// Returning an existing semantically identical aggregate is safe; a
		// different payload remains visible as a conflict.
		_ = tx.Rollback()
		current, readErr := s.get(ctx, in.SystemAccountID, in.ID)
		if readErr == nil && equivalentCreate(current, in) {
			return current, nil
		}
		if readErr == nil {
			return Account{}, fmt.Errorf("%w: account id already has different content", ErrRevisionConflict)
		}
		return Account{}, err
	}
	if err := writeAccountConnections(ctx, tx, s, in.ID, in.Connections, now); err != nil {
		return Account{}, err
	}
	if err := writeRelations(ctx, tx, s, in.ID, in.SystemAccountID, vendorCode, in.SupportedModels, in.ModelMappings, in.Tags, in.APIKeyBindings, now); err != nil {
		return Account{}, err
	}
	if err := tx.Commit(); err != nil {
		return Account{}, err
	}
	// Post-commit gateway runtime invalidation (网关模型列表账户并集设计 6.3
	// coverage): the committed create (accounts row + supportedModels/
	// mappings relations) is part of the union dependency face. The
	// idempotent-retry arm above rolls back and publishes nothing.
	// Best-effort, after the commit, never blocks or fails the write (see
	// invalidation.go).
	s.invalidateAfterCommittedWrite(invalidationReasonCreated)
	return s.get(ctx, in.SystemAccountID, in.ID)
}

func (s *Store) get(ctx context.Context, systemID, id string) (Account, error) {
	if err := s.requireOwner(); err != nil {
		return Account{}, err
	}
	var a Account
	var sched int
	var av sql.NullString
	// 读侧投影与 internal/accounts 同口径：provider_code 取 vendor_code
	// （generic 账户为空串），provider_protocol_profile_id 恒空。
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT id,system_account_id,COALESCE(vendor_code,'') AS provider_code,'' AS provider_protocol_profile_id,protocol_code,protocol_version,name,type,status,config_revision,dispatch_revision,schedulable,availability_schedule_json,created_at,updated_at FROM `+s.table("accounts")+` WHERE id=? AND system_account_id=? AND deleted_at IS NULL`), id, systemID).Scan(&a.ID, &a.SystemAccountID, &a.ProviderCode, &a.ProviderProtocolProfileID, &a.ProtocolCode, &a.ProtocolVersion, &a.Name, &a.Type, &a.Status, &a.ConfigRevision, &a.DispatchRevision, &sched, &av, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, err
	}
	a.Schedulable = sched != 0
	if av.Valid {
		a.AvailabilityScheduleJSON = &av.String
	}
	if err := readRelations(ctx, s.db, s, &a); err != nil {
		return Account{}, err
	}
	return a, nil
}

func (s *Store) list(ctx context.Context, systemID string) ([]Account, error) {
	if err := s.requireOwner(); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id FROM `+s.table("accounts")+` WHERE system_account_id=? AND deleted_at IS NULL ORDER BY id`), systemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		a, err := s.get(ctx, systemID, id)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) patch(ctx context.Context, systemID, id string, p Patch) (Account, error) {
	if err := s.requireOwner(); err != nil {
		return Account{}, err
	}
	if p.ExpectedConfigRevision < 1 {
		return Account{}, errors.New("expected config revision is required")
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback()
	var current int64
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT config_revision FROM `+s.table("accounts")+` WHERE id=? AND system_account_id=? AND deleted_at IS NULL`), id, systemID).Scan(&current); errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	} else if err != nil {
		return Account{}, err
	}
	if current != p.ExpectedConfigRevision {
		return Account{}, fmt.Errorf("%w: expected %d actual %d", ErrRevisionConflict, p.ExpectedConfigRevision, current)
	}
	sets := []string{}
	args := []any{}
	if p.Name != nil {
		if strings.TrimSpace(*p.Name) == "" {
			return Account{}, errors.New("account name is required")
		}
		sets = append(sets, "name=?")
		args = append(args, *p.Name)
	}
	if p.Status != nil {
		if strings.TrimSpace(*p.Status) == "" {
			return Account{}, errors.New("account status is required")
		}
		sets = append(sets, "status=?")
		args = append(args, *p.Status)
	}
	if p.Schedulable != nil {
		sets = append(sets, "schedulable=?")
		args = append(args, boolInt(*p.Schedulable))
	}
	if p.CredentialsEncrypted != nil {
		if strings.TrimSpace(*p.CredentialsEncrypted) == "" {
			return Account{}, errors.New("encrypted credentials are required")
		}
		sets = append(sets, "credentials_encrypted=?")
		args = append(args, *p.CredentialsEncrypted)
	}
	if p.AvailabilityScheduleJSON != nil {
		sets = append(sets, "availability_schedule_json=?")
		args = append(args, nullableString(*p.AvailabilityScheduleJSON))
	}
	now := s.stamp()
	baseChanged := len(sets) > 0
	if baseChanged {
		sets = append(sets, "config_revision=config_revision+1", "updated_at=?")
		args = append(args, now, id, systemID, current)
		q := `UPDATE ` + s.table("accounts") + ` SET ` + strings.Join(sets, ",") + ` WHERE id=? AND system_account_id=? AND config_revision=? AND deleted_at IS NULL`
		res, err := tx.ExecContext(ctx, s.bind(q), args...)
		if err != nil {
			return Account{}, err
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return Account{}, ErrRevisionConflict
		}
	}
	relationsTouched := p.SupportedModels != nil || p.ModelMappings != nil || p.Tags != nil || p.APIKeyBindings != nil
	if relationsTouched {
		if err := writeRelationsPatch(ctx, tx, s, id, systemID, p, now); err != nil {
			return Account{}, err
		}
		if !baseChanged {
			result, updateErr := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+` SET config_revision=config_revision+1,updated_at=? WHERE id=? AND system_account_id=? AND config_revision=? AND deleted_at IS NULL`), now, id, systemID, current)
			if updateErr != nil {
				return Account{}, updateErr
			}
			rows, _ := result.RowsAffected()
			if rows != 1 {
				return Account{}, ErrRevisionConflict
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return Account{}, err
	}
	// Post-commit gateway runtime invalidation (网关模型列表账户并集设计 6.3
	// coverage): a changed patch is part of the union dependency face
	// (supportedModels/mappings ride writeRelationsPatch; a non-nil relations
	// field always bumps config_revision). An all-nil patch commits an empty
	// transaction and publishes nothing. Best-effort, after the commit,
	// never blocks or fails the write (see invalidation.go).
	if baseChanged || relationsTouched {
		s.invalidateAfterCommittedWrite(invalidationReasonPatched)
	}
	return s.get(ctx, systemID, id)
}

func (s *Store) delete(ctx context.Context, systemID, id string, expected int64) (Account, error) {
	if err := s.requireOwner(); err != nil {
		return Account{}, err
	}
	if expected < 1 {
		return Account{}, errors.New("expected config revision is required")
	}
	a, err := s.get(ctx, systemID, id)
	if err != nil {
		return Account{}, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback()
	now := s.stamp()
	res, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+` SET status='disabled',schedulable=0,deleted_at=?,deleted_by=?,config_revision=config_revision+1,dispatch_revision=dispatch_revision+1,updated_at=? WHERE id=? AND system_account_id=? AND config_revision=? AND deleted_at IS NULL`), now, systemID, now, id, systemID, expected)
	if err != nil {
		return Account{}, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return Account{}, ErrRevisionConflict
	}
	if err := tx.Commit(); err != nil {
		return Account{}, err
	}
	// Post-commit gateway runtime invalidation (网关模型列表账户并集设计 6.3
	// coverage): the committed soft delete flips status/schedulable/
	// deleted_at on the accounts row — part of the union dependency face.
	// The not-found (already deleted / unknown account) and stale-revision
	// arms above return or roll back before the commit and publish nothing.
	// Best-effort, after the commit, never blocks or fails the write (see
	// invalidation.go).
	s.invalidateAfterCommittedWrite(invalidationReasonDeleted)
	a.Status = "disabled"
	a.Schedulable = false
	a.ConfigRevision = expected + 1
	a.DispatchRevision++
	a.UpdatedAt = now
	return a, nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func nullableString(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}

// writeRelations 落账户子关系。supportedModels 走 B3 目标形态：模型名按
// (vendor_code, upstream_model) + owner 可见性解析 models.id 后写
// (account_id, model_id)（解析失败返回 ErrModelNotFound，不静默跳过）。
// modelMappings 走 B4 第一批目标形态（设计 §5.1/§8）：每条映射经
// writeEntryMapping 双解析（connection preset → enabled account_connections.id；
// target 模型名 → models.id）+ §5.3 family 联动校验后写 account_entry_mappings；
// 解析/校验失败按 ErrMappingTargetUnsupported 链拒绝，不静默跳过。旧写
// account_model_mappings 的循环已删除（该表随 A 路线撤除，fresh DDL 不再建
// 表）；provider 入参随旧行的 provider_code 列一并退役，签名不再携带。
func writeRelations(ctx context.Context, tx *sql.Tx, s *Store, id, systemID, vendor string, models []string, mappings []ModelMapping, tags []string, keys []APIKeyBinding, now string) error {
	for _, m := range models {
		modelID, err := resolveSupportedModelID(ctx, tx, s, vendor, systemID, m)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_supported_models")+` (account_id,model_id,created_at) VALUES (?,?,?)`), id, modelID, now); err != nil {
			return err
		}
	}
	for _, m := range mappings {
		if err := writeEntryMapping(ctx, tx, s, id, systemID, vendor, m, now); err != nil {
			return err
		}
	}
	for _, name := range unique(tags) {
		h := sha256.Sum256([]byte(systemID + "\x00" + name))
		tid := "acctag-" + hex.EncodeToString(h[:12])
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_tags")+` (id,system_account_id,name,created_at,updated_at) VALUES (?,?,?,?,?) ON CONFLICT (system_account_id,name) DO UPDATE SET updated_at=excluded.updated_at`), tid, systemID, name, now, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_tag_bindings")+` (account_id,tag_id,system_account_id,created_at) VALUES (?,?,?,?) ON CONFLICT DO NOTHING`), id, tid, systemID, now); err != nil {
			return err
		}
	}
	for _, k := range keys {
		if strings.TrimSpace(k.ID) == "" || strings.TrimSpace(k.Fingerprint) == "" {
			return errors.New("api key binding identity is required")
		}
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_api_key_runtime_states")+` (id,system_account_id,account_id,key_fingerprint,status,created_at,updated_at) VALUES (?,?,?,?,?,?,?)`), k.ID, systemID, id, k.Fingerprint, defaultStatus(k.Status), now, now); err != nil {
			return err
		}
	}
	return nil
}
func writeRelationsPatch(ctx context.Context, tx *sql.Tx, s *Store, id, systemID string, p Patch, now string) error {
	if p.SupportedModels != nil {
		if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("account_supported_models")+` WHERE account_id=?`), id); err != nil {
			return err
		}
		// B3 目标形态：模型解析需要账户 vendor 与 owner 可见性（资源账户
		// system_account_id 即 owner scope），不再读旧 provider_code 键；
		// generic 账户 vendor_code 为 SQL NULL，经 NullString 扫描后按空串
		// 参与身份键比较（COALESCE 两侧同形）。
		var vendorColumn sql.NullString
		var owner string
		if err := tx.QueryRowContext(ctx, s.bind(`SELECT vendor_code, system_account_id FROM `+s.table("accounts")+` WHERE id=?`), id).Scan(&vendorColumn, &owner); err != nil {
			return err
		}
		vendor := vendorColumn.String
		for _, m := range *p.SupportedModels {
			modelID, err := resolveSupportedModelID(ctx, tx, s, vendor, owner, m)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_supported_models")+` (account_id,model_id,created_at) VALUES (?,?,?)`), id, modelID, now); err != nil {
				return err
			}
		}
	}
	if p.ModelMappings != nil {
		// B4 第一批目标形态：整集合替换写 account_entry_mappings（§8「任一
		// 集合字段出现时整集合替换」）。模型解析需要账户 vendor 与 owner
		// 可见性（与 SupportedModels 分支同口径；generic 账户 vendor_code 为
		// SQL NULL，经 NullString 扫描后按空串参与身份键比较）。
		if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("account_entry_mappings")+` WHERE account_id=?`), id); err != nil {
			return err
		}
		var vendorColumn sql.NullString
		var owner string
		if err := tx.QueryRowContext(ctx, s.bind(`SELECT vendor_code, system_account_id FROM `+s.table("accounts")+` WHERE id=?`), id).Scan(&vendorColumn, &owner); err != nil {
			return err
		}
		vendor := vendorColumn.String
		for _, m := range *p.ModelMappings {
			if err := writeEntryMapping(ctx, tx, s, id, owner, vendor, m, now); err != nil {
				return err
			}
		}
	}
	if p.Tags != nil {
		if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("account_tag_bindings")+` WHERE account_id=?`), id); err != nil {
			return err
		}
		if err := writeRelations(ctx, tx, s, id, systemID, "", nil, nil, *p.Tags, nil, now); err != nil {
			return err
		}
	}
	if p.APIKeyBindings != nil {
		if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("account_api_key_runtime_states")+` WHERE account_id=?`), id); err != nil {
			return err
		}
		for _, k := range *p.APIKeyBindings {
			if strings.TrimSpace(k.ID) == "" || strings.TrimSpace(k.Fingerprint) == "" {
				return errors.New("api key binding identity is required")
			}
			if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_api_key_runtime_states")+` (id,system_account_id,account_id,key_fingerprint,status,created_at,updated_at) VALUES (?,?,?,?,?,?,?)`), k.ID, systemID, id, k.Fingerprint, defaultStatus(k.Status), now, now); err != nil {
				return err
			}
		}
	}
	return nil
}
func readRelations(ctx context.Context, db *sql.DB, s *Store, a *Account) error {
	// B3 目标形态读取：account_supported_models 只存 model_id，聚合 DTO 的
	// SupportedModels 投影 models.upstream_model（与写入解析键同语义，往返
	// 幂等），INNER JOIN 天然排除迁移窗口内无 model_id 的旧形态行。
	rows, err := db.QueryContext(ctx, s.bind(`SELECT m.upstream_model FROM `+s.table("account_supported_models")+` asm JOIN `+s.table("models")+` m ON m.id=asm.model_id WHERE asm.account_id=? ORDER BY m.upstream_model`), a.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			rows.Close()
			return err
		}
		a.SupportedModels = append(a.SupportedModels, m)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	// B4 第一批目标形态读取：映射行走 account_entry_mappings，target 模型名
	// 经 LEFT JOIN models.upstream_model 投影（与 V1 批 GKM 装载器同款形态，
	// 往返幂等），连接自然键经 LEFT JOIN account_connections.connection_preset_code
	// 投影（连接 ID 不出聚合 DTO）。enabled 行与 disabled 行都读（DTO 带
	// Enabled 往返）。
	rows, err = db.QueryContext(ctx, s.bind(`SELECT em.source_model,em.source_endpoint_family,m.upstream_model,em.target_endpoint_family,c.connection_preset_code,em.enabled FROM `+s.table("account_entry_mappings")+` em
		LEFT JOIN `+s.table("models")+` m ON m.id=em.target_model_id
		LEFT JOIN `+s.table("account_connections")+` c ON c.id=em.target_connection_id
		WHERE em.account_id=? ORDER BY em.source_model,em.source_endpoint_family`), a.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var m ModelMapping
		var e int
		if err := rows.Scan(&m.SourceModel, &m.SourceEndpointFamily, &m.UpstreamModel, &m.UpstreamEndpointFamily, &m.TargetConnectionPresetCode, &e); err != nil {
			rows.Close()
			return err
		}
		m.Enabled = e != 0
		a.ModelMappings = append(a.ModelMappings, m)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	rows, err = db.QueryContext(ctx, s.bind(`SELECT t.name FROM `+s.table("account_tag_bindings")+` b JOIN `+s.table("account_tags")+` t ON t.id=b.tag_id WHERE b.account_id=? AND b.system_account_id=? ORDER BY t.name,t.id`), a.ID, a.SystemAccountID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			rows.Close()
			return err
		}
		a.Tags = append(a.Tags, tag)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	rows, err = db.QueryContext(ctx, s.bind(`SELECT id,key_fingerprint,status FROM `+s.table("account_api_key_runtime_states")+` WHERE account_id=? AND system_account_id=? ORDER BY id`), a.ID, a.SystemAccountID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var key APIKeyBinding
		if err := rows.Scan(&key.ID, &key.Fingerprint, &key.Status); err != nil {
			rows.Close()
			return err
		}
		a.APIKeyBindings = append(a.APIKeyBindings, key)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	return nil
}

func equivalentCreate(a Account, in CreateInput) bool {
	// 读侧 ProviderCode 是 vendor_code 投影（generic 为空串），入参仍是
	// 旧 provider 语义，比较前按同一推导规则归一。
	vendorCode, _ := vendorIdentityForProvider(strings.TrimSpace(in.ProviderCode))
	if a.ID != in.ID || a.SystemAccountID != in.SystemAccountID || a.ProviderCode != vendorCode || a.ProtocolCode != in.ProtocolCode || a.ProtocolVersion != in.ProtocolVersion || a.Name != in.Name || a.Type != in.Type || a.Status != in.Status || a.Schedulable != in.Schedulable || !sameOptionalString(a.AvailabilityScheduleJSON, in.AvailabilityScheduleJSON) {
		return false
	}
	return sameStrings(a.SupportedModels, in.SupportedModels) && sameMappings(a.ModelMappings, in.ModelMappings) && sameStrings(a.Tags, in.Tags) && sameKeys(a.APIKeyBindings, in.APIKeyBindings)
}

func sameOptionalString(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
func sameStrings(a, b []string) bool {
	a, b = sortedStrings(unique(a)), sortedStrings(unique(b))
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func sameMappings(a, b []ModelMapping) bool {
	a, b = sortedMappings(a), sortedMappings(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func sameKeys(a, b []APIKeyBinding) bool {
	for index := range a {
		a[index].Status = defaultStatus(a[index].Status)
	}
	for index := range b {
		b[index].Status = defaultStatus(b[index].Status)
	}
	a, b = sortedKeys(a), sortedKeys(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sortedStrings(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
func sortedMappings(values []ModelMapping) []ModelMapping {
	out := append([]ModelMapping(nil), values...)
	sort.Slice(out, func(i, j int) bool { return mappingKey(out[i]) < mappingKey(out[j]) })
	return out
}
func sortedKeys(values []APIKeyBinding) []APIKeyBinding {
	out := append([]APIKeyBinding(nil), values...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func mappingKey(m ModelMapping) string {
	return m.SourceModel + "\x00" + m.SourceEndpointFamily + "\x00" + m.UpstreamModel + "\x00" + m.UpstreamEndpointFamily + "\x00" + m.TargetConnectionPresetCode + fmt.Sprintf("\x00%t", m.Enabled)
}
func unique(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
func defaultStatus(v string) string {
	if strings.TrimSpace(v) == "" {
		return "active"
	}
	return v
}
