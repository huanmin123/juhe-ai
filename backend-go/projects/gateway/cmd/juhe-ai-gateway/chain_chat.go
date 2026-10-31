package main

// 演练场派发执行器与目录/传输投影（docs/functions/演练场设计.md §3.1）：
// chatGatewayExecutor 把演练场固定派发端口直驱到进程内 /v1 链 handler（io.Pipe
// 保活 SSE 流式），chatModelCatalog 把 runtime cache 的 pinned 账户快照与模型
// 目录投影为传输小结构。原 my-chat 会话面专属的资产对象存储与 token 计数
// 适配随会话面退役删除（§8）。
//
// 调度覆盖与内部身份两通道（chain_chat_dispatch.go / chain_chat_identity.go）
// 的注入点在本文件 Dispatch：绑定账户目标非空时注入进程内 context，/v1 链
// preauth 收敛候选窗口并跳过 Key 鉴权；外部请求两条通道都无法伪造。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-platform/safego"
)

// chatGatewayExecutor implements GenerationExecutor by serving the
// request through the in-process /v1 chain handler (dispatchChatGatewayRequest
// equivalence: same path/header/body contract, streaming response body, the
// origin hop replaced by a direct handler call).
type chatGatewayExecutor struct {
	// chain is the assembled /v1 gateway chain handler.
	chain http.Handler
	// dispatchTarget 是 AI 账户绑定派发的调度覆盖目标：绑定账户非空时
	// Dispatch 注入进程内 context；零值（外部请求）调度行为与现状一致。
	dispatchTarget chatDispatchTarget
	// internalIdentity 是派发归属系统账户的内部身份（chatInternalIdentity，
	// chain_chat_identity.go）：非零时 Dispatch 注入内部身份 context，/v1 链
	// preauth 跳过 API Key 鉴权并抑制使用日志（W-A 批契约）；零值表示不走
	// 内部通道（外部请求恒零值，两条通道都无法伪造）。
	internalIdentity chatInternalIdentity
}

func newChatGatewayExecutor(chain http.Handler) *chatGatewayExecutor {
	return &chatGatewayExecutor{chain: chain}
}

// WithChatDispatchAccount 实现聊天侧的调度覆盖端口：绑定账户非空返回目标的
// 执行器视图，同时携带 ownerID 作为内部身份（免 Key 直调后派发鉴权走内部身
// 份通道）；空账户返回原执行器实例，不产生任何包装或行为差异。
func (e *chatGatewayExecutor) WithChatDispatchAccount(accountID, ownerID string) GenerationExecutor {
	target := chatDispatchTarget{AccountID: accountID}
	if !target.pinned() {
		return e
	}
	return &chatGatewayExecutor{chain: e.chain, dispatchTarget: target, internalIdentity: chatInternalIdentity{OwnerSystemAccountID: ownerID}}
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

// Dispatch 镜像原 dispatchChatGatewayRequest 语义：构造 RequestInit 形状请求、
// 驱动链 handler，响应头一提交即返回（响应体继续经返回的 reader 流式泵出）。
func (e *chatGatewayExecutor) Dispatch(ctx context.Context, req GenerationDispatchRequest) (*GenerationDispatchResponse, error) {
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
	// 调度覆盖与内部身份同源注入（同一 per-session 视图，WithChatDispatchAccount
	// 一并携带）；外部请求两条通道都恒空。
	if e.dispatchTarget.pinned() {
		ctx = contextWithChatDispatchTarget(ctx, e.dispatchTarget)
		if e.internalIdentity.OwnerSystemAccountID != "" {
			ctx = contextWithChatInternalIdentity(ctx, e.internalIdentity)
		}
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
	// 响应头到达即解析完成；goroutine 继续把响应体泵入管道直到 handler 返回。
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
	return &GenerationDispatchResponse{
		Status: status,
		Header: target.Header(),
		Body:   reader,
	}, nil
}

// chatModelCatalog 把 runtime cache 的账户快照与模型目录投影为传输小结构
// （演练场传输账户/供应商目录/账户目录端口的生产实现）。
type chatModelCatalog struct {
	cache *gatewayruntimecache.Service
}

// ListChatPinnedAccountsForGroup 实现绑定作用域的 pinned 直取视图（AI 问答
// 设计 §5.2 2026-10-10 修订，演练场沿用）：按（分组, 账户 ID）经 runtime
// cache 专用路径直取运行时快照，含非 active/冷却/过期/禁调度账户；账户不在
// 该分组或分组禁用/不存在时返回空切片。
func (c chatModelCatalog) ListChatPinnedAccountsForGroup(groupID, systemAccountID, accountID string) []ChatTransportAccount {
	if c.cache == nil {
		return nil
	}
	accounts, err := c.cache.ListFreshOpenAIAccountsForChatPinnedAsync(context.Background(), groupID, systemAccountID, accountID)
	if err != nil {
		return nil
	}
	return chatTransportAccountsFromSecrets(accounts)
}

// chatTransportAccountsFromSecrets 是 runtime cache 账户快照到传输视图的类
// 型投影（ListChatPinnedAccountsForGroup 消费）。
func chatTransportAccountsFromSecrets(accounts []gatewayruntimecache.OpenAIAccountSecret) []ChatTransportAccount {
	out := make([]ChatTransportAccount, 0, len(accounts))
	for _, account := range accounts {
		mappings := make([]ChatTransportModelMapping, 0, len(account.ModelMappings))
		for _, mapping := range account.ModelMappings {
			enabled := mapping.Enabled
			mappings = append(mappings, ChatTransportModelMapping{
				Enabled:                &enabled,
				SourceModel:            mapping.SourceModel,
				UpstreamModel:          mapping.UpstreamModel,
				SourceEndpointFamily:   mapping.SourceEndpointFamily,
				UpstreamEndpointFamily: mapping.UpstreamEndpointFamily,
			})
		}
		out = append(out, ChatTransportAccount{
			ID:                     account.ID,
			Type:                   account.Type,
			ProviderCode:           account.VendorCode,
			SupportedEndpointModes: account.SupportedEndpointModes,
			SupportedModels:        account.SupportedModels,
			ModelMappings:          mappings,
		})
	}
	return out
}

func (c chatModelCatalog) ListProviderCatalog(providerCode, systemAccountID string) []ProviderModelCatalogItem {
	if c.cache == nil {
		return nil
	}
	items, err := c.cache.ListCachedProviderModelCatalogAsync(context.Background(), gatewayruntimecache.ModelCatalogListOptions{
		ProviderCode: providerCode,
		// 目录与计费解耦：未定价模型照常出现在模型列表/能力/默认推举（对齐
		// Node includeUnpriced:true；无价行仅在计费口径被目录源剔除）。
		IncludeUnpriced: true,
		SystemAccountID: systemAccountID,
	})
	if err != nil {
		return nil
	}
	return chatCatalogItemsFromRuntime(items)
}

// ListAccountSupportedModels：generic_http 账户按 account_supported_models.
// model_id 取真实 models 行（完整元数据）。
func (c chatModelCatalog) ListAccountSupportedModels(accountID, systemAccountID string) []ProviderModelCatalogItem {
	if c.cache == nil {
		return nil
	}
	items, err := c.cache.ListAccountSupportedModelCatalogAsync(context.Background(), accountID, systemAccountID)
	if err != nil {
		return nil
	}
	return chatCatalogItemsFromRuntime(items)
}

// chatCatalogItemsFromRuntime 是 runtimecache 大结构到传输小结构的类型投影：
// 两侧均为纯 JSON 数据（字段 tag 同形），往返恒成功（w2 登记）。
func chatCatalogItemsFromRuntime(items []gatewayruntimecache.ProviderModelCatalogItem) []ProviderModelCatalogItem {
	encoded, _ := json.Marshal(items)
	var out []ProviderModelCatalogItem
	_ = json.Unmarshal(encoded, &out)
	return out
}
