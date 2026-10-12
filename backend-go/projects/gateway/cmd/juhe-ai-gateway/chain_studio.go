package main

// 媒体工作室（媒体工作室设计 §3.5/§4.1）组合根端口：purpose=studio 专用
// Key 的幂等确保与 studio_domains_json 域授权读写、域模型目录聚合。资产对
// 象存储复用 chat 资产同款本地对象存储（chat_assets.storage_key 同源，不新
// 造存储栈，见 chain_studio_mount.go）。
//
// Key 先例：chain_chat_keys.go 的 EnsureChatAPIKey（默认资源收口自愈 + 并发
// 竞态恢复）；EnsureStudioKey 镜像同一机制，差异仅在 purpose='studio'、基名
// 与恒写四域 studio_domains_json（§3.5.3，P0 不提供编辑入口）。

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/studio"
	"github.com/huanminabc/juhe-ai/backend-go-platform/safego"
)

// studioBaseAPIKeyName 是 ensured studio Key 的名称基名（同名冲突按序号避
// 让，链上 chainNextDefaultNameFromExisting 先例）。
const studioBaseAPIKeyName = "媒体工作室 API Key"

// studioAPIKeyProvider 在 chat Key 供应器之上承载 studio Key 生命周期：默
// 认分组/默认策略路由自愈与名称避让直接复用 chat 同款实现（默认资源收口
// 语义一致，§3.5.4），本类型只补 purpose='studio' 的探测/插入/域授权列。
type studioAPIKeyProvider struct {
	*chatAPIKeyProvider
}

func newStudioAPIKeyProvider(db *sql.DB, postgres bool, secret string) *studioAPIKeyProvider {
	return &studioAPIKeyProvider{chatAPIKeyProvider: newChatAPIKeyProvider(db, postgres, secret)}
}

// EnsureStudioKey 镜像 EnsureChatAPIKey（§3.5.4）：自愈默认 GPT 分组与默认
// 策略路由 → 探测现存 purpose='studio' Key → 命中复用 → 否则插入新行（恒写
// 四域 studio_domains_json）；INSERT 撞唯一索引（purpose partial index 或
// 同名避让）时重探并复用胜者行，不报错。
func (p *studioAPIKeyProvider) EnsureStudioKey(ownerID string) (string, error) {
	timestamp := p.now().UTC().Format(chainTimeLayout)
	if err := p.ensureDefaultGPTGroup(ownerID, timestamp); err != nil {
		return "", err
	}
	if err := p.ensureDefaultRouteStrategies(ownerID, timestamp); err != nil {
		return "", err
	}
	existing, err := p.studioApiKeyIdForSystemAccount(ownerID)
	if err != nil {
		return "", err
	}
	if existing != "" {
		return existing, nil
	}
	routeStrategy, err := p.defaultGptRouteStrategyForSystemAccount(ownerID)
	if err != nil {
		return "", err
	}
	if routeStrategy == nil {
		return "", errors.New("创建媒体工作室 API Key 前必须先创建 GPT 默认策略路由")
	}
	apiKeyID := p.newID("key")
	key := apikeys.NewAPIKey()
	name, err := p.nextDefaultApiKeyName(ownerID, studioBaseAPIKeyName)
	if err != nil {
		return "", err
	}
	sealed, err := apikeys.EncryptJSON(p.secret, map[string]string{"key": key})
	if err != nil {
		return "", err
	}
	description := fmt.Sprintf("媒体工作室专用 API Key，默认绑定%s，可在 API Key 页面修改策略路由。", routeStrategy.name)
	_, err = p.db.Exec(p.bind(fmt.Sprintf(`INSERT INTO %s (
			id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix,
			key_secret_encrypted, status, is_default, purpose, studio_domains_json, expires_at,
			quota_limits_json, availability_schedule_json, availability_schedule_next_check_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', 0, 'studio', ?, NULL, NULL, NULL, NULL, ?, ?)`, p.table("api_keys"))),
		apiKeyID, ownerID, routeStrategy.id, name, description,
		apikeys.HashSecret(key), key[:8], key[len(key)-8:],
		sealed, studio.StudioAllDomainsJSON, timestamp, timestamp)
	if err != nil {
		// 并发竞态恢复（§3.5.4）：另一个 ensure 赢得 studio purpose 唯一索引
		// 或同名唯一索引时，重探并复用胜者行。
		raced, raceErr := p.studioApiKeyIdForSystemAccount(ownerID)
		if raceErr == nil && raced != "" && (isDuplicateStudioAPIKeyError(err) || isDuplicateAPIKeyNameError(err)) {
			return raced, nil
		}
		return "", err
	}
	return apiKeyID, nil
}

// StudioKeyDomains 读取该用户 studio Key 的域授权（studio_domains_json，§3.5.3
// 授权数据；/v1 preauth 域校验批次消费）。Key 不存在返回 (nil, nil)。
func (p *studioAPIKeyProvider) StudioKeyDomains(ownerID string) ([]string, error) {
	var raw sql.NullString
	err := p.db.QueryRow(p.bind(fmt.Sprintf(`SELECT studio_domains_json FROM %s
		WHERE system_account_id = ? AND purpose = 'studio' LIMIT 1`, p.table("api_keys"))), ownerID).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !raw.Valid {
		return studio.ParseStudioDomains("")
	}
	return studio.ParseStudioDomains(raw.String)
}

// FindStudioKeySecret 实现 studio.StudioKeyProvider 的明文查找端口（chat 的
// FindChatAPIKey 同款查询：active 且未过期；解密失败按显式错误）。生成链受理
// 前消费（/v1 Bearer 载荷源）。
func (p *studioAPIKeyProvider) FindStudioKeySecret(keyID, ownerID string) (string, error) {
	now := p.now().UTC().Format(chainTimeLayout)
	var secretEncrypted string
	query := fmt.Sprintf(`SELECT key_secret_encrypted FROM %s
		WHERE id = ? AND system_account_id = ? AND status = 'active' AND (expires_at IS NULL OR expires_at > ?) LIMIT 1`, p.table("api_keys"))
	err := p.db.QueryRow(p.bind(query), keyID, ownerID, now).Scan(&secretEncrypted)
	if err == sql.ErrNoRows {
		return "", errors.New("媒体工作室 API Key 不存在或已失效")
	}
	if err != nil {
		return "", err
	}
	var envelope struct {
		Key string `json:"key"`
	}
	if err := apikeys.DecryptJSON(p.secret, secretEncrypted, &envelope); err != nil || envelope.Key == "" {
		return "", fmt.Errorf("API Key %s 的密钥数据无效", keyID)
	}
	return envelope.Key, nil
}

// studioApiKeyIdForSystemAccount 探测该用户现存 purpose='studio' Key id。
func (p *studioAPIKeyProvider) studioApiKeyIdForSystemAccount(ownerID string) (string, error) {
	var id string
	err := p.db.QueryRow(p.bind(fmt.Sprintf(`SELECT id FROM %s WHERE system_account_id = ? AND purpose = 'studio' LIMIT 1`, p.table("api_keys"))), ownerID).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

// isDuplicateStudioAPIKeyError 识别并发 ensure 撞 idx_api_keys_studio_purpose_unique
// 的两族报错（PG 命名约束 / SQLite 索引名冲突），同 chain 聊天 Key 先例。
func isDuplicateStudioAPIKeyError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "idx_api_keys_studio_purpose_unique") ||
		strings.Contains(message, "UNIQUE constraint failed: api_keys.system_account_id")
}

// --- 域模型目录（studio.ModelCatalog） ---

// studioModelCatalog 在既有目录读链上按域过滤（§4.1：目录来源 =
// provider_model_catalog 静态快照按 mode 过滤 + custom_provider_models +
// hybrid 账户映射，工作室不新造目录）：provider 集合 = 当前用户两档范围内
// capability_domain 命中请求域的账户 provider 去重集；目录行谓词与 /v1 目
// 录读链同款（chainIsSupportedCatalogModel），图像域按 /v1 图像车道模型词
// 表（gatewayopenai.IsImageGenerationModel）判定，不新造词表。
type studioModelCatalog struct {
	cache    *gatewayruntimecache.Service
	accounts *accounts.Store
}

var (
	studioAudioProtocols = map[string]bool{"audio_speech": true, "audio_transcription": true}
	studioVideoProtocols = map[string]bool{"video": true}
)

// ListDomainModels 聚合请求域的可派发模型最小集（model/providerCode/domain/
// endpointModes/capabilities）。capabilities P0 恒为空对象：既有目录读链无
// 域能力载荷来源，按"能力接口驱动表单、不猜测"契约（§4.1/§8.1）不编造参数
// 项。
func (c studioModelCatalog) ListDomainModels(ctx context.Context, viewerID string, adminScope bool, domain string) ([]studio.StudioModel, error) {
	if c.accounts == nil {
		return nil, errors.New("studio 模型目录缺少账户范围端口")
	}
	if c.cache == nil {
		return nil, errors.New("studio 模型目录缺少 runtime cache")
	}
	providerOptions, err := c.accounts.ListStudioAccountOptions(ctx, viewerID, adminScope, domain)
	if err != nil {
		return nil, err
	}
	seenProvider := map[string]bool{}
	providers := []string{}
	for _, option := range providerOptions {
		code := chainNormalizeProviderToken(option.ProviderCode)
		if code == "" || seenProvider[code] {
			continue
		}
		seenProvider[code] = true
		providers = append(providers, code)
	}
	out := []studio.StudioModel{}
	seenModel := map[string]bool{}
	for _, code := range providers {
		// 直取 runtime cache 目录读链（chain_chat.go ListProviderCatalog 同
		// 源、IncludeUnpriced 对齐 chat 面口径），保留 Mode/协议原值供域谓词
		// 消费（chat 传输投影会丢弃 Mode，故不经其手）。
		items, itemsErr := c.cache.ListCachedProviderModelCatalogAsync(ctx, gatewayruntimecache.ModelCatalogListOptions{
			ProviderCode:    code,
			IncludeUnpriced: true,
			SystemAccountID: viewerID,
		})
		if itemsErr != nil {
			return nil, itemsErr
		}
		for _, item := range items {
			modes, ok := studioDomainEndpointModes(item, domain)
			if !ok {
				continue
			}
			key := item.Model + "\x00" + item.ProviderCode
			if seenModel[key] {
				continue
			}
			seenModel[key] = true
			out = append(out, studio.StudioModel{
				Model:         item.Model,
				ProviderCode:  item.ProviderCode,
				Domain:        domain,
				EndpointModes: modes,
				Capabilities:  map[string]any{},
			})
		}
	}
	return out, nil
}

// studioDomainEndpointModes 判定目录行是否属于请求域，并给出端点模式词表：
// audio = mode 音频标注且协议含 speech/transcription（词表原值透出，STT/TTS
// 页签据此过滤）；video = mode video 且协议含 video（创建形态 video_create）；
// image = 非 audio/video 标注、过目录可见性谓词且命中 /v1 图像模型词表
// （images_json）。未知域返回 false。
func studioDomainEndpointModes(item gatewayruntimecache.ProviderModelCatalogItem, domain string) ([]string, bool) {
	mode := ""
	if item.Mode != nil {
		mode = strings.ToLower(strings.TrimSpace(*item.Mode))
	}
	modeIsAudio := mode == "audio" || mode == "audio_speech" || mode == "audio_transcription"
	modeIsVideo := mode == "video"
	protocols := map[string]bool{}
	for _, protocol := range item.SupportedAPIProtocols {
		protocols[protocol] = true
	}
	hasProtocol := func(set map[string]bool) bool {
		for protocol := range protocols {
			if set[protocol] {
				return true
			}
		}
		return false
	}
	switch domain {
	case "audio":
		if !modeIsAudio || !hasProtocol(studioAudioProtocols) {
			return nil, false
		}
		modes := []string{}
		for _, protocol := range item.SupportedAPIProtocols {
			if studioAudioProtocols[protocol] {
				modes = append(modes, protocol)
			}
		}
		return modes, true
	case "video":
		if !modeIsVideo || !hasProtocol(studioVideoProtocols) {
			return nil, false
		}
		return []string{"video_create"}, true
	case "image":
		if modeIsAudio || modeIsVideo {
			return nil, false
		}
		if !chainIsSupportedCatalogModel(item) {
			return nil, false
		}
		if !gatewayopenai.IsImageGenerationModel(item.Model) {
			return nil, false
		}
		return []string{"images_json"}, true
	}
	return nil, false
}

// --- 进程内 /v1 派发链执行器（studio.GatewayExecutor） ---

// studioGatewayExecutor 实现 studio.GatewayExecutor：进程内直驱装配好的 /v1
// 网关链（chatGatewayExecutor 同款进程内受理模式，媒体工作室设计 §4.5.1：
// 不经回环 HTTP，不新造协议面）。studio 消费的端点都是有限体（图像/音频字节
// 或小 JSON），响应缓冲读，无流式管道需求。
type studioGatewayExecutor struct {
	chain http.Handler
}

func newStudioGatewayExecutor(chain http.Handler) *studioGatewayExecutor {
	return &studioGatewayExecutor{chain: chain}
}

// studioBufferWriter 是缓冲响应写出器（链写完即返回）。
type studioBufferWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *studioBufferWriter) Header() http.Header { return w.header }

func (w *studioBufferWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(p)
}

func (w *studioBufferWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

// Dispatch 构造 /v1 请求、同步驱动链并缓冲响应。DispatchAccount 非空时注入进
// 程内调度覆盖目标（§4.5.3 账户收敛：chat 绑定账户同款未导出 context 通道，
// 外部 HTTP 请求无法伪造）。
func (e *studioGatewayExecutor) Dispatch(ctx context.Context, req studio.GatewayDispatchRequest) (*studio.GatewayDispatchResponse, error) {
	if e == nil || e.chain == nil {
		return nil, errors.New("studio gateway executor 未接线")
	}
	path := req.Path
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if account := strings.TrimSpace(req.DispatchAccount); account != "" {
		ctx = contextWithChatDispatchTarget(ctx, chatDispatchTarget{AccountID: account})
	}
	method := strings.TrimSpace(req.Method)
	if method == "" {
		method = http.MethodPost
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, "http://studio-gateway.internal"+path, bytes.NewReader(req.Body))
	if err != nil {
		return nil, fmt.Errorf("构建 studio 网关请求失败: %w", err)
	}
	for key, value := range req.Headers {
		httpReq.Header.Set(key, value)
	}
	recorder := &studioBufferWriter{header: http.Header{}}
	done := make(chan struct{})
	go func() {
		defer safego.Recover("juheai.chain_studio.serve")
		defer close(done)
		e.chain.ServeHTTP(recorder, httpReq)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	status := recorder.status
	if status == 0 {
		status = http.StatusOK
	}
	return &studio.GatewayDispatchResponse{
		Status: status,
		Header: recorder.header,
		Body:   append([]byte(nil), recorder.body.Bytes()...),
	}, nil
}
