package main

// 演练场派发执行器（docs/functions/演练场设计.md §3.1）：chatGatewayExecutor
// 把演练场派发端口直驱到进程内 /v1 链 handler（io.Pipe 保活 SSE 流式）。派发
// 请求携带真实 API Key 明文（Authorization: Bearer，由组合根适配层解密注入），
// 走完整 /v1 鉴权/调度/计费链。原固定派发与内部身份通道
// （chain_chat_dispatch.go / chain_chat_identity.go）随演练场真实 Key 化退役。
//
// 原 my-chat 会话面专属的资产对象存储与 token 计数适配随会话面退役删除（§8）。

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/huanminabc/juhe-ai/backend-go-platform/safego"
)

// chatGatewayExecutor implements GenerationExecutor by serving the
// request through the in-process /v1 chain handler (dispatchChatGatewayRequest
// equivalence: same path/header/body contract, streaming response body, the
// origin hop replaced by a direct handler call).
type chatGatewayExecutor struct {
	// chain is the assembled /v1 gateway chain handler.
	chain http.Handler
}

func newChatGatewayExecutor(chain http.Handler) *chatGatewayExecutor {
	return &chatGatewayExecutor{chain: chain}
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
	httpReq, err := http.NewRequestWithContext(ctx, method, "http://chat-gateway.internal"+path, bytes.NewReader(req.Body))
	if err != nil {
		_ = pipe.Close()
		return nil, fmt.Errorf("构建 chat 网关请求失败: %w", err)
	}
	for key, value := range req.Headers {
		httpReq.Header.Set(key, value)
	}
	// RemoteAddr 透传（真实 Key 链路的 IP 黑名单/单 IP 并发/审计按同一客户端
	// 地址收敛）：控制台请求的 RemoteAddr 由派发请求携带，进程内直调不落
	// loopback 地址。
	httpReq.RemoteAddr = req.RemoteAddr
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
