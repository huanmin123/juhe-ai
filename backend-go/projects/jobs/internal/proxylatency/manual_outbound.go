package proxylatency

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

type manualOutboundInfo struct {
	IP     string
	Region string
}

type manualOutboundTarget struct {
	URL    string
	Parser string
}

var manualOutboundTargets = []manualOutboundTarget{
	{URL: "http://ip-api.com/json/?lang=zh-CN", Parser: "ip-api"},
	{URL: "https://ipwho.is/", Parser: "ipwhois"},
	{URL: "https://api.ip.sb/geoip", Parser: "ipsb"},
	{URL: "https://ipinfo.io/json", Parser: "ipinfo"},
	{URL: "https://api.ipify.org?format=json", Parser: "ipify"},
	{URL: "http://httpbin.org/ip", Parser: "httpbin"},
}

func probeManualOutbound(ctx context.Context, proxyURL string, timeout time.Duration) (manualOutboundInfo, bool) {
	if timeout <= 0 {
		return manualOutboundInfo{}, false
	}
	for _, target := range manualOutboundTargets {
		if err := ctx.Err(); err != nil {
			return manualOutboundInfo{}, false
		}
		info, ok := requestManualOutbound(ctx, proxyURL, target, timeout)
		if ok && info.IP != "" {
			return info, true
		}
	}
	return manualOutboundInfo{}, false
}

func requestManualOutbound(ctx context.Context, proxyURL string, target manualOutboundTarget, timeout time.Duration) (manualOutboundInfo, bool) {
	targetURL, err := url.Parse(target.URL)
	if err != nil {
		return manualOutboundInfo{}, false
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	transport, err := newProxyTransport(proxyURL, timeout)
	if err != nil {
		return manualOutboundInfo{}, false
	}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, targetURL.String(), nil)
	if err != nil {
		return manualOutboundInfo{}, false
	}
	parsedProxy, err := url.Parse(proxyURL)
	if err != nil {
		return manualOutboundInfo{}, false
	}
	applyNodeProbeHeaders(request, targetURL, parsedProxy)
	client := upstreamhttp.NewClientWithTransport(transport)
	response, err := client.Do(request)
	if err != nil {
		return manualOutboundInfo{}, false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = upstreamhttp.Drain(response.Body)
		return manualOutboundInfo{}, false
	}
	body, err := upstreamhttp.ReadAndDrainBounded(response.Body, maxResponseBodyBytes)
	if err != nil {
		return manualOutboundInfo{}, false
	}
	return parseManualOutbound(target.Parser, body)
}

func parseManualOutbound(parser string, body []byte) (manualOutboundInfo, bool) {
	var value map[string]any
	if err := json.Unmarshal(body, &value); err != nil {
		return manualOutboundInfo{}, false
	}
	stringValue := func(key string) string {
		value, ok := value[key].(string)
		if !ok {
			return ""
		}
		return strings.TrimSpace(value)
	}
	firstIP := func(raw string) string {
		if index := strings.IndexByte(raw, ','); index >= 0 {
			raw = raw[:index]
		}
		return strings.TrimSpace(raw)
	}
	region := func(country, countryCode string, fallback ...string) string {
		if country = strings.TrimSpace(country); country != "" {
			return country
		}
		if countryCode = strings.TrimSpace(countryCode); countryCode != "" {
			return strings.ToUpper(countryCode)
		}
		for _, candidate := range fallback {
			if candidate = strings.TrimSpace(candidate); candidate != "" {
				return candidate
			}
		}
		return ""
	}
	switch parser {
	case "ip-api":
		if strings.ToLower(stringValue("status")) != "success" {
			return manualOutboundInfo{}, false
		}
		return manualOutboundInfo{IP: stringValue("query"), Region: region(stringValue("country"), stringValue("countryCode"), stringValue("regionName"), stringValue("region"), stringValue("city"))}, stringValue("query") != ""
	case "ipwhois":
		if success, exists := value["success"].(bool); exists && !success {
			return manualOutboundInfo{}, false
		}
		ip := stringValue("ip")
		return manualOutboundInfo{IP: ip, Region: region(stringValue("country"), stringValue("country_code"), stringValue("region"), stringValue("city"))}, ip != ""
	case "ipsb":
		ip := stringValue("ip")
		return manualOutboundInfo{IP: ip, Region: region(stringValue("country"), stringValue("country_code"), stringValue("region"), stringValue("city"))}, ip != ""
	case "ipinfo":
		ip := stringValue("ip")
		return manualOutboundInfo{IP: ip, Region: region("", stringValue("country"), stringValue("region"), stringValue("city"))}, ip != ""
	case "ipify":
		ip := stringValue("ip")
		return manualOutboundInfo{IP: ip}, ip != ""
	case "httpbin":
		ip := firstIP(stringValue("origin"))
		return manualOutboundInfo{IP: ip}, ip != ""
	default:
		return manualOutboundInfo{}, false
	}
}
