package main

// G20 phase-2 composition-root chat generation-wave ports (the
// /__aisys__/api/my-chat family dispatches model/image/compaction work into
// the internal gateway chain).
//
// Node authority:
//   - modules/chat/chat-gateway-dispatch.ts dispatchChatGatewayRequest: an
//     HTTP fetch against `http://127.0.0.1:${runtimeConfig.port}` (the same
//     process' /v1 entry) with header/body passthrough and a streaming
//     response. The Go executor below keeps the equivalence but drops the
//     loopback hop: it drives the assembled /v1 chain handler in-process with
//     an io.Pipe body so SSE streaming stays live.
//   - runtime/listCachedOpenAIAccountsForGroupAsync +
//     listCachedProviderModelCatalogAsync: the model catalog port over the
//     runtime cache.
//   - storage/gateway-api-key.repository.ts validateGatewayApiKeyAsync: the
//     gateway key validation port over the runtime cache read.
//   - storage/chat-asset-storage.ts: the local object store (one file per
//     storage key under JUHE_AI_CHAT_ASSETS_ROOT).
//   - gpt-tokenizer: token counting via the o200k tokenizer.
//
// G20 phase-3 mounts these ports together (chain_chat_keys.go ChatAPIKeyProvider,
// chain_chat_images.go ImageProcessor, chain_chat_observation.go
// ImageObservations, chain_chat_mount.go the chat database owner + Deps
// assembly at ${systemApiPrefix}/my-chat).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprobe"
	"github.com/huanminabc/juhe-ai/backend-go-platform/safego"
)

// chatGatewayExecutor implements chat.GenerationExecutor by serving the
// request through the in-process /v1 chain handler (dispatchChatGatewayRequest
// equivalence: same path/header/body contract, streaming response body, the
// origin hop replaced by a direct handler call).
type chatGatewayExecutor struct {
	// chain is the assembled /v1 gateway chain handler.
	chain http.Handler
	// dispatchTarget 是 AI 问答会话绑定账户的调度覆盖目标：绑定账户非空时
	// Dispatch 注入进程内 context；零值（外部请求）调度行为与现状一致。
	dispatchTarget chatDispatchTarget
}

func newChatGatewayExecutor(chain http.Handler) *chatGatewayExecutor {
	return &chatGatewayExecutor{chain: chain}
}

// WithChatDispatchAccount 实现聊天侧的调度覆盖端口
// （chat.chatDispatchTargetAware 同形）：绑定账户非空返回目标的执行器视图；
// 空账户返回原执行器实例，不产生任何包装或行为差异。
func (e *chatGatewayExecutor) WithChatDispatchAccount(accountID string) chat.GenerationExecutor {
	target := chatDispatchTarget{AccountID: accountID}
	if !target.pinned() {
		return e
	}
	return &chatGatewayExecutor{chain: e.chain, dispatchTarget: target}
}

// chatPipeWriter is the http.ResponseWriter that streams the chain response
// into the executor's pipe once headers commit.
type chatPipeWriter struct {
	header     http.Header
	status     int
	headerCh   chan struct{}
	closeOnce  sync.Once
	headerOnce sync.Once
	body       *io.PipeWriter
}

func newChatPipeWriter(body *io.PipeWriter) *chatPipeWriter {
	return &chatPipeWriter{header: http.Header{}, headerCh: make(chan struct{}), body: body}
}

func (w *chatPipeWriter) Header() http.Header { return w.header }

func (w *chatPipeWriter) commitHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.headerOnce.Do(func() { close(w.headerCh) })
}

func (w *chatPipeWriter) Write(p []byte) (int, error) {
	w.commitHeader(http.StatusOK)
	return w.body.Write(p)
}

func (w *chatPipeWriter) WriteHeader(status int) {
	w.commitHeader(status)
}

func (w *chatPipeWriter) finish() {
	w.closeOnce.Do(func() { _ = w.body.Close() })
}

// Dispatch mirrors dispatchChatGatewayRequest: build the RequestInit-shaped
// request, run the chain, and resolve as soon as the response headers commit
// (the body keeps streaming through the returned reader).
func (e *chatGatewayExecutor) Dispatch(ctx context.Context, req chat.GenerationDispatchRequest) (*chat.GenerationDispatchResponse, error) {
	if e == nil || e.chain == nil {
		return nil, errors.New("chat gateway executor 未接线")
	}
	path := req.Path
	if path == "" {
		path = "/v1/chat/completions"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	reader, pipe := io.Pipe()
	target := newChatPipeWriter(pipe)
	method := strings.TrimSpace(req.Method)
	if method == "" {
		method = http.MethodPost
	}
	// 调度覆盖通道：仅当本执行器携带绑定账户目标时注入进程内 context；外部
	// HTTP 请求的 context 由服务端构造，无法携带该未导出 key，不存在伪造通道。
	if e.dispatchTarget.pinned() {
		ctx = contextWithChatDispatchTarget(ctx, e.dispatchTarget)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, "http://chat-gateway.internal"+path, bytes.NewReader(req.Body))
	if err != nil {
		_ = pipe.Close()
		return nil, fmt.Errorf("构建 chat 网关请求失败: %w", err)
	}
	for key, value := range req.Headers {
		httpReq.Header.Set(key, value)
	}
	done := make(chan struct{})
	go func() {
		defer safego.Recover("juheai.chain_chat.serve")
		defer close(done)
		defer target.finish()
		e.chain.ServeHTTP(target, httpReq)
	}()
	// Node fetch resolves when the response headers arrive; the goroutine
	// keeps pumping the body into the pipe until the handler returns.
	select {
	case <-target.headerCh:
	case <-done:
	case <-ctx.Done():
		_ = pipe.Close()
		<-done
		return nil, ctx.Err()
	}
	status := target.status
	if status == 0 {
		status = http.StatusOK
	}
	return &chat.GenerationDispatchResponse{
		Status: status,
		Header: target.Header(),
		Body:   reader,
	}, nil
}

// chatModelCatalog implements chat.ModelCatalog over the runtime cache
// (Node listCachedOpenAIAccountsForGroupAsync + listCachedProviderModelCatalogAsync).
type chatModelCatalog struct {
	cache *gatewayruntimecache.Service
}

func (c chatModelCatalog) ListAccountsForGroup(groupID, systemAccountID, requestedModel, endpointFamily string) []chat.ChatTransportAccount {
	if c.cache == nil {
		return nil
	}
	accounts, err := c.cache.ListCachedOpenAIAccountsForGroupAsync(context.Background(), groupID, systemAccountID, gatewayruntimecache.CachedOpenAIAccountsForGroupOptions{
		RequestedModel:          requestedModel,
		RequestedEndpointFamily: endpointFamily,
	})
	if err != nil {
		return nil
	}
	return chatTransportAccountsFromSecrets(accounts)
}

// ListChatPinnedAccountsForGroup 实现会话绑定作用域的 pinned 直取视图（AI 问答
// 设计 §5.2 2026-10-10 修订）：按（分组, 账户 ID）经 runtime cache 专用路径
// 直取运行时快照，含非 active/冷却/过期/禁调度账户；账户不在该分组或分组
// 禁用/不存在时返回空切片。
func (c chatModelCatalog) ListChatPinnedAccountsForGroup(groupID, systemAccountID, accountID string) []chat.ChatTransportAccount {
	if c.cache == nil {
		return nil
	}
	accounts, err := c.cache.ListFreshOpenAIAccountsForChatPinnedAsync(context.Background(), groupID, systemAccountID, accountID)
	if err != nil {
		return nil
	}
	return chatTransportAccountsFromSecrets(accounts)
}

// chatTransportAccountsFromSecrets 是 runtime cache 账户快照到 chat 传输视图
// 的类型投影（ListAccountsForGroup 与 ListChatPinnedAccountsForGroup 共用）。
func chatTransportAccountsFromSecrets(accounts []gatewayruntimecache.OpenAIAccountSecret) []chat.ChatTransportAccount {
	out := make([]chat.ChatTransportAccount, 0, len(accounts))
	for _, account := range accounts {
		mappings := make([]chat.ChatTransportModelMapping, 0, len(account.ModelMappings))
		for _, mapping := range account.ModelMappings {
			enabled := mapping.Enabled
			mappings = append(mappings, chat.ChatTransportModelMapping{
				Enabled:                &enabled,
				SourceModel:            mapping.SourceModel,
				UpstreamModel:          mapping.UpstreamModel,
				SourceEndpointFamily:   mapping.SourceEndpointFamily,
				UpstreamEndpointFamily: mapping.UpstreamEndpointFamily,
			})
		}
		out = append(out, chat.ChatTransportAccount{
			ID:                     account.ID,
			Type:                   account.Type,
			ProviderCode:           account.ProviderCode,
			SupportedEndpointModes: account.SupportedEndpointModes,
			SupportedModels:        account.SupportedModels,
			ModelMappings:          mappings,
		})
	}
	return out
}

func (c chatModelCatalog) ListProviderCatalog(providerCode, systemAccountID string) []chat.ProviderModelCatalogItem {
	if c.cache == nil {
		return nil
	}
	items, err := c.cache.ListCachedProviderModelCatalogAsync(context.Background(), gatewayruntimecache.ModelCatalogListOptions{
		ProviderCode: providerCode,
		// chat 面与计费解耦：未定价模型照常出现在 chat 模型列表/能力/默认推举
		// （对齐 Node includeUnpriced:true；无价行仅在计费口径被目录源剔除）。
		IncludeUnpriced: true,
		SystemAccountID: systemAccountID,
	})
	if err != nil {
		return nil
	}
	// Marshal→Unmarshal 往返是 runtimecache 大结构到 chat 传输小结构的类型
	// 投影：两侧均为纯 JSON 数据（字段 tag 同形），往返恒成功（w2 登记）。
	encoded, _ := json.Marshal(items)
	var out []chat.ProviderModelCatalogItem
	_ = json.Unmarshal(encoded, &out)
	return out
}

// chatGatewayKeyValidator implements chat.GatewayKeyValidator over the
// runtime cache gateway key read (validateGatewayApiKeyAsync projection).
type chatGatewayKeyValidator struct {
	cache *gatewayruntimecache.Service
}

func (v chatGatewayKeyValidator) ValidateGatewayKey(secret string) (*chat.GatewayKeyView, error) {
	if v.cache == nil {
		return nil, errors.New("chat gateway key validator 未接线")
	}
	runtime, err := v.cache.ReadCachedGatewayRuntimeAsync(context.Background(), secret)
	if err != nil {
		return nil, err
	}
	if runtime.APIKey == nil {
		return nil, nil
	}
	view := &chat.GatewayKeyView{
		GroupBindings:          make([]chat.GatewayGroupBinding, 0, len(runtime.APIKey.GroupBindings)),
		ImageGenerationEnabled: runtime.APIKey.SystemAccountImageGenerationEnabled == 1,
	}
	for _, binding := range runtime.APIKey.GroupBindings {
		view.GroupBindings = append(view.GroupBindings, chat.GatewayGroupBinding{
			GroupID:      binding.GroupID,
			Status:       binding.Status,
			GroupEnabled: binding.GroupEnabled == 1,
		})
	}
	return view, nil
}

// chatAssetObjectStore implements chat.ObjectStore over the chat assets root
// (Node chat-asset-storage.ts: one blob per storage key, write-guarded by the
// expected SHA-256 and the size cap).
type chatAssetObjectStore struct {
	root string
}

func newChatAssetObjectStore(root string) (*chatAssetObjectStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("chat 资产根目录未配置（JUHE_AI_CHAT_ASSETS_ROOT）")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("创建 chat 资产根目录失败: %w", err)
	}
	return &chatAssetObjectStore{root: root}, nil
}

func (s *chatAssetObjectStore) path(storageKey string) (string, error) {
	clean := filepath.Clean("/" + storageKey)
	if strings.Contains(clean, "..") {
		return "", fmt.Errorf("chat 资产存储键不合法: %q", storageKey)
	}
	return filepath.Join(s.root, filepath.FromSlash(clean[1:])), nil
}

func (s *chatAssetObjectStore) Write(storageKey string, data []byte, maxBytes int64, expectedSHA256 string) error {
	if int64(len(data)) > maxBytes {
		return fmt.Errorf("chat 资产超过大小上限 %d", maxBytes)
	}
	sum := sha256.Sum256(data)
	if expectedSHA256 != "" && !strings.EqualFold(hex.EncodeToString(sum[:]), expectedSHA256) {
		return errors.New("chat 资产内容校验失败")
	}
	target, err := s.path(storageKey)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, data, 0o644)
}

func (s *chatAssetObjectStore) Open(storageKey string, maxBytes int64) ([]byte, int64, error) {
	target, err := s.path(storageKey)
	if err != nil {
		return nil, 0, err
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return nil, 0, err
	}
	if int64(len(data)) > maxBytes {
		return nil, 0, fmt.Errorf("chat 资产超过大小上限 %d", maxBytes)
	}
	return data, int64(len(data)), nil
}

func (s *chatAssetObjectStore) Delete(storageKeys []string) error {
	var firstErr error
	for _, storageKey := range storageKeys {
		if strings.TrimSpace(storageKey) == "" {
			continue
		}
		target, err := s.path(storageKey)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// chatTokenCount adapts the o200k tokenizer onto chat.TokenCountFunc; a
// tokenizer construction failure degrades to the bytes/4 estimate the Node
// pipeline uses as the last resort.
func newChatTokenCount() (chat.TokenCountFunc, error) {
	tokenizer, err := modelcheckprobe.NewO200kTokenizer()
	if err != nil {
		return nil, fmt.Errorf("创建 chat tokenizer 失败: %w", err)
	}
	return func(text string) int {
		count, countErr := tokenizer.Count(text)
		if countErr != nil {
			return (len(text) + 3) / 4
		}
		return count
	}, nil
}
