package versiontracking

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamidentity"
)

func TestRefreshRejectsRollbackAndKeepsPreviousAutoValue(t *testing.T) {
	var written map[string]string
	var applied map[string]string
	deps := testDeps(t, map[string]string{
		FamilyCodex:      "0.200.0",
		FamilyClaudeCode: "2.1.300",
		FamilyGeminiCLI:  "0.70.0",
		FamilyZCode:      "3.20.0",
	}, func(next map[string]string) {
		written = cloneVersions(next)
	}, func(next map[string]string) {
		applied = cloneVersions(next)
	})
	deps.HTTP = scriptedDoer{
		FamilyCodex:      releaseResponse(http.StatusOK, githubReleaseJSON("0.199.0", "https://github.com/openai/codex/releases/tag/rust-v0.199.0")),
		FamilyClaudeCode: releaseResponse(http.StatusOK, npmJSON("@anthropic-ai/claude-code", "2.1.301")),
		FamilyGeminiCLI:  releaseResponse(http.StatusOK, npmJSON("@google/gemini-cli", "0.70.1")),
		FamilyZCode:      releaseResponse(http.StatusOK, zcodeJSON("v3.20.1", "https://github.com/zai-org/ZCode/releases/tag/v3.20.1")),
	}

	result, err := Refresh(context.Background(), deps)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !result.Changed {
		t.Fatal("其他族上升必须写库")
	}
	if written[FamilyCodex] != "0.200.0" {
		t.Fatalf("codex 回滚未被保留: %+v", written)
	}
	if written[FamilyClaudeCode] != "2.1.301" || written[FamilyGeminiCLI] != "0.70.1" || written[FamilyZCode] != "3.20.1" {
		t.Fatalf("其他族未更新: %+v", written)
	}
	if applied[FamilyCodex] != "0.200.0" || applied[FamilyClaudeCode] != "2.1.301" {
		t.Fatalf("进程内注入与写入不一致: %+v", applied)
	}
	assertAction(t, result, FamilyCodex, actionRejectedRollback)
	assertAction(t, result, FamilyClaudeCode, actionUpdated)
}

func TestRefreshRejectsValueBelowBuiltIn(t *testing.T) {
	var writeCalls int
	deps := testDeps(t, map[string]string{}, func(map[string]string) {
		writeCalls++
	}, func(map[string]string) {})
	below := decrementPatch(upstreamidentity.BuiltInClientVersion(FamilyCodex))
	deps.HTTP = scriptedDoer{
		FamilyCodex:      releaseResponse(http.StatusOK, githubReleaseJSON(below, "https://github.com/openai/codex/releases/tag/rust-v"+below)),
		FamilyClaudeCode: releaseResponse(http.StatusOK, npmJSON("@anthropic-ai/claude-code", "9.9.9")),
		FamilyGeminiCLI:  releaseResponse(http.StatusOK, npmJSON("@google/gemini-cli", "9.9.9")),
		FamilyZCode:      releaseResponse(http.StatusOK, zcodeJSON("v9.9.9", "https://github.com/zai-org/ZCode/releases/tag/v9.9.9")),
	}

	result, err := Refresh(context.Background(), deps)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if writeCalls != 1 {
		t.Fatalf("writeCalls = %d, want 1", writeCalls)
	}
	if result.Next[FamilyCodex] != "" {
		t.Fatalf("低于内置的族不得写入自动层: %+v", result.Next)
	}
	assertAction(t, result, FamilyCodex, actionRejectedBuiltIn)
	if result.Next[FamilyClaudeCode] != "9.9.9" {
		t.Fatalf("其他族应更新: %+v", result.Next)
	}
}

func TestRefreshIsolatesSingleSourceFailure(t *testing.T) {
	deps := testDeps(t, map[string]string{FamilyClaudeCode: "2.1.300"}, func(map[string]string) {}, func(map[string]string) {})
	doer := scriptedDoer{
		FamilyCodex:      releaseResponse(http.StatusTooManyRequests, "rate limited"),
		FamilyClaudeCode: releaseResponse(http.StatusOK, npmJSON("@anthropic-ai/claude-code", "2.1.301")),
		FamilyGeminiCLI:  releaseResponse(http.StatusBadGateway, "bad gateway"),
		FamilyZCode:      releaseResponse(http.StatusOK, zcodeJSON("v9.9.9", "https://github.com/zai-org/ZCode/releases/tag/v9.9.9")),
	}
	doer[FamilyCodex].header.Set("Retry-After", "30")
	deps.HTTP = doer

	result, err := Refresh(context.Background(), deps)
	if err != nil {
		t.Fatalf("部分成功不得返回 error: %v", err)
	}
	assertAction(t, result, FamilyCodex, actionSkippedSourceFail)
	assertAction(t, result, FamilyGeminiCLI, actionSkippedSourceFail)
	assertAction(t, result, FamilyClaudeCode, actionUpdated)
	if result.Next[FamilyClaudeCode] != "2.1.301" || result.Next[FamilyZCode] != "9.9.9" {
		t.Fatalf("成功族未更新: %+v", result.Next)
	}
	codex := outcomeByFamily(result, FamilyCodex)
	if codex.RetryAfter != "30" || !strings.Contains(codex.Error, "429") {
		t.Fatalf("429 未记录 Retry-After: %+v", codex)
	}
}

func TestRefreshAllSourcesFailedReturnsError(t *testing.T) {
	var writeCalls int
	deps := testDeps(t, map[string]string{FamilyCodex: "0.200.0"}, func(map[string]string) {
		writeCalls++
	}, func(map[string]string) {})
	deps.HTTP = scriptedDoer{
		FamilyCodex:      releaseResponse(http.StatusInternalServerError, "down"),
		FamilyClaudeCode: releaseResponse(http.StatusNotFound, "missing"),
		FamilyGeminiCLI:  {err: io.ErrUnexpectedEOF},
		FamilyZCode:      releaseResponse(http.StatusOK, `{"tag_name":"not-a-version"}`),
	}

	_, err := Refresh(context.Background(), deps)
	if err == nil || !strings.Contains(err.Error(), "全部客户端版本发布源失败") {
		t.Fatalf("err = %v", err)
	}
	if writeCalls != 0 {
		t.Fatalf("全失败不得写库, writeCalls=%d", writeCalls)
	}
}

func TestRefreshSkipsUpsertWhenNothingChanged(t *testing.T) {
	var writeCalls int
	current := map[string]string{
		FamilyCodex:      "0.200.0",
		FamilyClaudeCode: "2.1.300",
		FamilyGeminiCLI:  "0.70.0",
		FamilyZCode:      "3.20.0",
		FamilyGrokCLI:    "1.0.45",
	}
	deps := testDeps(t, current, func(map[string]string) {
		writeCalls++
	}, func(map[string]string) {
		t.Fatal("无变化不得注入进程内覆盖")
	})
	deps.HTTP = scriptedDoer{
		FamilyCodex:      releaseResponse(http.StatusOK, githubReleaseJSON("0.200.0", "https://github.com/openai/codex/releases/tag/rust-v0.200.0")),
		FamilyClaudeCode: releaseResponse(http.StatusOK, npmJSON("@anthropic-ai/claude-code", "2.1.300")),
		FamilyGeminiCLI:  releaseResponse(http.StatusOK, npmJSON("@google/gemini-cli", "0.70.0")),
		FamilyZCode:      releaseResponse(http.StatusOK, zcodeJSON("v3.20.0", "https://github.com/zai-org/ZCode/releases/tag/v3.20.0")),
		FamilyGrokCLI:    releaseResponse(http.StatusOK, "[package]\nname = \"xai-grok-shell\"\nversion = \"1.0.45\"\n"),
	}

	result, err := Refresh(context.Background(), deps)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if result.Changed || writeCalls != 0 {
		t.Fatalf("changed=%v writeCalls=%d", result.Changed, writeCalls)
	}
	for _, outcome := range result.Outcomes {
		if outcome.Action != actionUnchanged {
			t.Fatalf("outcome = %+v", outcome)
		}
	}
}

func TestRefreshReadFailureReturnsErrorWithoutFetch(t *testing.T) {
	deps := testDeps(t, nil, func(map[string]string) {
		t.Fatal("读取失败不得写库")
	}, func(map[string]string) {})
	deps.Read = func(context.Context) (map[string]string, error) {
		return nil, io.ErrClosedPipe
	}
	deps.HTTP = scriptedDoer{}

	_, err := Refresh(context.Background(), deps)
	if err == nil || !strings.Contains(err.Error(), "读取自动版本键失败") {
		t.Fatalf("err = %v", err)
	}
}

type scriptedResponse struct {
	status int
	body   string
	header http.Header
	err    error
}

func releaseResponse(status int, body string) *scriptedResponse {
	return &scriptedResponse{status: status, body: body, header: make(http.Header)}
}

type scriptedDoer map[string]*scriptedResponse

func (d scriptedDoer) Do(req *http.Request) (*http.Response, error) {
	family := familyForURL(req.URL.String())
	item := d[family]
	if item == nil {
		return nil, io.ErrUnexpectedEOF
	}
	if item.err != nil {
		return nil, item.err
	}
	header := item.header
	if header == nil {
		header = make(http.Header)
	}
	return &http.Response{
		StatusCode: item.status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(item.body)),
		Request:    req,
	}, nil
}

func familyForURL(rawURL string) string {
	switch {
	case strings.Contains(rawURL, "openai/codex"):
		return FamilyCodex
	case strings.Contains(rawURL, "claude-code"):
		return FamilyClaudeCode
	case strings.Contains(rawURL, "gemini-cli"):
		return FamilyGeminiCLI
	case strings.Contains(rawURL, "zai-org/ZCode"):
		return FamilyZCode
	case strings.Contains(rawURL, "xai-org/grok-build"):
		return FamilyGrokCLI
	default:
		return ""
	}
}

func testDeps(t *testing.T, current map[string]string, write func(map[string]string), apply func(map[string]string)) Deps {
	t.Helper()
	snapshot := cloneVersions(current)
	var mu sync.Mutex
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	return Deps{
		Read: func(context.Context) (map[string]string, error) {
			mu.Lock()
			defer mu.Unlock()
			return cloneVersions(snapshot), nil
		},
		Write: func(_ context.Context, next map[string]string) error {
			write(cloneVersions(next))
			return nil
		},
		Apply: apply,
		BuiltIn: func(family string) string {
			return upstreamidentity.BuiltInClientVersion(family)
		},
		Now:    func() time.Time { return now },
		Logger: slog.New(slog.DiscardHandler),
	}
}

func githubReleaseJSON(version, htmlURL string) string {
	return `{"name":"` + version + `","tag_name":"rust-v` + version + `","prerelease":false,"draft":false,"html_url":"` + htmlURL + `"}`
}

func npmJSON(name, version string) string {
	return `{"name":"` + name + `","version":"` + version + `"}`
}

func zcodeJSON(tag, htmlURL string) string {
	return `{"tag_name":"` + tag + `","prerelease":false,"draft":false,"html_url":"` + htmlURL + `"}`
}

func assertAction(t *testing.T, result Result, family, action string) {
	t.Helper()
	outcome := outcomeByFamily(result, family)
	if outcome.Action != action {
		t.Fatalf("%s action = %s, want %s (%+v)", family, outcome.Action, action, outcome)
	}
}

func outcomeByFamily(result Result, family string) FamilyOutcome {
	for _, outcome := range result.Outcomes {
		if outcome.Family == family {
			return outcome
		}
	}
	return FamilyOutcome{}
}

func decrementPatch(version string) string {
	parts := strings.Split(version, ".")
	if len(parts) != 3 || parts[2] == "0" {
		return "0.0.0"
	}
	last := parts[2]
	n := 0
	for _, digit := range last {
		n = n*10 + int(digit-'0')
	}
	return parts[0] + "." + parts[1] + "." + itoa(n-1)
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
