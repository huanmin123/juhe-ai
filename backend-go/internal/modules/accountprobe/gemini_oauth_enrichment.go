package accountprobe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	geminiOAuthEnrichmentTimeout    = 25 * time.Second
	geminiOAuthEnrichmentMaxBody    = 256 << 10
	geminiOAuthCloudCodeURL         = "https://cloudcode-pa.googleapis.com"
	geminiOAuthResourceManagerURL   = "https://cloudresourcemanager.googleapis.com"
	geminiOAuthGoogleAPIsURL        = "https://www.googleapis.com"
	geminiOAuthCLIUserAgent         = "GeminiCLI/0.1.5 (Windows; AMD64)"
	geminiOAuthDriveMetadataScope   = "https://www.googleapis.com/auth/drive.metadata.readonly"
	geminiOAuthEnrichmentMetadataID = "ANTIGRAVITY"
)

// ErrGeminiOAuthEnrichment identifies local discovery and enrichment failures.
var ErrGeminiOAuthEnrichment = errors.New("gemini OAuth enrichment failed")

// GeminiOAuthEnrichmentSecrets keeps access tokens out of diagnostics and
// structured logging. The token is intentionally only usable by this package.
type GeminiOAuthEnrichmentSecrets struct{ accessToken string }

func NewGeminiOAuthEnrichmentSecrets(accessToken string) GeminiOAuthEnrichmentSecrets {
	return GeminiOAuthEnrichmentSecrets{accessToken: strings.TrimSpace(accessToken)}
}

func (GeminiOAuthEnrichmentSecrets) String() string               { return "[REDACTED]" }
func (GeminiOAuthEnrichmentSecrets) GoString() string             { return "[REDACTED]" }
func (GeminiOAuthEnrichmentSecrets) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

type GeminiOAuthEnrichmentInput struct {
	OAuthType  string
	Secrets    GeminiOAuthEnrichmentSecrets
	ProjectID  string
	TierID     string
	Scope      string
	ProxyURL   string
	ObservedAt time.Time
}

func (GeminiOAuthEnrichmentInput) String() string               { return "[REDACTED]" }
func (GeminiOAuthEnrichmentInput) GoString() string             { return "[REDACTED]" }
func (GeminiOAuthEnrichmentInput) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

type GeminiOAuthEnrichmentOutput struct {
	ProjectID          string
	TierID             string
	DriveStorageLimit  *int64
	DriveStorageUsage  *int64
	DriveTierUpdatedAt time.Time
}

func (o GeminiOAuthEnrichmentOutput) String() string   { return "GeminiOAuthEnrichmentOutput{redacted}" }
func (o GeminiOAuthEnrichmentOutput) GoString() string { return o.String() }

// GeminiOAuthEnrichmentHTTPRequest is an immutable, transport-neutral request
// snapshot. Header and body accessors return copies so an executor cannot
// mutate the state machine's request.
type GeminiOAuthEnrichmentHTTPRequest struct {
	method  string
	url     string
	header  http.Header
	body    []byte
	proxy   string
	timeout time.Duration
}

func (r GeminiOAuthEnrichmentHTTPRequest) Method() string             { return r.method }
func (r GeminiOAuthEnrichmentHTTPRequest) URL() string                { return r.url }
func (r GeminiOAuthEnrichmentHTTPRequest) Header() http.Header        { return r.header.Clone() }
func (r GeminiOAuthEnrichmentHTTPRequest) Body() []byte               { return append([]byte(nil), r.body...) }
func (r GeminiOAuthEnrichmentHTTPRequest) ProxyURL() string           { return r.proxy }
func (r GeminiOAuthEnrichmentHTTPRequest) Timeout() time.Duration     { return r.timeout }
func (r GeminiOAuthEnrichmentHTTPRequest) MaxResponseBytes() int      { return geminiOAuthEnrichmentMaxBody }
func (GeminiOAuthEnrichmentHTTPRequest) String() string               { return "[REDACTED]" }
func (GeminiOAuthEnrichmentHTTPRequest) GoString() string             { return "[REDACTED]" }
func (GeminiOAuthEnrichmentHTTPRequest) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

type GeminiOAuthEnrichmentHTTPResponse struct {
	statusCode int
	body       []byte
	truncated  bool
}

func NewGeminiOAuthEnrichmentHTTPResponse(statusCode int, body []byte, truncated bool) GeminiOAuthEnrichmentHTTPResponse {
	return GeminiOAuthEnrichmentHTTPResponse{statusCode: statusCode, body: append([]byte(nil), body...), truncated: truncated}
}
func (r GeminiOAuthEnrichmentHTTPResponse) StatusCode() int            { return r.statusCode }
func (r GeminiOAuthEnrichmentHTTPResponse) Body() []byte               { return append([]byte(nil), r.body...) }
func (r GeminiOAuthEnrichmentHTTPResponse) Truncated() bool            { return r.truncated }
func (GeminiOAuthEnrichmentHTTPResponse) String() string               { return "[REDACTED]" }
func (GeminiOAuthEnrichmentHTTPResponse) GoString() string             { return "[REDACTED]" }
func (GeminiOAuthEnrichmentHTTPResponse) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

// GeminiOAuthEnrichmentHTTPExecutor is the only side-effect boundary. The
// caller supplies the URL policy, proxy implementation and bounded transport.
type GeminiOAuthEnrichmentHTTPExecutor interface {
	ExecuteGeminiOAuthEnrichment(context.Context, GeminiOAuthEnrichmentHTTPRequest) (GeminiOAuthEnrichmentHTTPResponse, error)
}

type geminiOAuthEnrichmentError struct {
	step   string
	status int
	cause  error
}

func (e *geminiOAuthEnrichmentError) Error() string {
	if e.status != 0 {
		return fmt.Sprintf("%s: HTTP %d", e.step, e.status)
	}
	return e.step
}
func (e *geminiOAuthEnrichmentError) String() string   { return e.Error() }
func (e *geminiOAuthEnrichmentError) GoString() string { return e.Error() }
func (e *geminiOAuthEnrichmentError) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{"message": e.Error()})
}
func (e *geminiOAuthEnrichmentError) Unwrap() []error {
	if e.cause == nil {
		return []error{ErrGeminiOAuthEnrichment}
	}
	return []error{ErrGeminiOAuthEnrichment, e.cause}
}

func wrapGeminiOAuthEnrichmentError(step string, status int, cause error) error {
	return &geminiOAuthEnrichmentError{step: step, status: status, cause: cause}
}

type GeminiOAuthEnricher struct {
	executor GeminiOAuthEnrichmentHTTPExecutor
	now      func() time.Time
	sleep    func(context.Context, time.Duration) error
}

func NewGeminiOAuthEnricher(executor GeminiOAuthEnrichmentHTTPExecutor) *GeminiOAuthEnricher {
	return &GeminiOAuthEnricher{executor: executor, now: time.Now, sleep: geminiOAuthEnrichmentSleep}
}

// EnrichGeminiOAuth applies the Node authority's Code Assist/Google One
// project and tier discovery. ai_studio intentionally has no discovery call.
func (e *GeminiOAuthEnricher) EnrichGeminiOAuth(ctx context.Context, input GeminiOAuthEnrichmentInput) (GeminiOAuthEnrichmentOutput, error) {
	if e == nil || e.executor == nil {
		return GeminiOAuthEnrichmentOutput{}, fmt.Errorf("%w: executor is required", ErrGeminiOAuthEnrichment)
	}
	oauthType := normalizeGeminiEnrichmentOAuthType(input.OAuthType)
	if input.Secrets.accessToken == "" {
		return GeminiOAuthEnrichmentOutput{}, fmt.Errorf("%w: access token is required", ErrGeminiOAuthEnrichment)
	}
	observedAt := input.ObservedAt
	if observedAt.IsZero() {
		observedAt = e.now()
	}
	observedAt = observedAt.UTC()

	projectID := strings.TrimSpace(input.ProjectID)
	tierID := canonicalGeminiEnrichmentTier(oauthType, input.TierID)
	if oauthType == "ai_studio" {
		if tierID == "" {
			tierID = "aistudio_free"
		}
		return GeminiOAuthEnrichmentOutput{ProjectID: projectID, TierID: tierID}, nil
	}

	if projectID == "" || (oauthType == "code_assist" && tierID == "") {
		detected, err := e.detectCodeAssistProjectAndTier(ctx, input, tierID)
		if err != nil {
			if projectID == "" && oauthType == "code_assist" {
				return GeminiOAuthEnrichmentOutput{}, err
			}
		} else {
			if projectID == "" {
				projectID = detected.projectID
			}
			if oauthType == "code_assist" && detected.tierID != "" {
				tierID = detected.tierID
			}
		}
	}
	if projectID == "" {
		if oauthType == "code_assist" {
			return GeminiOAuthEnrichmentOutput{}, fmt.Errorf("%w: Gemini Code Assist project_id is required", ErrGeminiOAuthEnrichment)
		}
		return GeminiOAuthEnrichmentOutput{}, fmt.Errorf("%w: Gemini Google One project_id is required", ErrGeminiOAuthEnrichment)
	}

	out := GeminiOAuthEnrichmentOutput{ProjectID: projectID, TierID: tierID}
	if oauthType == "code_assist" {
		if out.TierID == "" {
			out.TierID = "gcp_standard"
		}
		return out, nil
	}
	if out.TierID == "" {
		out.TierID = "google_one_free"
	}
	if hasGeminiDriveMetadataScope(input.Scope) {
		quota, err := e.fetchDriveStorageQuota(ctx, input)
		if err == nil {
			out.DriveStorageLimit = &quota.limit
			out.DriveStorageUsage = &quota.usage
			out.DriveTierUpdatedAt = observedAt
			if detected := inferGeminiGoogleOneTierBytes(quota.limit); detected != "google_one_unknown" {
				out.TierID = detected
			}
		}
	}
	return out, nil
}

type geminiCodeAssistDetection struct{ projectID, tierID string }

func (e *GeminiOAuthEnricher) detectCodeAssistProjectAndTier(ctx context.Context, input GeminiOAuthEnrichmentInput, preferredTier string) (geminiCodeAssistDetection, error) {
	load, loadErr := e.requestJSON(ctx, input, "loadCodeAssist", geminiOAuthCloudCodeURL+"/v1internal:loadCodeAssist", http.MethodPost, map[string]any{
		"metadata": map[string]string{"ideType": geminiOAuthEnrichmentMetadataID, "platform": "PLATFORM_UNSPECIFIED", "pluginType": "GEMINI"},
	})
	tierID := codeAssistTier(load)
	if tierID == "" {
		tierID = canonicalGeminiEnrichmentTier("code_assist", preferredTier)
		if tierID == "" {
			tierID = "gcp_standard"
		}
	}
	if project := codeAssistProject(load); project != "" {
		return geminiCodeAssistDetection{projectID: project, tierID: tierID}, nil
	}
	if codeAssistTier(load) != "" {
		if project := e.fetchResourceManagerProject(ctx, input); project != "" {
			return geminiCodeAssistDetection{projectID: project, tierID: tierID}, nil
		}
		return geminiCodeAssistDetection{}, fmt.Errorf("%w: registered Code Assist tier has no project", ErrGeminiOAuthEnrichment)
	}
	for attempt := 0; attempt < 5; attempt++ {
		onboard, err := e.requestJSON(ctx, input, "onboardUser", geminiOAuthCloudCodeURL+"/v1internal:onboardUser", http.MethodPost, map[string]any{
			"tierId":   upstreamGeminiCodeAssistTier(tierID),
			"metadata": map[string]string{"ideType": geminiOAuthEnrichmentMetadataID, "platform": "PLATFORM_UNSPECIFIED", "pluginType": "GEMINI"},
		})
		if err != nil {
			if project := e.fetchResourceManagerProject(ctx, input); project != "" {
				return geminiCodeAssistDetection{projectID: project, tierID: tierID}, nil
			}
			return geminiCodeAssistDetection{}, err
		}
		if onboard["done"] == true {
			if response, ok := onboard["response"].(map[string]any); ok {
				if project := normalizeGeminiEnrichmentString(response["cloudaicompanionProject"]); project != "" {
					return geminiCodeAssistDetection{projectID: project, tierID: tierID}, nil
				}
				if project := codeAssistProject(response); project != "" {
					return geminiCodeAssistDetection{projectID: project, tierID: tierID}, nil
				}
			}
			break
		}
		if err := e.sleep(ctx, 2*time.Second); err != nil {
			return geminiCodeAssistDetection{}, wrapGeminiOAuthEnrichmentError("onboardUser delay", 0, err)
		}
	}
	if project := e.fetchResourceManagerProject(ctx, input); project != "" {
		return geminiCodeAssistDetection{projectID: project, tierID: tierID}, nil
	}
	if loadErr != nil {
		return geminiCodeAssistDetection{}, fmt.Errorf("%w: loadCodeAssist and onboardUser did not provide project", ErrGeminiOAuthEnrichment)
	}
	return geminiCodeAssistDetection{}, fmt.Errorf("%w: onboardUser did not provide project", ErrGeminiOAuthEnrichment)
}

func (e *GeminiOAuthEnricher) fetchResourceManagerProject(ctx context.Context, input GeminiOAuthEnrichmentInput) string {
	payload, err := e.requestJSON(ctx, input, "resourceManager", geminiOAuthResourceManagerURL+"/v1/projects", http.MethodGet, nil)
	if err != nil {
		return ""
	}
	projects, ok := payload["projects"].([]any)
	if !ok {
		return ""
	}
	active := make([]map[string]any, 0, len(projects))
	for _, value := range projects {
		project, ok := value.(map[string]any)
		if ok && normalizeGeminiEnrichmentString(project["projectId"]) != "" && normalizeGeminiEnrichmentString(project["lifecycleState"]) == "ACTIVE" {
			active = append(active, project)
		}
	}
	for _, project := range active {
		value := strings.ToLower(normalizeGeminiEnrichmentString(project["projectId"]) + " " + normalizeGeminiEnrichmentString(project["name"]))
		if strings.Contains(value, "cloud-ai-companion") || strings.Contains(value, "cloud ai companion") || strings.Contains(value, "code assist") {
			return normalizeGeminiEnrichmentString(project["projectId"])
		}
	}
	for _, project := range active {
		value := strings.ToLower(normalizeGeminiEnrichmentString(project["projectId"]) + " " + normalizeGeminiEnrichmentString(project["name"]))
		if strings.Contains(value, "default") {
			return normalizeGeminiEnrichmentString(project["projectId"])
		}
	}
	if len(active) > 0 {
		return normalizeGeminiEnrichmentString(active[0]["projectId"])
	}
	return ""
}

type geminiDriveQuota struct{ limit, usage int64 }

func (e *GeminiOAuthEnricher) fetchDriveStorageQuota(ctx context.Context, input GeminiOAuthEnrichmentInput) (geminiDriveQuota, error) {
	payload, err := e.requestJSON(ctx, input, "drive quota", geminiOAuthGoogleAPIsURL+"/drive/v3/about?fields=storageQuota", http.MethodGet, nil)
	if err != nil {
		return geminiDriveQuota{}, err
	}
	quota, _ := payload["storageQuota"].(map[string]any)
	return geminiDriveQuota{limit: nonNegativeInt64(quota["limit"]), usage: nonNegativeInt64(quota["usage"])}, nil
}

func (e *GeminiOAuthEnricher) requestJSON(ctx context.Context, input GeminiOAuthEnrichmentInput, step, url, method string, body map[string]any) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, wrapGeminiOAuthEnrichmentError(step, 0, err)
	}
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			return nil, wrapGeminiOAuthEnrichmentError(step, 0, err)
		}
	}
	request := GeminiOAuthEnrichmentHTTPRequest{method: method, url: url, body: encoded, proxy: strings.TrimSpace(input.ProxyURL), timeout: geminiOAuthEnrichmentTimeout, header: http.Header{
		"Accept":        []string{"application/json"},
		"Authorization": []string{"Bearer " + input.Secrets.accessToken},
		"Content-Type":  []string{"application/json"},
		"User-Agent":    []string{geminiOAuthCLIUserAgent},
	}}
	response, err := e.executor.ExecuteGeminiOAuthEnrichment(ctx, request)
	if err != nil {
		return nil, wrapGeminiOAuthEnrichmentError(step, 0, err)
	}
	if response.Truncated() || len(response.Body()) > geminiOAuthEnrichmentMaxBody {
		return nil, wrapGeminiOAuthEnrichmentError(step, response.StatusCode(), errors.New("response exceeds bound"))
	}
	if response.StatusCode() < 200 || response.StatusCode() >= 300 {
		return nil, wrapGeminiOAuthEnrichmentError(step, response.StatusCode(), errors.New("non-success response"))
	}
	var decoded any
	if err := json.Unmarshal(response.Body(), &decoded); err != nil {
		// Node's parseJsonRecord keeps malformed success bodies as an opaque raw
		// value. No enrichment branch consumes it, so an empty record is the
		// equivalent result without retaining possibly sensitive response text.
		return map[string]any{}, nil
	}
	payload, _ := decoded.(map[string]any)
	if payload == nil {
		payload = map[string]any{}
	}
	return payload, nil
}

func geminiOAuthEnrichmentSleep(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func normalizeGeminiEnrichmentOAuthType(value string) string {
	switch strings.TrimSpace(value) {
	case "google_one", "ai_studio":
		return strings.TrimSpace(value)
	default:
		return "code_assist"
	}
}

func canonicalGeminiEnrichmentTier(oauthType, raw string) string {
	value := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(raw), "-", "_"))
	if value == "" {
		return ""
	}
	switch oauthType {
	case "google_one":
		switch value {
		case "ai_premium", "google_ai_pro":
			return "google_ai_pro"
		case "google_one_unlimited", "google_ai_ultra":
			return "google_ai_ultra"
		case "google_one_unknown":
			return value
		case "free", "google_one_basic", "google_one_standard", "google_one_free":
			return "google_one_free"
		}
	case "ai_studio":
		if value == "aistudio_paid" || value == "paid" {
			return "aistudio_paid"
		}
		if value == "aistudio_free" || value == "free" {
			return "aistudio_free"
		}
	default:
		switch value {
		case "enterprise", "ultra", "gcp_enterprise", "ultra_tier":
			return "gcp_enterprise"
		case "legacy", "standard", "pro", "gcp_standard", "standard_tier", "pro_tier":
			return "gcp_standard"
		}
	}
	return ""
}

func upstreamGeminiCodeAssistTier(tier string) string {
	if tier == "gcp_enterprise" {
		return "ENTERPRISE"
	}
	return "LEGACY"
}

func codeAssistProject(payload map[string]any) string {
	if project := normalizeGeminiEnrichmentString(payload["cloudaicompanionProject"]); project != "" {
		return project
	}
	if project, ok := payload["cloudaicompanionProject"].(map[string]any); ok {
		return normalizeGeminiEnrichmentString(project["id"])
	}
	return ""
}

func codeAssistTier(payload map[string]any) string {
	paid := codeAssistRawTier(payload["paidTier"])
	current := codeAssistRawTier(payload["currentTier"])
	if paid != "" || current != "" {
		if paid != "" {
			return canonicalGeminiEnrichmentTier("code_assist", paid)
		}
		return canonicalGeminiEnrichmentTier("code_assist", current)
	}
	if allowed, ok := payload["allowedTiers"].([]any); ok {
		var selected map[string]any
		for _, value := range allowed {
			tier, ok := value.(map[string]any)
			if !ok {
				continue
			}
			if selected == nil {
				selected = tier
			}
			if tier["isDefault"] == true {
				selected = tier
				break
			}
		}
		if selected != nil {
			return canonicalGeminiEnrichmentTier("code_assist", normalizeGeminiEnrichmentString(selected["id"]))
		}
	}
	return ""
}

func codeAssistRawTier(value any) string {
	if record, ok := value.(map[string]any); ok {
		return normalizeGeminiEnrichmentString(record["id"])
	}
	return normalizeGeminiEnrichmentString(value)
}

func hasGeminiDriveMetadataScope(scope string) bool {
	for _, value := range strings.Fields(scope) {
		if value == geminiOAuthDriveMetadataScope {
			return true
		}
	}
	return false
}

func inferGeminiGoogleOneTierBytes(storageBytes int64) string {
	if storageBytes <= 0 {
		return "google_one_unknown"
	}
	const gibibyte int64 = 1024 * 1024 * 1024
	const tebibyte int64 = 1024 * gibibyte
	if storageBytes > 100*tebibyte {
		return "google_ai_ultra"
	}
	if storageBytes >= 2*tebibyte {
		return "google_ai_pro"
	}
	if storageBytes >= 15*gibibyte {
		return "google_one_free"
	}
	return "google_one_unknown"
}

func nonNegativeInt64(value any) int64 {
	switch value := value.(type) {
	case json.Number:
		parsed, err := value.Int64()
		if err == nil && parsed >= 0 {
			return parsed
		}
	case float64:
		if value >= 0 && value <= float64(^uint64(0)>>1) {
			return int64(value)
		}
	case string:
		var parsed int64
		if _, err := fmt.Sscan(strings.TrimSpace(value), &parsed); err == nil && parsed >= 0 {
			return parsed
		}
	}
	return 0
}

func normalizeGeminiEnrichmentString(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}
