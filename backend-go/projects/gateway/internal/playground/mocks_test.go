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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
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

// fakeImageJobStore 是图像任务存储的内存替身（接口全实现：状态机、卡片归一、
// 过滤与启动恢复走真实 imageJobCardOf/过滤逻辑的存储语义近似——行数据在内存，
// 断言聚焦 handler/执行器行为）。terminal 通道在 Complete/Fail 时投递任务 ID
// 供执行器测试同步等待。
type fakeImageJobStore struct {
	mu       sync.Mutex
	records  map[string]*ImageJobRecord
	order    []string
	terminal chan string
	listErr  error
}

func newFakeImageJobStore() *fakeImageJobStore {
	return &fakeImageJobStore{
		records:  map[string]*ImageJobRecord{},
		terminal: make(chan string, 16),
	}
}

func (s *fakeImageJobStore) Create(job *ImageJobRecord) (ImageJobRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := *job
	record.ID = "job_" + strconv.Itoa(len(s.records)+1) + "-fake"
	record.Status = imageJobStatusQueued
	now := time.Now().UnixMilli()
	record.CreatedAt = now
	record.UpdatedAt = now
	for _, id := range record.ReferenceIDs {
		if id == "ref_missing" {
			return ImageJobRecord{}, errImageJobReferenceNotFound
		}
	}
	s.records[record.ID] = &record
	s.order = append(s.order, record.ID)
	return record, nil
}

func (s *fakeImageJobStore) Claim(viewerID, jobID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[jobID]
	if !ok || record.ViewerID != viewerID || record.Status != imageJobStatusQueued {
		return false, nil
	}
	record.Status = imageJobStatusRunning
	record.UpdatedAt = time.Now().UnixMilli()
	return true, nil
}

func (s *fakeImageJobStore) Complete(viewerID, jobID string, resultIDs []string, usage ImageJobUsage, costUSD *float64, size string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[jobID]
	if !ok || record.ViewerID != viewerID {
		return errImageJobNotFound
	}
	now := time.Now().UnixMilli()
	record.Status = imageJobStatusCompleted
	record.ResultIDs = resultIDs
	record.InputTokens, record.OutputTokens, record.TotalTokens = usage.InputTokens, usage.OutputTokens, usage.TotalTokens
	record.CostUSD = costUSD
	record.CompletedAt = now
	record.ExpiresAt = now + int64(imageJobRetention/time.Millisecond)
	record.UpdatedAt = now
	if size != "" {
		record.Size = size
	}
	s.terminal <- jobID
	return nil
}

func (s *fakeImageJobStore) Fail(viewerID, jobID, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[jobID]
	if !ok || record.ViewerID != viewerID {
		return errImageJobNotFound
	}
	record.Status = imageJobStatusFailed
	record.ErrorMessage = message
	record.UpdatedAt = time.Now().UnixMilli()
	s.terminal <- jobID
	return nil
}

func (s *fakeImageJobStore) Get(viewerID, jobID string) (ImageJobCard, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[jobID]
	if !ok || record.ViewerID != viewerID {
		return ImageJobCard{}, errImageJobNotFound
	}
	return imageJobCardOf(*record, time.Now()), nil
}

func (s *fakeImageJobStore) List(viewerID string, filter ImageJobFilter) (ImageJobPage, error) {
	if s.listErr != nil {
		return ImageJobPage{}, s.listErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	limit := filter.Limit
	if limit <= 0 {
		limit = 24
	}
	if limit > 100 {
		limit = 100
	}
	matched := make([]*ImageJobRecord, 0, len(s.records))
	nowMilli := time.Now().UnixMilli()
	for _, id := range s.order {
		record := s.records[id]
		if record.ViewerID != viewerID {
			continue
		}
		// status 派生态与 pgImageJobStore.List 同口径：active = queued|running；
		// expired = completed 且（images_expired 或 expires_at 已过）；completed
		// 收敛为未删完成。
		deleted := record.ImagesExpired || (record.ExpiresAt > 0 && record.ExpiresAt < nowMilli)
		switch filter.Status {
		case imageJobStatusActive:
			if record.Status != imageJobStatusQueued && record.Status != imageJobStatusRunning {
				continue
			}
		case imageJobStatusExpired:
			if record.Status != imageJobStatusCompleted || !deleted {
				continue
			}
		case imageJobStatusCompleted:
			if record.Status != imageJobStatusCompleted || deleted {
				continue
			}
		case imageJobStatusQueued, imageJobStatusRunning, imageJobStatusFailed:
			if record.Status != filter.Status {
				continue
			}
		case "":
		}
		matched = append(matched, record)
	}
	// created_at DESC, id DESC（与 pgImageJobStore.List 同序）。
	for i := 1; i < len(matched); i++ {
		for j := i; j > 0; j-- {
			a, b := matched[j-1], matched[j]
			if a.CreatedAt < b.CreatedAt || (a.CreatedAt == b.CreatedAt && a.ID < b.ID) {
				matched[j-1], matched[j] = b, a
			} else {
				break
			}
		}
	}
	page := ImageJobPage{Jobs: []ImageJobCard{}}
	now := time.Now()
	for _, record := range matched {
		if len(page.Jobs) >= limit {
			break
		}
		page.Jobs = append(page.Jobs, imageJobCardOf(*record, now))
	}
	if len(matched) > len(page.Jobs) && len(page.Jobs) > 0 {
		page.NextCursor = page.Jobs[len(page.Jobs)-1].CreatedAt
	}
	return page, nil
}

func (s *fakeImageJobStore) RecoverAtBoot() ([]ImageJobRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	queued := make([]ImageJobRecord, 0, len(s.records))
	for _, id := range s.order {
		record := s.records[id]
		switch record.Status {
		case imageJobStatusQueued:
			queued = append(queued, *record)
		case imageJobStatusRunning:
			record.Status = imageJobStatusFailed
			record.ErrorMessage = "网关重启，任务中断"
		}
	}
	return queued, nil
}

// snapshot 返回任务记录副本（执行器终态断言用）。
func (s *fakeImageJobStore) snapshot(jobID string) (ImageJobRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[jobID]
	if !ok {
		return ImageJobRecord{}, false
	}
	return *record, true
}

// waitTerminal 等待任务到达终态（Complete/Fail 投递），超时 fatal。
func (s *fakeImageJobStore) waitTerminal(t *testing.T, timeout time.Duration) string {
	t.Helper()
	select {
	case jobID := <-s.terminal:
		return jobID
	case <-time.After(timeout):
		t.Fatalf("任务 %v 内未到达终态", timeout)
		return ""
	}
}

// fakeImageCost 是计价端口替身（按 (providerCode, model) 查表返回固定价或
// false，供 costUsd 注入/无价断言）。
type fakeImageCost struct {
	price float64
	ok    bool
	calls []string
}

func (f *fakeImageCost) ImageCost(providerCode, model, size string, imageCount int, inputTokens, outputTokens int64) (float64, bool) {
	f.calls = append(f.calls, providerCode+"\x00"+model+"\x00"+size)
	if !f.ok {
		return 0, false
	}
	return f.price, true
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
