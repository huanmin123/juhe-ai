package versiontracking

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamidentity"
)

const (
	// SourceTimeout 是单源 HTTP 超时（设计 §5：15s）。
	SourceTimeout = 15 * time.Second
	// MaxBodyBytes 是单源响应限读（设计 §5：1 MiB，复用 upstreamhttp.ReadBounded）。
	MaxBodyBytes = 1 << 20
	// UserAgent 是 GitHub 请求的标识 UA（设计 §5：带标识，不做认证）。
	UserAgent = "juhe-ai-version-tracker/1"

	eventRefreshCompleted = "upstream_client_version_refresh_completed"

	actionUpdated           = "updated"
	actionUnchanged         = "unchanged"
	actionRejectedRollback  = "rejected_below_auto"
	actionRejectedBuiltIn   = "rejected_below_builtin"
	actionSkippedSourceFail = "skipped_source_failed"
	actionSkippedCompare    = "skipped_compare_failed"
)

// HTTPDoer 是可注入的 HTTP 客户端（测试用 Mock；生产注入直连 http.Client）。
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Deps 是单轮刷新的外部依赖。读取/写入由组合根对接 jobssettings 与业务库 upsert。
type Deps struct {
	HTTP    HTTPDoer
	Read    func(ctx context.Context) (map[string]string, error)
	Write   func(ctx context.Context, next map[string]string) error
	Apply   func(next map[string]string)
	BuiltIn func(family string) string
	Now     func() time.Time
	Logger  *slog.Logger
}

// FamilyOutcome 是单族本轮结果（进入结构化日志）。
type FamilyOutcome struct {
	Family   string `json:"family"`
	Result   string `json:"result"`
	RawField string `json:"rawField,omitempty"`
	Parsed   string `json:"parsed,omitempty"`
	Action   string `json:"action"`
	Duration string `json:"duration"`
	Error    string `json:"error,omitempty"`
	// RetryAfter 只在 HTTP 429 时记录（设计 §5）。
	RetryAfter string `json:"retryAfter,omitempty"`
}

// Result 是一轮刷新的汇总。Changed 为 true 表示已 upsert 并注入进程内自动层。
type Result struct {
	Changed  bool
	Outcomes []FamilyOutcome
	Next     map[string]string
}

// Refresh 串行拉取五个发布源，按写者侧规则汇总后（有变化时）幂等 upsert 并注入进程内
// 自动层。全部源失败返回 error（交调度退避）；部分成功返回 nil。
func Refresh(ctx context.Context, deps Deps) (Result, error) {
	if err := validateDeps(&deps); err != nil {
		return Result{}, err
	}
	current, readErr := deps.Read(ctx)
	if readErr != nil {
		return Result{}, fmt.Errorf("读取自动版本键失败: %w", readErr)
	}
	if current == nil {
		current = map[string]string{}
	}
	next := cloneVersions(current)
	outcomes := make([]FamilyOutcome, 0, 4)
	sourceFailures := 0
	changed := false

	for _, source := range Sources() {
		outcome := refreshFamily(ctx, deps, source, current, next)
		outcomes = append(outcomes, outcome)
		if outcome.Result == "failed" {
			sourceFailures++
			continue
		}
		if outcome.Action == actionUpdated {
			changed = true
		}
	}

	result := Result{Outcomes: outcomes, Next: cloneVersions(next)}
	if sourceFailures == len(Sources()) {
		logRefresh(deps.Logger, false, outcomes)
		return result, fmt.Errorf("全部客户端版本发布源失败")
	}
	if changed {
		if err := deps.Write(ctx, next); err != nil {
			logRefresh(deps.Logger, false, outcomes)
			return result, fmt.Errorf("写入自动版本键失败: %w", err)
		}
		deps.Apply(cloneVersions(next))
		result.Changed = true
		result.Next = cloneVersions(next)
	}
	logRefresh(deps.Logger, true, outcomes)
	return result, nil
}

func refreshFamily(ctx context.Context, deps Deps, source Source, current, next map[string]string) FamilyOutcome {
	started := deps.Now()
	outcome := FamilyOutcome{Family: source.Family}
	body, status, retryAfter, err := fetchSource(ctx, deps, source)
	if err != nil {
		outcome.Result = "failed"
		outcome.Action = actionSkippedSourceFail
		outcome.Error = err.Error()
		outcome.RetryAfter = retryAfter
		if status != 0 {
			outcome.Error = fmt.Sprintf("HTTP %d: %s", status, err.Error())
		}
		outcome.Duration = deps.Now().Sub(started).String()
		return outcome
	}
	parsed, err := source.Parse(body)
	if err != nil {
		outcome.Result = "failed"
		outcome.Action = actionSkippedSourceFail
		outcome.Error = err.Error()
		outcome.Duration = deps.Now().Sub(started).String()
		return outcome
	}
	outcome.Result = "ok"
	outcome.RawField = parsed.RawField
	outcome.Parsed = parsed.Version
	outcome.Action = decideAction(source.Family, parsed.Version, current, deps.BuiltIn)
	if outcome.Action == actionUpdated {
		next[source.Family] = parsed.Version
	}
	outcome.Duration = deps.Now().Sub(started).String()
	return outcome
}

func decideAction(family, version string, current map[string]string, builtIn func(string) string) string {
	if existing := current[family]; existing != "" {
		cmp, err := CompareSemver(version, existing)
		if err != nil {
			return actionSkippedCompare
		}
		if cmp < 0 {
			return actionRejectedRollback
		}
		if cmp == 0 {
			return actionUnchanged
		}
	}
	baseline := builtIn(family)
	if baseline != "" {
		cmp, err := CompareSemver(version, baseline)
		if err != nil {
			return actionSkippedCompare
		}
		if cmp < 0 {
			return actionRejectedBuiltIn
		}
	}
	if current[family] == version {
		return actionUnchanged
	}
	return actionUpdated
}

func fetchSource(ctx context.Context, deps Deps, source Source) ([]byte, int, string, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, SourceTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, source.URL, nil)
	if err != nil {
		return nil, 0, "", err
	}
	request.Header.Set("Accept", "application/json")
	if source.GitHub {
		request.Header.Set("User-Agent", UserAgent)
	}
	response, err := deps.HTTP.Do(request)
	if err != nil {
		return nil, 0, "", err
	}
	defer response.Body.Close()
	retryAfter := ""
	if response.StatusCode == http.StatusTooManyRequests {
		retryAfter = strings.TrimSpace(response.Header.Get("Retry-After"))
	}
	body, readErr := upstreamhttp.ReadBounded(response.Body, MaxBodyBytes)
	if readErr != nil {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil, response.StatusCode, retryAfter, fmt.Errorf("读取发布源响应失败: %w", readErr)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, response.StatusCode, retryAfter, fmt.Errorf("发布源返回 HTTP %d", response.StatusCode)
	}
	return body, response.StatusCode, retryAfter, nil
}

func validateDeps(deps *Deps) error {
	if deps == nil || deps.HTTP == nil {
		return fmt.Errorf("版本跟版缺少 HTTP 客户端")
	}
	if deps.Read == nil || deps.Write == nil || deps.Apply == nil {
		return fmt.Errorf("版本跟版缺少读取、写入或注入依赖")
	}
	if deps.BuiltIn == nil {
		deps.BuiltIn = upstreamidentity.BuiltInClientVersion
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	return nil
}

func cloneVersions(source map[string]string) map[string]string {
	cloned := make(map[string]string, len(source))
	for family, version := range source {
		cloned[family] = version
	}
	return cloned
}

func logRefresh(logger *slog.Logger, ok bool, outcomes []FamilyOutcome) {
	if logger == nil {
		return
	}
	args := []any{"event", eventRefreshCompleted, "ok", ok, "sources", len(outcomes)}
	for index, outcome := range outcomes {
		prefix := "source_" + strconv.Itoa(index)
		args = append(args,
			prefix+"_family", outcome.Family,
			prefix+"_result", outcome.Result,
			prefix+"_raw", outcome.RawField,
			prefix+"_parsed", outcome.Parsed,
			prefix+"_action", outcome.Action,
			prefix+"_duration", outcome.Duration,
		)
		if outcome.Error != "" {
			args = append(args, prefix+"_error", outcome.Error)
		}
		if outcome.RetryAfter != "" {
			args = append(args, prefix+"_retry_after", outcome.RetryAfter)
		}
	}
	logger.Info("客户端版本自动跟版刷新完成", args...)
}
