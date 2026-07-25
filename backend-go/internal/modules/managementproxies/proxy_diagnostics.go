package managementproxies

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/language"
	"golang.org/x/text/language/display"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	proxyProbeTimeout       = 15 * time.Second
	manualProxyTestDeadline = 25 * time.Second
)

type TestInput struct {
	ID string
}

type TestResult struct {
	Before Summary
	Proxy  Summary
	Report ProxyTestReport
}

type ProxyTestItem struct {
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	HTTPStatus *int    `json:"httpStatus,omitempty"`
	LatencyMs  *int    `json:"latencyMs,omitempty"`
	Message    string  `json:"message"`
	TargetURL  *string `json:"targetUrl,omitempty"`
}

type ProxyTestReport struct {
	ProxyID        string          `json:"proxyId"`
	ProxyName      string          `json:"proxyName"`
	Score          int             `json:"score"`
	Grade          string          `json:"grade"`
	Status         string          `json:"status"`
	PassedCount    int             `json:"passedCount"`
	WarningCount   int             `json:"warningCount"`
	FailedCount    int             `json:"failedCount"`
	OutboundIP     *string         `json:"outboundIp,omitempty"`
	OutboundRegion *string         `json:"outboundRegion,omitempty"`
	BaseLatencyMs  *int            `json:"baseLatencyMs,omitempty"`
	TestedAt       string          `json:"testedAt"`
	Items          []ProxyTestItem `json:"items"`
	Message        string          `json:"message"`
}

type ProxyProbeInput struct {
	TargetURL string
	ProxyURL  string
	Timeout   time.Duration
}

type ProxyProbeResult struct {
	StatusCode int
	Body       string
	LatencyMs  int
}

type ProxyProbe interface {
	Probe(ctx context.Context, input ProxyProbeInput) (ProxyProbeResult, error)
}

type proxyOutboundInfo struct {
	IP     *string
	Region *string
}

type outboundProbeTarget struct {
	URL    string
	Parser string
}

var outboundProbeTargets = []outboundProbeTarget{
	{URL: "http://ip-api.com/json/?lang=zh-CN", Parser: "ip-api"},
	{URL: "https://ipwho.is/", Parser: "ipwhois"},
	{URL: "https://api.ip.sb/geoip", Parser: "ipsb"},
	{URL: "https://ipinfo.io/json", Parser: "ipinfo"},
	{URL: "https://api.ipify.org?format=json", Parser: "ipify"},
	{URL: "http://httpbin.org/ip", Parser: "httpbin"},
}

func (s *Service) Test(ctx context.Context, input TestInput) (TestResult, error) {
	writer, err := s.proxyWriter()
	if err != nil {
		return TestResult{}, err
	}
	proxyID := strings.TrimSpace(input.ID)
	current, found, err := writer.FindManagementProxy(ctx, proxyID)
	if err != nil {
		return TestResult{}, err
	}
	if !found {
		return TestResult{}, ErrProxyNotFound
	}
	proxyURL, err := s.proxyURL(current)
	if err != nil {
		return TestResult{}, err
	}
	deadlineCtx, cancel := context.WithTimeout(ctx, manualProxyTestDeadline)
	defer cancel()
	testedAt := s.now().UTC()
	if s.providers == nil {
		return TestResult{}, fmt.Errorf("management provider reader is required")
	}
	providers, err := s.providers.ListManagementProviders(deadlineCtx, port.ManagementProviderListInput{
		SystemAccountID: current.SystemAccountID,
	})
	if err != nil {
		return TestResult{}, err
	}
	enabledProviders := make([]port.ManagementProviderOption, 0, len(providers))
	for _, provider := range providers {
		if provider.Enabled {
			enabledProviders = append(enabledProviders, provider)
		}
	}

	outboundResult := make(chan proxyOutboundInfo, 1)
	go func() {
		outboundResult <- s.probeOutboundInfo(deadlineCtx, proxyURL)
	}()

	providerItems := make([]ProxyTestItem, 0, len(enabledProviders))
	for _, provider := range enabledProviders {
		providerItems = append(providerItems, s.testProvider(deadlineCtx, proxyURL, provider))
	}
	outbound := <-outboundResult
	baseItem := baseConnectivityItem(providerItems, len(enabledProviders))
	items := make([]ProxyTestItem, 0, len(providerItems)+1)
	items = append(items, baseItem)
	items = append(items, providerItems...)
	summary := summarizeProxyTestItems(items)
	report := ProxyTestReport{
		ProxyID:        current.ID,
		ProxyName:      current.Name,
		Score:          summary.Score,
		Grade:          summary.Grade,
		Status:         summary.Status,
		PassedCount:    summary.PassedCount,
		WarningCount:   summary.WarningCount,
		FailedCount:    summary.FailedCount,
		OutboundIP:     outbound.IP,
		OutboundRegion: outbound.Region,
		BaseLatencyMs:  baseItem.LatencyMs,
		TestedAt:       testedAt.Format(time.RFC3339Nano),
		Items:          items,
		Message:        proxyTestReportMessage(summary.Status, summary.FailedCount, summary.WarningCount),
	}
	updatedAt := s.now().UTC()
	updated, found, err := writer.UpdateManagementProxyTestState(ctx, port.ManagementProxyTestStateInput{
		ID:              current.ID,
		TestStatus:      report.Status,
		LatencyMs:       report.BaseLatencyMs,
		OutboundIP:      port.ManagementProxyNullableTextPatch{Set: report.OutboundIP != nil, Value: report.OutboundIP},
		OutboundRegion:  port.ManagementProxyNullableTextPatch{Set: report.OutboundRegion != nil, Value: report.OutboundRegion},
		LastTestMessage: report.Message,
		LastTestedAt:    testedAt,
		UpdatedAt:       updatedAt,
	})
	if err != nil {
		return TestResult{}, err
	}
	if !found {
		return TestResult{}, ErrProxyNotFound
	}
	s.invalidate(ctx, ProxyTestStateUpdatedReason)
	return TestResult{
		Before: proxySummaryFromPort(current),
		Proxy:  proxySummaryFromPort(updated),
		Report: report,
	}, nil
}

func (s *Service) proxyURL(proxy port.ManagementProxySummary) (string, error) {
	scheme := strings.TrimSpace(proxy.Type)
	if scheme == "socks5" {
		scheme = "socks5h"
	}
	switch scheme {
	case "http", "https", "socks5h":
	default:
		return "", &ValidationError{Message: "代理类型无效"}
	}
	password := ""
	if proxy.PasswordEncrypted != nil && strings.TrimSpace(*proxy.PasswordEncrypted) != "" {
		if s.codec == nil {
			return "", fmt.Errorf("%w: codec is required", ErrProxyCredentialCodecUnusable)
		}
		credential, err := s.codec.DecryptJSON(*proxy.PasswordEncrypted)
		if err != nil {
			return "", fmt.Errorf("%w: 代理凭据解密失败", ErrProxyCredentialCodecUnusable)
		}
		if value, ok := credential["password"].(string); ok {
			password = value
		}
	}
	host := strings.Trim(strings.TrimSpace(proxy.Host), "[]")
	proxyAddress := url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(host, strconv.Itoa(proxy.Port)),
	}
	username := ""
	if proxy.Username != nil {
		username = *proxy.Username
	}
	if username != "" {
		if password != "" {
			proxyAddress.User = url.UserPassword(username, password)
		} else {
			proxyAddress.User = url.User(username)
		}
	}
	return proxyAddress.String(), nil
}

func (s *Service) testProvider(ctx context.Context, proxyURL string, provider port.ManagementProviderOption) ProxyTestItem {
	targetURL := provider.BaseURL
	targetURLValue := targetURL
	name := strings.TrimSpace(provider.Name)
	if name == "" {
		name = provider.Code
	}
	if ctx.Err() != nil {
		return ProxyTestItem{
			Name:      name,
			Status:    "unknown",
			Message:   "未发起目标请求：代理检测总耗时已达到上限",
			TargetURL: &targetURLValue,
		}
	}
	result, err := s.probe.Probe(ctx, ProxyProbeInput{
		TargetURL: targetURL,
		ProxyURL:  proxyURL,
		Timeout:   remainingProxyProbeTimeout(ctx),
	})
	if err != nil {
		message := safeProxyProbeError(err, proxyURL)
		if errors.Is(err, context.DeadlineExceeded) {
			return ProxyTestItem{
				Name:      name,
				Status:    "failed",
				Message:   "代理检测请求超时",
				TargetURL: &targetURLValue,
			}
		}
		if isTransportProxyProbeFailure(err) {
			return ProxyTestItem{
				Name:      name,
				Status:    "failed",
				Message:   message,
				TargetURL: &targetURLValue,
			}
		}
		return ProxyTestItem{
			Name:      name,
			Status:    "unknown",
			Message:   "未形成真实代理检测请求：" + message,
			TargetURL: &targetURLValue,
		}
	}
	statusCode := result.StatusCode
	latencyMs := result.LatencyMs
	return ProxyTestItem{
		Name:       name,
		Status:     "passed",
		HTTPStatus: &statusCode,
		LatencyMs:  &latencyMs,
		Message:    proxyProviderMessage(result.StatusCode),
		TargetURL:  &targetURLValue,
	}
}

func (s *Service) probeOutboundInfo(ctx context.Context, proxyURL string) proxyOutboundInfo {
	for _, target := range outboundProbeTargets {
		if ctx.Err() != nil {
			return proxyOutboundInfo{}
		}
		result, err := s.probe.Probe(ctx, ProxyProbeInput{
			TargetURL: target.URL,
			ProxyURL:  proxyURL,
			Timeout:   remainingProxyProbeTimeout(ctx),
		})
		if err != nil || result.StatusCode != 200 {
			continue
		}
		info, err := parseOutboundProbeResponse(target.Parser, result.Body)
		if err == nil && info.IP != nil {
			return info
		}
	}
	return proxyOutboundInfo{}
}

func remainingProxyProbeTimeout(ctx context.Context) time.Duration {
	timeout := proxyProbeTimeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining < timeout {
			timeout = remaining
		}
	}
	if timeout <= 0 {
		return time.Millisecond
	}
	return timeout
}

func baseConnectivityItem(items []ProxyTestItem, providerCount int) ProxyTestItem {
	if providerCount <= 0 {
		return ProxyTestItem{
			Name:    "基础连通性",
			Status:  "unknown",
			Message: "没有启用的供应商默认地址，未形成代理传输检测",
		}
	}
	failedCount := 0
	unknownCount := 0
	passedCount := 0
	latencyTotal := 0
	latencyCount := 0
	for _, item := range items {
		if item.Status == "failed" {
			failedCount++
		}
		if item.Status == "unknown" {
			unknownCount++
		}
		if item.Status == "passed" {
			passedCount++
		}
		if item.LatencyMs != nil {
			latencyTotal += *item.LatencyMs
			latencyCount++
		}
	}
	var latencyMs *int
	if latencyCount > 0 {
		average := int(math.Round(float64(latencyTotal) / float64(latencyCount)))
		latencyMs = &average
	}
	status := "unknown"
	message := "供应商默认地址均未形成真实传输检测"
	switch {
	case failedCount == 0 && unknownCount == 0:
		status = "passed"
		message = "全部供应商默认地址可达"
	case passedCount > 0:
		status = "warning"
		message = fmt.Sprintf("部分供应商默认地址完成传输检测（%d/%d）", passedCount, providerCount)
	case failedCount > 0:
		status = "failed"
		message = "供应商默认地址全部发生传输失败"
	}
	return ProxyTestItem{
		Name:      "基础连通性",
		Status:    status,
		LatencyMs: latencyMs,
		Message:   message,
	}
}

type proxyTestSummary struct {
	Score        int
	Grade        string
	Status       string
	PassedCount  int
	WarningCount int
	FailedCount  int
}

func summarizeProxyTestItems(items []ProxyTestItem) proxyTestSummary {
	summary := proxyTestSummary{}
	for _, item := range items {
		switch item.Status {
		case "passed":
			summary.PassedCount++
		case "warning":
			summary.WarningCount++
		case "failed":
			summary.FailedCount++
		}
	}
	unknownCount := 0
	for _, item := range items {
		if item.Status == "unknown" {
			unknownCount++
		}
	}
	switch {
	case summary.FailedCount > 0:
		summary.Status = "failed"
	case summary.WarningCount > 0 || (summary.PassedCount > 0 && unknownCount > 0):
		summary.Status = "warning"
	case unknownCount > 0 || len(items) == 0:
		summary.Status = "unknown"
	default:
		summary.Status = "passed"
	}
	if summary.Status == "unknown" {
		summary.Score = 0
	} else {
		summary.Score = max(0, int(math.Round(100-float64(summary.WarningCount*10)-float64(summary.FailedCount*35))))
	}
	switch {
	case summary.Score >= 90:
		summary.Grade = "A"
	case summary.Score >= 75:
		summary.Grade = "B"
	case summary.Score >= 60:
		summary.Grade = "C"
	default:
		summary.Grade = "D"
	}
	return summary
}

func proxyProviderMessage(statusCode int) string {
	return fmt.Sprintf("HTTP %d（传输完整，状态码仅供诊断）", statusCode)
}

func proxyTestReportMessage(status string, failedCount int, warningCount int) string {
	switch status {
	case "passed":
		return "代理质量检测通过"
	case "warning":
		return fmt.Sprintf("代理可用，存在 %d 项告警", warningCount)
	case "failed":
		return fmt.Sprintf("代理检测存在 %d 项失败", failedCount)
	default:
		return "代理检测未形成有效传输尝试"
	}
}

func safeProxyProbeError(err error, proxyURL string) string {
	message := strings.TrimSpace(err.Error())
	if message == "" {
		return "目标地址不可达"
	}
	if proxyURL != "" {
		message = strings.ReplaceAll(message, proxyURL, "[代理]")
		if parsed, parseErr := url.Parse(proxyURL); parseErr == nil && parsed.User != nil {
			message = strings.ReplaceAll(message, parsed.User.String(), "***")
		}
	}
	return message
}

func parseOutboundProbeResponse(parser string, body string) (proxyOutboundInfo, error) {
	var value struct {
		Success      *bool  `json:"success"`
		Status       any    `json:"status"`
		Message      string `json:"message"`
		Query        any    `json:"query"`
		IP           any    `json:"ip"`
		Origin       any    `json:"origin"`
		Country      any    `json:"country"`
		CountryCode  any    `json:"country_code"`
		CountryCode2 any    `json:"countryCode"`
		Region       any    `json:"region"`
		RegionName   any    `json:"regionName"`
		City         any    `json:"city"`
	}
	if err := json.Unmarshal([]byte(body), &value); err != nil {
		return proxyOutboundInfo{}, err
	}
	switch parser {
	case "httpbin":
		return proxyOutboundInfo{IP: firstIP(value.Origin)}, nil
	case "ipify":
		return proxyOutboundInfo{IP: stringValue(value.IP)}, nil
	case "ip-api":
		if strings.ToLower(fmt.Sprint(value.Status)) != "success" {
			if value.Message != "" {
				return proxyOutboundInfo{}, errors.New(value.Message)
			}
			return proxyOutboundInfo{}, errors.New("出口信息探测失败")
		}
		return proxyOutboundInfo{
			IP:     stringValue(value.Query),
			Region: regionText(value.Country, value.CountryCode2, value.RegionName, value.Region, value.City),
		}, nil
	case "ipwhois":
		if value.Success != nil && !*value.Success {
			if value.Message != "" {
				return proxyOutboundInfo{}, errors.New(value.Message)
			}
			return proxyOutboundInfo{}, errors.New("出口信息探测失败")
		}
		return proxyOutboundInfo{
			IP:     stringValue(value.IP),
			Region: regionText(value.Country, value.CountryCode, value.Region, value.City),
		}, nil
	case "ipsb":
		return proxyOutboundInfo{
			IP:     stringValue(value.IP),
			Region: regionText(value.Country, value.CountryCode, value.Region, value.City),
		}, nil
	case "ipinfo":
		return proxyOutboundInfo{
			IP:     stringValue(value.IP),
			Region: regionText(nil, value.Country, value.Region, value.City),
		}, nil
	default:
		return proxyOutboundInfo{}, nil
	}
}

func stringValue(value any) *string {
	text, ok := value.(string)
	if !ok {
		return nil
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	return &text
}

func firstIP(value any) *string {
	text := stringValue(value)
	if text == nil {
		return nil
	}
	first := strings.TrimSpace(strings.Split(*text, ",")[0])
	if first == "" {
		return nil
	}
	return &first
}

func regionText(countryValue any, countryCodeValue any, fallbackValues ...any) *string {
	if country := countryDisplayName(countryValue, countryCodeValue); country != nil {
		return country
	}
	for _, value := range fallbackValues {
		if text := stringValue(value); text != nil {
			return text
		}
	}
	return nil
}

func countryDisplayName(countryValue any, countryCodeValue any) *string {
	country := stringValue(countryValue)
	countryCode := stringValue(countryCodeValue)
	if countryCode == nil && country != nil && len(*country) == 2 {
		countryCode = country
	}
	if countryCode == nil {
		return country
	}
	region, err := language.ParseRegion(strings.ToUpper(*countryCode))
	if err != nil {
		return country
	}
	name := strings.TrimSpace(display.Regions(language.SimplifiedChinese).Name(region))
	if name == "" {
		return countryCode
	}
	return &name
}
