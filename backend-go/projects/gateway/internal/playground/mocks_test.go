package playground

// 演练场路由测试共用 Mock 基建：注册器用标准库 ServeMux 直接模拟 kernel 键格
// 式（"METHOD /path"），派发端口按回放队列产出固定响应并记录入站请求供断言，
// Key 模型候选端口为表驱动固定视图——全部断言走真实 handler，不经私有方法
// 捷径（Key 归属校验在 cmd 适配层，端口 mock 以表内无该 Key 表达同一语义）。

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
		return Scope{ViewerID: "sys_user_1"}, nil
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

// mockKeyModels 是 Key 模型候选端口 Mock：按 apiKeyID 返回固定候选行并记录
// 调用（表内无该 Key = Key 不存在/非本人语义，handler 收 404）。
type mockKeyModels struct {
	rows  map[string][]KeyModelRow
	calls [][2]string // [viewerID, apiKeyID]
	err   *HTTPError
}

func (m *mockKeyModels) ListKeyModels(_ context.Context, viewerID, apiKeyID string) ([]KeyModelRow, *HTTPError) {
	m.calls = append(m.calls, [2]string{viewerID, apiKeyID})
	if m.err != nil {
		return nil, m.err
	}
	rows, ok := m.rows[apiKeyID]
	if !ok {
		return nil, KeyNotFound()
	}
	return rows, nil
}

// keyRowsOf 把 (modelId, kind) 对折叠成候选行（providerCode 恒 gpt——测试只关
// 注 kind 校验，providerCode 投影在 cmd 适配层）。
func keyRowsOf(pairs ...any) []KeyModelRow {
	rows := make([]KeyModelRow, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		rows = append(rows, KeyModelRow{
			ModelID:      pairs[i].(string),
			Kind:         pairs[i+1].(ModelKind),
			ProviderCode: "gpt",
		})
	}
	return rows
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
	mu          sync.Mutex
	records     map[string]*ImageJobRecord
	order       []string
	terminal    chan string
	listErr     error
	deleteErr   error
	deleteCalls [][2]string // Delete 入参 [viewerID, jobID] 调用记录
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

// Delete implements ImageJobStore. deleteErr 非 nil 时优先返回（注入存储故障
// 面）；默认按 pgImageJobStore 真实语义执行：不存在/越权 errImageJobNotFound、
// 非终态 errImageJobNotDeletable、终态移除记录行（§3.4.6 v1.5 手动删除）。
func (s *fakeImageJobStore) Delete(viewerID, jobID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteCalls = append(s.deleteCalls, [2]string{viewerID, jobID})
	if s.deleteErr != nil {
		return s.deleteErr
	}
	record, ok := s.records[jobID]
	if !ok || record.ViewerID != viewerID {
		return errImageJobNotFound
	}
	if record.Status == imageJobStatusQueued || record.Status == imageJobStatusRunning {
		return errImageJobNotDeletable
	}
	delete(s.records, jobID)
	for index, id := range s.order {
		if id == jobID {
			s.order = append(s.order[:index], s.order[index+1:]...)
			break
		}
	}
	return nil
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

// playgroundKeyModelsFixtureID 是基础夹具的 Key ID（Key 归属校验在 cmd 适配
// 层，端口 mock 按 ID 表达候选面）。
const playgroundKeyModelsFixtureID = "key_1"

// playgroundFixtureDeps 装配「一个 Key + chat 目录行」的最小依赖（Artifacts 指
// 向每测试独立的文件系统替身——生产为 PG 实现，单测保留真实文件写读以覆盖
// 覆盖替换/懒清理）。候选扩展由用例直接改 mockKeyModels.rows。
func playgroundFixtureDeps(t *testing.T) *Deps {
	t.Helper()
	return &Deps{
		Dispatch: &mockDispatch{},
		KeyModels: &mockKeyModels{rows: map[string][]KeyModelRow{
			playgroundKeyModelsFixtureID: keyRowsOf("gpt-5.4", KindChat, "gpt-image-2", KindImage),
		}},
		Artifacts: newFSArtifactStore(t.TempDir()),
	}
}

// fixtureKeyModels 取基础夹具的候选端口（类型断言捷径，用例补行用）。
func fixtureKeyModels(deps *Deps) *mockKeyModels {
	return deps.KeyModels.(*mockKeyModels)
}
