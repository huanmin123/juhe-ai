package oauthmgmt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Grok SSO device-flow constants mirror grok-sso-device-flow.ts.
const (
	GrokSSOBuildScope           = "openid profile email offline_access grok-cli:access api:access conversations:read conversations:write"
	GrokSSOAccountsURL          = "https://accounts.x.ai/"
	GrokSSODeviceURL            = "https://auth.x.ai/oauth2/device/code"
	GrokSSOVerifyURL            = "https://auth.x.ai/oauth2/device/verify"
	GrokSSOApproveURL           = "https://auth.x.ai/oauth2/device/approve"
	GrokSSOTokenURL             = "https://auth.x.ai/oauth2/token"
	GrokSSOClientID             = "b1a00492-073a-47ea-816f-4c329264a828"
	grokSSODefaultPollInterval  = 5 * time.Second
	grokSSODefaultDeviceExpires = 30 * time.Minute
	grokSSOMaxPollDuration      = 75 * time.Second
	grokSSOMaxRedirects         = 8
)

// grokSSOUserAgent mirrors grokSSODefaultUserAgent.
const grokSSOUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// SSODeviceRequest mirrors GrokSSODeviceRequest.
type SSODeviceRequest struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    string
}

// SSODeviceResponse mirrors GrokSSODeviceResponse.
type SSODeviceResponse struct {
	StatusCode int
	Headers    map[string]string
	Body       string
}

// SSODeviceRequester is the injected device-flow transport (Node
// GrokSSODeviceDependencies.request). The default performs real HTTPS; tests
// inject a scripted requester, so the slice never leaves the process in CI.
type SSODeviceRequester interface {
	Do(ctx context.Context, request SSODeviceRequest) (SSODeviceResponse, error)
}

// SSODeviceDeps mirrors GrokSSODeviceDependencies with test seams for sleep
// and the clock.
type SSODeviceDeps struct {
	Request SSODeviceRequester
	Sleep   func(ctx context.Context, delay time.Duration) error
	Now     func() time.Time
}

// defaultSSODeviceRequester is the production device-flow transport: manual
// redirect handling (the flow owns redirect logic), 90s per-request timeout.
func defaultSSODeviceRequester() SSODeviceRequester {
	client := &http.Client{
		Timeout: 90 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return SSODeviceRequesterFunc(func(ctx context.Context, request SSODeviceRequest) (SSODeviceResponse, error) {
		req, err := http.NewRequestWithContext(ctx, request.Method, request.URL, strings.NewReader(request.Body))
		if err != nil {
			return SSODeviceResponse{}, err
		}
		for key, value := range request.Headers {
			req.Header.Set(key, value)
		}
		response, err := client.Do(req)
		if err != nil {
			return SSODeviceResponse{}, err
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024+1))
		if err != nil {
			return SSODeviceResponse{}, err
		}
		headers := map[string]string{}
		for key, values := range response.Header {
			if len(values) > 0 {
				headers[strings.ToLower(key)] = strings.Join(values, "\n")
			}
		}
		return SSODeviceResponse{StatusCode: response.StatusCode, Headers: headers, Body: string(body)}, nil
	})
}

// SSODeviceRequesterFunc adapts a function to SSODeviceRequester.
type SSODeviceRequesterFunc func(ctx context.Context, request SSODeviceRequest) (SSODeviceResponse, error)

// Do implements SSODeviceRequester.
func (f SSODeviceRequesterFunc) Do(ctx context.Context, request SSODeviceRequest) (SSODeviceResponse, error) {
	return f(ctx, request)
}

// grokSSODeviceError mirrors GrokSSODeviceError.
type grokSSODeviceError struct {
	Message    string
	StatusCode int
}

func (e *grokSSODeviceError) Error() string { return e.Message }

func grokSSOHTTPError(message string, statusCode int) *grokSSODeviceError {
	return &grokSSODeviceError{
		Message:    fmt.Sprintf("%s：xAI OAuth HTTP %d", message, statusCode),
		StatusCode: 502,
	}
}

// normalizeGrokSSOToken mirrors normalizeGrokSSOToken: accepts raw values,
// "cookie: ..." headers and name=value cookie lists (sso / sso-rw).
func normalizeGrokSSOToken(value string) string {
	normalized := strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(normalized), "cookie:") {
		normalized = strings.TrimSpace(normalized[len("cookie:"):])
	}
	for _, part := range strings.Split(normalized, ";") {
		separator := strings.Index(part, "=")
		if separator < 0 {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(part[:separator]))
		if name == "sso" || name == "sso-rw" {
			return sanitizeSSOToken(part[separator+1:])
		}
	}
	if separator := strings.Index(normalized, ";"); separator >= 0 {
		normalized = strings.TrimSpace(normalized[:separator])
	}
	return sanitizeSSOToken(normalized)
}

// normalizeGrokSSOImportTokens mirrors normalizeGrokSSOImportTokens: split,
// normalize, dedupe, keep order.
func normalizeGrokSSOImportTokens(tokens []string, single string) []string {
	seen := map[string]bool{}
	output := []string{}
	inputs := []string{}
	if single = strings.TrimSpace(single); single != "" {
		inputs = append(inputs, single)
	}
	inputs = append(inputs, tokens...)
	for _, input := range inputs {
		for _, piece := range strings.Split(strings.NewReplacer("\r", "\n", ",", "\n").Replace(input), "\n") {
			token := normalizeGrokSSOToken(piece)
			if token == "" || seen[token] {
				continue
			}
			seen[token] = true
			output = append(output, token)
		}
	}
	return output
}

// convertGrokSSOToOAuth mirrors convertGrokSSOToOAuth: device flow with the
// SSO cookie jar, consent automation and token polling.
func convertGrokSSOToOAuth(ctx context.Context, ssoToken string, deps SSODeviceDeps) (*grokRawToken, error) {
	ssoToken = normalizeGrokSSOToken(ssoToken)
	if ssoToken == "" {
		return nil, &grokSSODeviceError{Message: "xAI SSO 未授权", StatusCode: 400}
	}
	if deps.Sleep == nil {
		deps.Sleep = func(ctx context.Context, delay time.Duration) error {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		}
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	flow := &grokSSODeviceFlow{deps: deps, cookies: map[string]grokSSOCookie{}}
	flow.storeCookie(grokSSOCookie{name: "sso", value: ssoToken, domain: "x.ai", path: "/", secure: true})
	flow.storeCookie(grokSSOCookie{name: "sso-rw", value: ssoToken, domain: "x.ai", path: "/", secure: true})
	return flow.convert(ctx)
}

// grokSSOCookie mirrors GrokSSOCookie.
type grokSSOCookie struct {
	name      string
	value     string
	domain    string
	path      string
	secure    bool
	hostOnly  bool
	expiresAt time.Time
	hasExpiry bool
}

// grokSSODeviceFlow mirrors GrokSSODeviceFlow.
type grokSSODeviceFlow struct {
	deps    SSODeviceDeps
	cookies map[string]grokSSOCookie
}

func (f *grokSSODeviceFlow) now() time.Time { return f.deps.Now() }

func (f *grokSSODeviceFlow) storeCookie(cookie grokSSOCookie) {
	f.cookies[cookieKey(cookie)] = cookie
}

// convert mirrors GrokSSODeviceFlow.convert: accounts check → device code →
// verification page → verify → approve → poll token.
func (f *grokSSODeviceFlow) convert(ctx context.Context) (*grokRawToken, error) {
	response, err := f.request(ctx, http.MethodGet, GrokSSOAccountsURL, nil)
	if err != nil {
		return nil, err
	}
	if response.StatusCode == 401 || strings.Contains(response.FinalURL, "sign-in") || strings.Contains(response.FinalURL, "sign-up") {
		return nil, &grokSSODeviceError{Message: "xAI SSO 未授权", StatusCode: 400}
	}
	if response.StatusCode < 200 || response.StatusCode >= 400 {
		return nil, grokSSOHTTPError("校验 Grok Web SSO 失败", response.StatusCode)
	}

	response, err = f.request(ctx, http.MethodPost, GrokSSODeviceURL, map[string]string{
		"client_id": GrokSSOClientID,
		"scope":     GrokSSOBuildScope,
	})
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, grokSSOHTTPError("启动 xAI device flow 失败", response.StatusCode)
	}
	device := parseTokenPayload(response.Body)
	deviceCode := normalizeText(device["device_code"])
	userCode := normalizeText(device["user_code"])
	verificationURL := normalizeText(device["verification_uri_complete"])
	if deviceCode == "" || userCode == "" || !isTrustedXAIAuthURL(verificationURL) {
		return nil, &grokSSODeviceError{Message: "xAI device flow 响应不完整", StatusCode: 502}
	}
	interval := grokSSODefaultPollInterval
	if value, ok := finitePositiveInt(device["interval"]); ok {
		interval = time.Duration(value) * time.Second
	}
	expiresIn := grokSSODefaultDeviceExpires
	if value, ok := finitePositiveInt(device["expires_in"]); ok {
		expiresIn = time.Duration(value) * time.Second
	}

	response, err = f.request(ctx, http.MethodGet, verificationURL, nil)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 400 {
		return nil, grokSSOHTTPError("打开 xAI device 验证页失败", response.StatusCode)
	}

	response, err = f.request(ctx, http.MethodPost, GrokSSOVerifyURL, map[string]string{"user_code": userCode})
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 400 {
		return nil, grokSSOHTTPError("校验 xAI device code 失败", response.StatusCode)
	}
	if !strings.Contains(response.FinalURL, "consent") {
		return nil, &grokSSODeviceError{Message: "xAI device 验证未进入 consent 页面", StatusCode: 502}
	}

	response, err = f.request(ctx, http.MethodPost, GrokSSOApproveURL, map[string]string{
		"user_code":      userCode,
		"action":         "allow",
		"principal_type": "User",
		"principal_id":   "",
	})
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 400 {
		return nil, grokSSOHTTPError("批准 xAI device code 失败", response.StatusCode)
	}
	if !strings.Contains(response.FinalURL, "done") {
		return nil, &grokSSODeviceError{Message: "xAI device 授权未进入 done 页面", StatusCode: 502}
	}

	return f.pollToken(ctx, deviceCode, interval, expiresIn)
}

// pollToken mirrors pollToken: authorization_pending retries, slow_down
// backoff, hard failures on access_denied/expired_token.
func (f *grokSSODeviceFlow) pollToken(ctx context.Context, deviceCode string, interval, expiresIn time.Duration) (*grokRawToken, error) {
	if interval < time.Second {
		interval = time.Second
	}
	deadline := f.now().Add(minDuration(expiresIn, grokSSOMaxPollDuration))
	for f.now().Before(deadline) {
		if err := f.deps.Sleep(ctx, interval); err != nil {
			return nil, &grokSSODeviceError{Message: "xAI SSO 转换已取消或超时", StatusCode: 502}
		}
		response, err := f.request(ctx, http.MethodPost, GrokSSOTokenURL, map[string]string{
			"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
			"client_id":   GrokSSOClientID,
			"device_code": deviceCode,
		})
		if err != nil {
			return nil, err
		}
		payload := parseTokenPayload(response.Body)
		accessToken := normalizeText(payload["access_token"])
		if response.StatusCode >= 200 && response.StatusCode < 300 && accessToken != "" {
			expiresIn := grokDefaultTokenTTL
			if value, ok := finitePositiveInt(payload["expires_in"]); ok {
				expiresIn = value
			}
			tokenType := normalizeText(payload["token_type"])
			if tokenType == "" {
				tokenType = "Bearer"
			}
			return &grokRawToken{
				AccessToken:  accessToken,
				RefreshToken: normalizeText(payload["refresh_token"]),
				IDToken:      normalizeText(payload["id_token"]),
				TokenType:    tokenType,
				ExpiresIn:    expiresIn,
				Scope:        normalizeText(payload["scope"]),
			}, nil
		}
		errorCode := normalizeText(payload["error"])
		switch errorCode {
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
			continue
		case "access_denied", "expired_token":
			return nil, &grokSSODeviceError{Message: "xAI device 授权被拒绝或已过期", StatusCode: 400}
		}
		detail := normalizeText(payload["error_description"])
		if detail == "" {
			detail = errorCode
		}
		if response.StatusCode >= 400 {
			message := "xAI token 轮询失败"
			if detail != "" {
				message += "：" + detail
			}
			return nil, &grokSSODeviceError{
				Message:    fmt.Sprintf("%s（HTTP %d）", message, response.StatusCode),
				StatusCode: 502,
			}
		}
		message := "xAI token 轮询失败"
		if detail != "" {
			return nil, &grokSSODeviceError{Message: message + "：" + detail, StatusCode: 502}
		}
		return nil, &grokSSODeviceError{Message: fmt.Sprintf("%s：HTTP %d", message, response.StatusCode), StatusCode: 502}
	}
	return nil, &grokSSODeviceError{Message: "xAI device flow token 轮询超时", StatusCode: 502}
}

// ssoDeviceResponseWithFinalURL carries the redirect-settled URL.
type ssoDeviceResponseWithFinalURL struct {
	SSODeviceResponse
	FinalURL string
}

// request mirrors GrokSSODeviceFlow.request: trusted-URL enforcement, cookie
// header, manual redirect handling (POST→GET on 303/301/302), cookie capture
// and the 2 MiB body guard.
func (f *grokSSODeviceFlow) request(ctx context.Context, method, initialURL string, form map[string]string) (*ssoDeviceResponseWithFinalURL, error) {
	if !isTrustedXAIAuthURL(initialURL) {
		return nil, &grokSSODeviceError{Message: "xAI OAuth URL 不受信任", StatusCode: 502}
	}
	currentURL := initialURL
	currentMethod := method
	currentForm := form
	for redirects := 0; redirects <= grokSSOMaxRedirects; redirects++ {
		body := ""
		if currentForm != nil {
			body = encodeForm(currentForm)
		}
		headers := map[string]string{
			"accept":          "application/json, text/html;q=0.9, */*;q=0.8",
			"accept-language": "zh-CN,zh;q=0.9,en;q=0.8",
			"user-agent":      grokSSOUserAgent,
		}
		if cookie := f.cookieHeader(currentURL); cookie != "" {
			headers["cookie"] = cookie
		}
		if currentForm != nil {
			headers["content-type"] = "application/x-www-form-urlencoded"
			headers["content-length"] = itoa(len(body))
		}
		response, err := f.deps.Request.Do(ctx, SSODeviceRequest{
			Method: currentMethod, URL: currentURL, Headers: headers, Body: body,
		})
		if err != nil {
			return nil, err
		}
		f.captureCookies(response.Headers, currentURL)
		if len(response.Body) > 2*1024*1024 {
			return nil, &grokSSODeviceError{Message: "xAI OAuth 响应超过 2 MiB", StatusCode: 502}
		}
		if response.StatusCode < 300 || response.StatusCode > 399 {
			return &ssoDeviceResponseWithFinalURL{SSODeviceResponse: response, FinalURL: currentURL}, nil
		}
		location := strings.TrimSpace(response.Headers["location"])
		if location == "" {
			return nil, &grokSSODeviceError{Message: "xAI OAuth 重定向缺少 Location", StatusCode: 502}
		}
		parsed, parseErr := url.Parse(currentURL)
		next, nextErr := url.Parse(location)
		if parseErr != nil || nextErr != nil {
			return nil, &grokSSODeviceError{Message: "xAI OAuth 重定向到不受信任的主机", StatusCode: 502}
		}
		nextURL := parsed.ResolveReference(next).String()
		if !isTrustedXAIAuthURL(nextURL) {
			return nil, &grokSSODeviceError{Message: "xAI OAuth 重定向到不受信任的主机", StatusCode: 502}
		}
		currentURL = nextURL
		if response.StatusCode == http.StatusSeeOther ||
			((response.StatusCode == http.StatusMovedPermanently || response.StatusCode == http.StatusFound) && currentMethod != http.MethodGet) {
			currentMethod = http.MethodGet
			currentForm = nil
			continue
		}
		currentForm = form
	}
	return nil, &grokSSODeviceError{Message: "xAI OAuth 重定向次数过多", StatusCode: 502}
}

// captureCookies mirrors captureCookies: set-cookie parsing with the slice's
// simplified attribute handling (domain/path/max-age; Expires stays
// session-scoped).
func (f *grokSSODeviceFlow) captureCookies(headers map[string]string, responseURL string) {
	parsed, err := url.Parse(responseURL)
	if err != nil {
		return
	}
	responseHost := strings.ToLower(parsed.Hostname())
	setCookie := headers["set-cookie"]
	if setCookie == "" {
		return
	}
	for _, value := range strings.Split(setCookie, "\n") {
		pair := value
		attributes := ""
		if index := strings.Index(value, ";"); index >= 0 {
			pair = value[:index]
			attributes = value[index+1:]
		}
		separator := strings.Index(pair, "=")
		if separator <= 0 {
			continue
		}
		name := strings.TrimSpace(pair[:separator])
		cookieValue := strings.TrimSpace(pair[separator+1:])
		if name == "" || len(name) > 128 || len(cookieValue) > 16384 ||
			strings.ContainsAny(name+cookieValue, "\r\n\x00") {
			continue
		}
		attrs := map[string]string{}
		for _, attribute := range strings.Split(attributes, ";") {
			trimmed := strings.TrimSpace(attribute)
			if trimmed == "" {
				continue
			}
			eq := strings.Index(trimmed, "=")
			key := strings.ToLower(strings.TrimSpace(trimmed))
			valueText := ""
			if eq >= 0 {
				key = strings.ToLower(strings.TrimSpace(trimmed[:eq]))
				valueText = strings.TrimSpace(trimmed[eq+1:])
			}
			if _, exists := attrs[key]; !exists {
				attrs[key] = valueText
			}
		}
		requestedDomain := strings.TrimPrefix(strings.ToLower(attrs["domain"]), ".")
		if requestedDomain != "" && !cookieDomainMatches(responseHost, requestedDomain) {
			continue
		}
		domain := requestedDomain
		if domain == "" {
			domain = responseHost
		}
		path := attrs["path"]
		if !strings.HasPrefix(path, "/") {
			path = defaultCookiePath(parsed.Path)
		}
		_, securePresent := attrs["secure"]
		cookie := grokSSOCookie{
			name: name, value: cookieValue, domain: domain, path: path,
			secure:   securePresent,
			hostOnly: requestedDomain == "",
		}
		deleteCookie := false
		if rawMaxAge, hasMaxAge := attrs["max-age"]; hasMaxAge {
			if seconds, parseErr := parseLeadingInt(rawMaxAge); parseErr == nil {
				if seconds <= 0 {
					deleteCookie = true
				} else {
					cookie.expiresAt = f.now().Add(time.Duration(seconds) * time.Second)
					cookie.hasExpiry = true
				}
			}
		}
		if !deleteCookie && cookie.hasExpiry && !cookie.expiresAt.After(f.now()) {
			deleteCookie = true
		}
		key := cookieKey(cookie)
		if deleteCookie {
			delete(f.cookies, key)
			continue
		}
		f.cookies[key] = cookie
	}
}

// cookieHeader mirrors cookieHeader: expired/secure/host filtering, path
// length then name ordering.
func (f *grokSSODeviceFlow) cookieHeader(requestURL string) string {
	parsed, err := url.Parse(requestURL)
	if err != nil {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	now := f.now()
	cookies := make([]grokSSOCookie, 0, len(f.cookies))
	for _, cookie := range f.cookies {
		if cookie.hasExpiry && !cookie.expiresAt.After(now) {
			continue
		}
		if cookie.secure && parsed.Scheme != "https" {
			continue
		}
		if cookie.hostOnly {
			if cookie.domain != host {
				continue
			}
		} else if !cookieDomainMatches(host, cookie.domain) {
			continue
		}
		if !cookiePathMatches(parsed.Path, cookie.path) {
			continue
		}
		cookies = append(cookies, cookie)
	}
	sort.SliceStable(cookies, func(i, j int) bool {
		if len(cookies[i].path) != len(cookies[j].path) {
			return len(cookies[i].path) > len(cookies[j].path)
		}
		return cookies[i].name < cookies[j].name
	})
	parts := make([]string, 0, len(cookies))
	for _, cookie := range cookies {
		parts = append(parts, cookie.name+"="+cookie.value)
	}
	return strings.Join(parts, "; ")
}

func cookieKey(cookie grokSSOCookie) string {
	return cookie.name + "\x00" + cookie.domain + "\x00" + cookie.path
}

func cookieDomainMatches(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

func cookiePathMatches(requestPath, cookiePath string) bool {
	if requestPath == cookiePath {
		return true
	}
	if !strings.HasPrefix(requestPath, cookiePath) {
		return false
	}
	return strings.HasSuffix(cookiePath, "/") ||
		(len(requestPath) > len(cookiePath) && requestPath[len(cookiePath)] == '/')
}

func defaultCookiePath(pathname string) string {
	if !strings.HasPrefix(pathname, "/") || pathname == "/" {
		return "/"
	}
	lastSlash := strings.LastIndex(pathname, "/")
	if lastSlash <= 0 {
		return "/"
	}
	return pathname[:lastSlash]
}

func isTrustedXAIAuthURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Scheme != "https" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "x.ai" || strings.HasSuffix(host, ".x.ai")
}

func sanitizeSSOToken(value string) string {
	return strings.TrimSpace(strings.NewReplacer("\r", "", "\n", "", "\x00", "").Replace(value))
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func parseLeadingInt(value string) (int, error) {
	value = strings.TrimSpace(value)
	digits := value
	if digits == "" {
		return 0, errors.New("empty")
	}
	negative := false
	if digits[0] == '-' || digits[0] == '+' {
		negative = digits[0] == '-'
		digits = digits[1:]
	}
	number := 0
	for _, char := range digits {
		if char < '0' || char > '9' {
			return 0, errors.New("not a number")
		}
		number = number*10 + int(char-'0')
	}
	if negative {
		number = -number
	}
	return number, nil
}
