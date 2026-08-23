package proxylatency

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const proxyLatencyManualJobName = "proxy-latency"

// ManualRequest is the loopback-only v1 bridge payload. It is deliberately
// separate from Outcome: target URLs and display names are needed to recreate
// Node's management response, but are never persisted in the jobs outcome.
type ManualRequest struct {
	SchemaVersion  int                 `json:"schema_version"`
	ProxyID        string              `json:"proxy_id"`
	ProxyName      string              `json:"proxy_name"`
	ConfigRevision string              `json:"config_revision"`
	ProxyType      string              `json:"proxy_type"`
	ProxyHost      string              `json:"proxy_host"`
	ProxyPort      int                 `json:"proxy_port"`
	ProxyUsername  string              `json:"proxy_username,omitempty"`
	ProxyPassword  *CredentialEnvelope `json:"proxy_password,omitempty"`
	Targets        []ManualTarget      `json:"targets"`
	DeadlineMS     int                 `json:"deadline_ms,omitempty"`
}

type ManualTarget struct {
	Provider  string `json:"provider"`
	ProfileID string `json:"profile_id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
}

type ProxyTestReport struct {
	ProxyID        string          `json:"proxyId"`
	ProxyName      string          `json:"proxyName"`
	Score          int             `json:"score"`
	Grade          string          `json:"grade"`
	Status         OverallStatus   `json:"status"`
	PassedCount    int             `json:"passedCount"`
	WarningCount   int             `json:"warningCount"`
	FailedCount    int             `json:"failedCount"`
	OutboundIP     string          `json:"outboundIp,omitempty"`
	OutboundRegion string          `json:"outboundRegion,omitempty"`
	BaseLatencyMS  *int64          `json:"baseLatencyMs,omitempty"`
	TestedAt       string          `json:"testedAt"`
	Items          []ProxyTestItem `json:"items"`
	Message        string          `json:"message"`
}

type ProxyTestItem struct {
	Name       string     `json:"name"`
	Status     ItemStatus `json:"status"`
	HTTPStatus *int       `json:"httpStatus,omitempty"`
	LatencyMS  *int64     `json:"latencyMs,omitempty"`
	Message    string     `json:"message"`
	TargetURL  string     `json:"targetUrl,omitempty"`
}

func (request ManualRequest) Validate(maxDeadline time.Duration) error {
	if request.SchemaVersion != 1 || strings.TrimSpace(request.ProxyID) == "" || strings.TrimSpace(request.ProxyName) == "" {
		return errors.New("J3a manual input schema_version/proxy_id/proxy_name 无效")
	}
	if !validProxyLatencyType(request.ProxyType) || strings.TrimSpace(request.ProxyHost) == "" || request.ProxyPort < 1 || request.ProxyPort > 65535 {
		return errors.New("J3a manual proxy 配置无效")
	}
	if request.ProxyPassword != nil && strings.TrimSpace(request.ProxyUsername) == "" {
		return errors.New("J3a manual proxy password 缺少 username")
	}
	if len(request.Targets) > 128 {
		return errors.New("J3a manual targets 不得超过 128")
	}
	if maxDeadline <= 0 || maxDeadline > 25*time.Second {
		maxDeadline = 25 * time.Second
	}
	if request.DeadlineMS != 0 && (request.DeadlineMS < 1_000 || time.Duration(request.DeadlineMS)*time.Millisecond > maxDeadline) {
		return errors.New("J3a manual deadline_ms 超出范围")
	}
	seen := make(map[string]struct{}, len(request.Targets))
	for _, target := range request.Targets {
		if strings.TrimSpace(target.Provider) == "" || strings.TrimSpace(target.ProfileID) == "" || strings.TrimSpace(target.Name) == "" || strings.TrimSpace(target.URL) == "" {
			return errors.New("J3a manual target 字段无效")
		}
		if _, exists := seen[target.Provider]; exists {
			return errors.New("J3a manual provider target 重复")
		}
		seen[target.Provider] = struct{}{}
	}
	if _, err := canonicalConfigRevision(request.ConfigRevision); err != nil {
		return fmt.Errorf("J3a manual config_revision 无效: %w", err)
	}
	return nil
}

func (request ManualRequest) InputDraft(now time.Time, deadline time.Duration) InputDraft {
	targets := make([]Target, 0, len(request.Targets))
	for _, target := range request.Targets {
		targets = append(targets, Target{Provider: target.Provider, ProfileID: target.ProfileID, URL: target.URL})
	}
	return InputDraft{
		ProxyID: request.ProxyID, ConfigRevision: request.ConfigRevision, Trigger: TriggerManual,
		IssuedAt: now.UTC(), ExpiresAt: now.UTC().Add(deadline), PolicyVersion: proxyLatencyInputPolicyVersion,
		ProxyType: request.ProxyType, ProxyHost: request.ProxyHost, ProxyPort: request.ProxyPort,
		ProxyUsername: request.ProxyUsername, ProxyPassword: request.ProxyPassword, Targets: targets,
	}
}

// ValidateOutcome checks the boundary between the durable Go outcome and the
// richer Node management report.  The executor normally guarantees these
// identities, but the bridge must fail closed if a future outcome decoder or
// replay path ever supplies an undeclared provider/profile.
func (request ManualRequest) ValidateOutcome(outcome Outcome) error {
	if outcome.ProxyID != request.ProxyID || outcome.Trigger != TriggerManual {
		return errors.New("J3a manual outcome identity 不匹配")
	}
	if len(outcome.Items) == 0 {
		return errors.New("J3a manual outcome items 为空")
	}
	if len(outcome.Items) != len(request.Targets) {
		return fmt.Errorf("J3a manual outcome items 未覆盖全部 provider: got=%d want=%d", len(outcome.Items), len(request.Targets))
	}
	declared := make(map[string]ManualTarget, len(request.Targets))
	for _, target := range request.Targets {
		declared[target.Provider] = target
	}
	seen := make(map[string]struct{}, len(outcome.Items))
	for _, item := range outcome.Items {
		target, ok := declared[item.Provider]
		if !ok || target.ProfileID != item.ProfileID {
			return fmt.Errorf("J3a manual outcome provider/profile 未声明: %s/%s", item.Provider, item.ProfileID)
		}
		if _, duplicate := seen[item.Provider]; duplicate {
			return fmt.Errorf("J3a manual outcome provider 重复: %s", item.Provider)
		}
		seen[item.Provider] = struct{}{}
	}
	if SummarizeItems(outcome.Items) != outcome.OverallStatus {
		return errors.New("J3a manual outcome overall status 不匹配")
	}
	return nil
}

func (request ManualRequest) Report(outcome Outcome) ProxyTestReport {
	nameByProvider := make(map[string]ManualTarget, len(request.Targets))
	for _, target := range request.Targets {
		nameByProvider[target.Provider] = target
	}
	items := make([]ProxyTestItem, 0, len(outcome.Items)+1)
	providerItems := make([]ProxyTestItem, 0, len(outcome.Items))
	for _, item := range outcome.Items {
		target := nameByProvider[item.Provider]
		converted := ProxyTestItem{Name: target.Name, Status: item.Status, Message: itemMessage(item), TargetURL: target.URL}
		if item.HTTPStatus != 0 {
			value := item.HTTPStatus
			converted.HTTPStatus = &value
		}
		if item.LatencyMS != 0 {
			value := item.LatencyMS
			converted.LatencyMS = &value
		}
		providerItems = append(providerItems, converted)
	}
	base := syntheticBase(providerItems, len(request.Targets))
	items = append(items, base)
	items = append(items, providerItems...)
	summary := summarizeReport(items)
	return ProxyTestReport{
		ProxyID: request.ProxyID, ProxyName: request.ProxyName, Score: summary.score, Grade: summary.grade,
		Status: summary.status, PassedCount: summary.passed, WarningCount: summary.warning, FailedCount: summary.failed,
		BaseLatencyMS: base.LatencyMS, TestedAt: outcome.ObservedAt.UTC().Format(time.RFC3339Nano), Items: items,
		Message: reportMessage(summary.status, summary.failed, summary.warning),
	}
}

func itemMessage(item ItemResult) string {
	if item.ErrorCode != "" {
		return item.ErrorCode
	}
	if item.HTTPStatus != 0 {
		return fmt.Sprintf("HTTP %d（传输完整，状态码仅供诊断）", item.HTTPStatus)
	}
	if item.Status == ItemFailed {
		return "代理传输失败"
	}
	if item.Status == ItemUnknown {
		return "未形成真实代理检测请求"
	}
	return "代理目标检测完成"
}

func syntheticBase(items []ProxyTestItem, providerCount int) ProxyTestItem {
	if providerCount <= 0 {
		return ProxyTestItem{Name: "基础连通性", Status: ItemUnknown, Message: "没有启用的供应商默认地址，未形成代理传输检测"}
	}
	failed, unknown, reachable := 0, 0, 0
	var total int64
	latencies := 0
	for _, item := range items {
		switch item.Status {
		case ItemFailed:
			failed++
		case ItemUnknown:
			unknown++
		case ItemPassed:
			reachable++
		}
		if item.LatencyMS != nil {
			total += *item.LatencyMS
			latencies++
		}
	}
	status := ItemUnknown
	if failed == 0 && unknown == 0 {
		status = ItemPassed
	} else if reachable > 0 {
		status = ItemWarning
	} else if failed > 0 {
		status = ItemFailed
	}
	base := ProxyTestItem{Name: "基础连通性", Status: status, Message: "供应商默认地址均未形成真实传输检测"}
	if latencies > 0 {
		average := total / int64(latencies)
		base.LatencyMS = &average
	}
	switch {
	case failed == 0 && unknown == 0:
		base.Message = "全部供应商默认地址可达"
	case reachable > 0:
		base.Message = fmt.Sprintf("部分供应商默认地址完成传输检测（%d/%d）", reachable, providerCount)
	case failed > 0:
		base.Message = "供应商默认地址全部发生传输失败"
	}
	return base
}

func summarizeReport(items []ProxyTestItem) struct {
	status                         OverallStatus
	score, passed, warning, failed int
	grade                          string
} {
	passed, warning, failed, unknown := 0, 0, 0, 0
	for _, item := range items {
		switch item.Status {
		case ItemPassed:
			passed++
		case ItemWarning:
			warning++
		case ItemFailed:
			failed++
		case ItemUnknown:
			unknown++
		}
	}
	status := OverallUnknown
	if failed > 0 {
		status = OverallFailed
	} else if warning > 0 || (passed > 0 && unknown > 0) {
		status = OverallWarning
	} else if unknown > 0 || len(items) == 0 {
		status = OverallUnknown
	} else {
		status = OverallPassed
	}
	score := 0
	if status != OverallUnknown {
		score = 100 - warning*10 - failed*35
		if score < 0 {
			score = 0
		}
	}
	grade := "D"
	if score >= 90 {
		grade = "A"
	} else if score >= 75 {
		grade = "B"
	} else if score >= 60 {
		grade = "C"
	}
	return struct {
		status                         OverallStatus
		score, passed, warning, failed int
		grade                          string
	}{status: status, score: score, grade: grade, passed: passed, warning: warning, failed: failed}
}

func reportMessage(status OverallStatus, failed, warning int) string {
	switch status {
	case OverallPassed:
		return "代理质量检测通过"
	case OverallWarning:
		return fmt.Sprintf("代理可用，存在 %d 项告警", warning)
	case OverallFailed:
		return fmt.Sprintf("代理检测存在 %d 项失败", failed)
	default:
		return "代理检测未形成有效传输尝试"
	}
}
