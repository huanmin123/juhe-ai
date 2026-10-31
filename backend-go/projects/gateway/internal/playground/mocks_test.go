package playground

// 演练场路由测试共用 Mock 基建：注册器用标准库 ServeMux 直接模拟 kernel 键格
// 式（"METHOD /path"），派发端口按回放队列产出固定响应并记录入站请求供断言，
// 账户/目录端口为表驱动固定视图——全部断言走真实 handler，不经私有方法捷径。

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
)

// muxRegisterer 用 http.ServeMux 实现 Registerer（pattern 键与 kernel 同格式）。
type muxRegisterer struct{ mux *http.ServeMux }

func (m muxRegisterer) Register(pattern string, handler http.Handler) {
	m.mux.Handle(pattern, handler)
}

// newTestRoutes 装配测试路由：RequireSession 直通、固定 Scope、注入的 Mock
// 端口；返回 mux 供 httptest 请求。
func newTestRoutes(deps *Deps) (*http.ServeMux, *Deps) {
	if deps.RequireSession == nil {
		deps.RequireSession = func(h http.Handler) http.Handler { return h }
	}
	deps.ScopeResolver = func(*http.Request) (Scope, error) {
		return Scope{ViewerID: "sys_user_1", IsAdmin: false}, nil
	}
	mux := http.NewServeMux()
	deps.Register(muxRegisterer{mux}, "/__aisys__/api/playground")
	return mux, deps
}

// dispatchCall 记录一次派发的请求与产出。
type dispatchCall struct {
	Request  DispatchRequest
	Response *DispatchResponse
	Err      error
}

// mockDispatch 是派发端口 Mock：按脚本顺序回放固定响应（nil 脚本项 = 通用 200
// JSON 回显），记录全部入站请求；脚本耗尽后失败（避免测试静默漏配）。
type mockDispatch struct {
	script   []scriptedResponse
	calls    []dispatchCall
	failNext error
}

// scriptedResponse 是一次派发的回放产物（Body 预读为字节，SSE 流按需包装）。
type scriptedResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

func (m *mockDispatch) Dispatch(_ context.Context, req DispatchRequest) (*DispatchResponse, error) {
	call := dispatchCall{Request: req}
	defer func() { m.calls = append(m.calls, call) }()
	if m.failNext != nil {
		call.Err = m.failNext
		return nil, m.failNext
	}
	if len(m.script) == 0 {
		call.Err = errors.New("mockDispatch 脚本耗尽")
		return nil, call.Err
	}
	item := m.script[0]
	m.script = m.script[1:]
	status := item.Status
	if status == 0 {
		status = http.StatusOK
	}
	response := &DispatchResponse{
		Status: status,
		Header: item.Header,
		Body:   io.NopCloser(bytes.NewReader(item.Body)),
	}
	call.Response = response
	return response, nil
}

// sseBody 把 SSE 文本（\n 分隔）规范为字节载荷。
func sseBody(lines ...string) []byte {
	return []byte(strings.Join(lines, "\n") + "\n\n")
}

// staticAccounts 是账户端口 Mock（固定表 + 可注入错误）。
type staticAccounts struct {
	options []AccountOption
	err     error
}

func (s *staticAccounts) ListAccountOptions(context.Context, Scope) ([]AccountOption, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.options, nil
}

// staticLookup 是账户解析 Mock：表内命中返回启用账户，未命中 (nil, nil)（与
// accounts.Store FindChatAccount 的范围外同型语义一致）。
type staticLookup struct {
	refs map[string]*AccountRef
}

func (s *staticLookup) FindAccount(_ context.Context, _ Scope, accountID string) (*AccountRef, error) {
	ref, ok := s.refs[accountID]
	if !ok {
		return nil, nil
	}
	return ref, nil
}

// staticTransport 是 chat-pinned 运行时视图 Mock（按账户 ID 固定返回）。
type staticTransport struct {
	views map[string][]TransportAccount
}

func (s *staticTransport) ListPinnedAccountsForGroup(groupID, systemAccountID, accountID string) []TransportAccount {
	return s.views[accountID]
}

// staticCatalog 是目录端口 Mock（provider 码/账户 ID → 固定行）。
type staticCatalog struct {
	byProvider map[string][]CatalogItem
	byAccount  map[string][]CatalogItem
}

func (s *staticCatalog) ListProviderCatalog(providerCode, _ string) []CatalogItem {
	return s.byProvider[providerCode]
}

func (s *staticCatalog) ListAccountSupportedModels(accountID, _ string) []CatalogItem {
	return s.byAccount[accountID]
}

// fixedFetch 是 fetch_url Mock（固定正文或错误，按序回放）。
type fixedFetch struct {
	results []FetchResult
	errs    []error
	index   int
}

func (f *fixedFetch) Fetch(context.Context, string) (FetchResult, error) {
	if f.index < len(f.errs) && f.errs[f.index] != nil {
		result := FetchResult{}
		f.index++
		return result, f.errs[f.index-1]
	}
	if f.index >= len(f.results) {
		return FetchResult{Text: "(mock 无更多回放)"}, nil
	}
	result := f.results[f.index]
	f.index++
	return result, nil
}

// doJSON 发送 JSON 请求并返回 recorder。
func doJSON(mux *http.ServeMux, method, target string, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	return recorder
}

// pngOnePixel 是最小合法 PNG 字节（魔数 + IHDR 头长度足够过魔数嗅探）。
var pngOnePixel = []byte{
	0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
}

// wavBytes 是最小合法 WAV 字节（RIFF/WAVE 头，足够过魔数嗅探）。
var wavBytes = append(append([]byte("RIFF"), []byte{0x24, 0x00, 0x00, 0x00}...), []byte("WAVEfmt ")...)

// base64Of 是测试内 base64 编码辅助（StdEncoding，与 decodeBase64Image 对齐）。
func base64Of(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}
