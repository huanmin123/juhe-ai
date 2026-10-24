package main

// openaicompat 404 JSON 契约全部进程内驱动，不依赖真实上游。

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
)

func TestW1ChatGatewayExecutorDispatch(t *testing.T) {
	// 未接线错误。
	executor := newChatGatewayExecutor(nil)
	if _, err := executor.Dispatch(context.Background(), chat.GenerationDispatchRequest{}); err == nil {
		t.Fatal("nil chain 必须报未接线错误")
	}
	// 正常流：path 归一化 + header 透传 + body 回读。
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("authorization") != "Bearer k" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	chainExecutor := newChatGatewayExecutor(inner)
	response, err := chainExecutor.Dispatch(context.Background(), chat.GenerationDispatchRequest{
		Path:    "v1/chat/completions",
		Method:  "POST",
		Headers: map[string]string{"authorization": "Bearer k"},
		Body:    []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if response.Status != http.StatusTeapot {
		t.Fatalf("status = %d", response.Status)
	}
	if response.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("content-type = %q", response.Header.Get("Content-Type"))
	}
	body := make([]byte, 64)
	n, _ := response.Body.Read(body)
	if !strings.Contains(string(body[:n]), "ok") {
		t.Fatalf("body = %q", string(body[:n]))
	}
	_ = response.Body.Close()
	// 空 path 默认 /v1/chat/completions。
	defaultPathExecutor := newChatGatewayExecutor(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	if _, err := defaultPathExecutor.Dispatch(context.Background(), chat.GenerationDispatchRequest{}); err != nil {
		t.Fatalf("默认 path dispatch: %v", err)
	}
	// ctx 取消：Dispatch 返回 ctx 错误。
	blocking := newChatGatewayExecutor(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := blocking.Dispatch(ctx, chat.GenerationDispatchRequest{Path: "/v1/chat/completions"}); err == nil {
		t.Fatal("已取消 ctx 必须返回错误")
	}
}

func TestW1ChatPipeWriterHeaderIdempotent(t *testing.T) {
	reader, writer := io.Pipe()
	target := newChatPipeWriter(writer)
	target.WriteHeader(http.StatusTeapot)
	target.WriteHeader(http.StatusOK) // 二次提交被忽略
	if target.status != http.StatusTeapot {
		t.Fatalf("status = %d，want 418", target.status)
	}
	select {
	case <-target.headerCh:
	default:
		t.Fatal("headerCh 未关闭")
	}
	// io.Pipe 同步语义：写入在读取方消费前阻塞，放后台 goroutine 完成写入与关闭。
	go func() {
		_, _ = target.Write([]byte("data"))
		target.finish()
	}()
	content, err := io.ReadAll(reader)
	if err != nil || string(content) != "data" {
		t.Fatalf("pipe 数据 = %q, %v", string(content), err)
	}
}

func TestW1ChatTokenCount(t *testing.T) {
	count, err := newChatTokenCount()
	if err != nil {
		t.Fatalf("newChatTokenCount: %v", err)
	}
	if count("你好") <= 0 {
		t.Fatalf("token 计数 = %d，必须为正", count("你好"))
	}
}

func TestW1ErrChainCompatAndBearer(t *testing.T) {
	err := errChainCompat("组合缺少句柄")
	if err.Error() != "组合缺少句柄" {
		t.Fatalf("Error() = %q", err.Error())
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/files", nil)
	if got := bearerTokenOf(request); got != "" {
		t.Fatalf("无 header = %q", got)
	}
	request.Header.Set("authorization", "Bearer  sk-key ")
	if got := bearerTokenOf(request); got != "sk-key" {
		t.Fatalf("bearer = %q", got)
	}
	request.Header.Set("authorization", "Basic abc")
	if got := bearerTokenOf(request); got != "" {
		t.Fatalf("非 bearer = %q", got)
	}
	request.Header.Set("authorization", "BearerOnly")
	if got := bearerTokenOf(request); got != "" {
		t.Fatalf("无空格 bearer = %q", got)
	}
}

func TestW1ChainCompatDispatcher404Contract(t *testing.T) {
	mux := &chainCompatMux{mux: http.NewServeMux()}
	mux.Register("GET /v1/files", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"files":[]}`))
	}))
	dispatcher := &chainCompatDispatcher{mux: mux}
	if !dispatcher.matches("/v1/files") || !dispatcher.matches("/v1/files/x") || dispatcher.matches("/v1/filesystem") || dispatcher.matches("/v1/other") {
		t.Fatal("matches 前缀判断错误")
	}
	recorder := httptest.NewRecorder()
	dispatcher.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/files", nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != `{"files":[]}` {
		t.Fatalf("命中路由 = %d %q", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	dispatcher.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/v1/files", nil))
	if recorder.Code != http.StatusNotFound || recorder.Body.String() != `{"message":"资源不存在"}` {
		t.Fatalf("未匹配 = %d %q", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	dispatcher.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/other", nil))
	if recorder.Code != http.StatusNotFound || recorder.Body.String() != `{"message":"资源不存在"}` {
		t.Fatalf("非族路径 = %d %q", recorder.Code, recorder.Body.String())
	}
	var nilDispatcher *chainCompatDispatcher
	recorder = httptest.NewRecorder()
	nilDispatcher.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/files", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("nil dispatcher = %d", recorder.Code)
	}
}

func TestW1MountChainOpenAICompatFamiliesFailFast(t *testing.T) {
	if err := mountChainOpenAICompatFamilies(nil, nil, runtimeConfig{}, nil); err == nil {
		t.Fatal("nil composition 必须报错")
	}
	if err := mountChainOpenAICompatFamilies(&composition{}, nil, runtimeConfig{}, nil); err == nil {
		t.Fatal("缺业务库句柄必须报错")
	}
}
func TestW1ChatAssetObjectStoreLifecycle(t *testing.T) {
	if _, err := newChatAssetObjectStore("  "); err == nil {
		t.Fatal("空 root 必须报错")
	}
	root := filepath.Join(t.TempDir(), "assets")
	store, err := newChatAssetObjectStore(root)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	// 行为存疑无：穿越键经 filepath.Clean("/"+key) 从根消解，映射到根内而不越界。
	escaped, err := store.path("../escape")
	if err != nil {
		t.Fatalf("穿越键归一化报错: %v", err)
	}
	if !strings.HasPrefix(escaped, root) || strings.Contains(escaped, "..") {
		t.Fatalf("穿越键映射越界: %q", escaped)
	}
	data := []byte("图片字节")
	if err := store.Write("conv/a.png", data, 100, strings.ToUpper(chatAssetSHA256Hex(data))); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := store.Write("conv/a.png", data, 2, ""); err == nil {
		t.Fatal("超限写入必须报错")
	}
	if err := store.Write("conv/a.png", data, 100, "deadbeef"); err == nil {
		t.Fatal("sha 不匹配必须报错")
	}
	got, size, err := store.Open("conv/a.png", 100)
	if err != nil || string(got) != "图片字节" || size != int64(len(data)) {
		t.Fatalf("open = %q, %d, %v", got, size, err)
	}
	if _, _, err := store.Open("conv/a.png", 2); err == nil {
		t.Fatal("超限读取必须报错")
	}
	if _, _, err := store.Open("conv/missing.png", 100); err == nil {
		t.Fatal("缺失读取必须报错")
	}
	// 删除存在的键；缺失/空键静默通过（Node remove 语义）。
	if err := store.Delete([]string{"", "conv/missing.png", "conv/a.png"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, _, err := store.Open("conv/a.png", 100); err == nil {
		t.Fatal("删除后读取必须报错")
	}
}

func TestW1ChatAttachSubscriberTrySend(t *testing.T) {
	subscriber := &chatAttachSubscriber{events: make(chan chat.ChatGenerationEvent, 2)}
	for i := 0; i < 2; i++ {
		if !subscriber.TrySend(chat.ChatGenerationEvent{Type: "tick"}) {
			t.Fatalf("缓冲内第 %d 条必须成功", i+1)
		}
	}
	if subscriber.TrySend(chat.ChatGenerationEvent{Type: "overflow"}) {
		t.Fatal("溢出必须 drop")
	}
	if !subscriber.dropped {
		t.Fatal("溢出后必须标记 dropped")
	}
	drained := 0
	for range subscriber.events {
		drained++
	}
	if drained != 2 {
		t.Fatalf("缓冲事件 = %d，want 2", drained)
	}
	if subscriber.TrySend(chat.ChatGenerationEvent{Type: "after"}) {
		t.Fatal("dropped 后必须直接 false")
	}
}

func TestW1OpenChatDatabase(t *testing.T) {
	// SQLite 缺路径：fail fast。
	if _, _, err := openChatDatabase(runtimeConfig{}, nil, nil, false); err == nil {
		t.Fatal("缺 chat 数据库路径必须报错")
	}
	db, owns, err := openChatDatabase(runtimeConfig{ChatDatabasePath: filepath.Join(t.TempDir(), "chat.sqlite3")}, nil, nil, false)
	if err != nil || db == nil || !owns {
		t.Fatalf("open sqlite chat db = %v, %v", owns, err)
	}
	_ = db.Close()
}
