package modelcheckprobe_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	modelcheckprobe "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprobe"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountprobe"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountquality"
	exactkeyprobe "github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamenvelope"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamidentity"
)

const (
	w1CrossFaceAccountID = "w1-cross-face-account"
	w1CrossFaceModel     = "gpt-5.6-sol"
	w1CrossFaceToken     = "w1-cross-face-token"
	w1CrossFaceBaseURL   = "https://w1-cross-face.example"
	w1CrossFaceSecret    = "w1-cross-face-test-secret"
	w1CaptureSentinel    = "w1 cross-face capture sentinel"
)

type w1CandidateSource struct {
	view *accountprobe.View
}

func (source w1CandidateSource) LoadProbeView(context.Context, accountquality.ProbeRequest) (*accountprobe.View, error) {
	return source.view, nil
}

type w1CapturedTransport struct {
	calls     int
	request   *http.Request
	body      []byte
	roundTrip error
	readErr   error
}

func (transport *w1CapturedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.calls++
	body, err := io.ReadAll(request.Body)
	transport.body = body
	transport.readErr = err
	copy := request.Clone(request.Context())
	if request.URL != nil {
		urlCopy := *request.URL
		copy.URL = &urlCopy
	}
	copy.Header = request.Header.Clone()
	transport.request = copy
	if err != nil {
		transport.roundTrip = err
		return nil, err
	}
	transport.roundTrip = errors.New(w1CaptureSentinel)
	return nil, transport.roundTrip
}

type w1CapturedFace struct {
	name    string
	request *http.Request
	body    map[string]any
}

func TestW1EnvelopeCrossFaceOpenAIResponses(t *testing.T) {
	// All faces share the same account target, model, endpoint mode and budget
	// fixture. Probe challenge/input and randomized instruction contents are
	// intentionally ignored; identity UUIDs are checked by their relationships,
	// not compared literally across independently generated requests. C.store is
	// asserted per face because it remains probe-family semantic input.
	t.Run("A OAuth Codex", func(t *testing.T) {
		faces := []w1CapturedFace{
			w1RunF2(t, "A"),
			w1RunF3(t, "A"),
			w1RunF4(t, "A"),
		}
		for _, face := range faces {
			w1AssertFaceShape(t, face, "A")
		}
		w1AssertSharedFaceShape(t, faces)
	})

	t.Run("B codex_responses API Key", func(t *testing.T) {
		// F3 Execute currently has no codex_responses API-key input arm.
		faces := []w1CapturedFace{
			w1RunF2(t, "B"),
			w1RunF4(t, "B"),
		}
		for _, face := range faces {
			w1AssertFaceShape(t, face, "B")
		}
		w1AssertSharedFaceShape(t, faces)
	})

	t.Run("C plain API Key", func(t *testing.T) {
		faces := []w1CapturedFace{
			w1RunF2(t, "C"),
			w1RunF3(t, "C"),
			w1RunF4(t, "C"),
		}
		for _, face := range faces {
			w1AssertFaceShape(t, face, "C")
		}
		w1AssertSharedFaceShape(t, faces)
	})
}

func w1RunF2(t *testing.T, form string) w1CapturedFace {
	t.Helper()
	transport := &w1CapturedTransport{}
	view := w1F2View(form)
	service, err := accountprobe.NewService(accountprobe.Options{
		Source:        w1CandidateSource{view: view},
		Client:        &http.Client{Transport: transport},
		Secret:        w1CrossFaceSecret,
		RetryTimeouts: []time.Duration{time.Second},
	})
	if err != nil {
		t.Fatalf("F2 NewService: %v", err)
	}
	observation, _, err := service.ManualDiagnostics(context.Background(), view, false)
	if err != nil {
		t.Fatalf("F2 ManualDiagnostics: %v", err)
	}
	if observation == nil || observation.Result.Success || !observation.Evidence.HasRealUpstreamAttempt {
		t.Fatalf("F2 sentinel failure must remain a failed real-attempt observation: observation=%+v", observation)
	}
	return w1CapturedRequest(t, "F2", transport)
}

func w1F2View(form string) *accountprobe.View {
	view := &accountprobe.View{
		AccountID:                 w1CrossFaceAccountID,
		AccountName:               w1CrossFaceAccountID,
		Type:                      "api_key",
		Status:                    "active",
		ProviderCode:              "openai",
		ProtocolCode:              "openai",
		ProtocolVersion:           "v1",
		ProviderProtocolProfileID: "profile_openai_openai_v1",
		HealthCheckModel:          w1CrossFaceModel,
		HealthCheckEndpointMode:   string(accountprobe.ModeResponsesSSE),
		SupportedModels:           []string{w1CrossFaceModel},
		BaseURL:                   w1CrossFaceBaseURL,
		Credentials:               map[string]any{},
		SelectedAPIKey:            w1CrossFaceToken,
		NormalizeEndpointModes:    map[accountprobe.EndpointMode]bool{accountprobe.ModeResponsesSSE: true},
	}
	switch form {
	case "A":
		view.Type = "oauth"
		view.ProviderProtocolProfileID = "profile_gpt_openai_v1"
		view.Credentials = map[string]any{"account_id": w1CrossFaceAccountID}
	case "B":
		view.ClientCompatibility = "codex_responses"
		view.APIKeyEntries = []accountprobe.KeyEntry{{Key: w1CrossFaceToken, Index: 0}}
	case "C":
		view.APIKeyEntries = []accountprobe.KeyEntry{{Key: w1CrossFaceToken, Index: 0}}
	default:
		panic("unsupported W1 cross-face form: " + form)
	}
	return view
}

func w1RunF3(t *testing.T, form string) w1CapturedFace {
	t.Helper()
	transport := &w1CapturedTransport{}
	var request modelcheckprobe.Request
	var err error
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+w1CrossFaceToken)
	options := modelcheckprobe.Options{
		Endpoint:          w1CrossFaceBaseURL,
		Headers:           headers,
		Client:            &http.Client{Transport: transport},
		Timeout:           time.Second,
		ProtocolProfileID: "profile_openai_openai_v1",
		CredentialType:    "api_key",
	}
	switch form {
	case "A":
		request, err = modelcheckprobe.BuildOpenAIOAuthCodexBasic(w1CrossFaceModel, "w1 cross-face probe challenge", true)
		options.Adapter = modelcheckprobe.AdapterOpenAIOAuthCodex
		options.ProtocolProfileID = "profile_gpt_openai_v1"
		options.CredentialType = "oauth"
		headers.Set("Chatgpt-Account-Id", w1CrossFaceAccountID)
	case "C":
		request, err = modelcheckprobe.BuildBasicForEndpointMode(
			modelcheckprofile.ProtocolOpenAIResponses,
			w1CrossFaceModel,
			"w1 cross-face probe challenge",
			string(modelcheckprofile.EndpointModeResponsesSSE),
		)
	default:
		t.Fatalf("F3 has no configured cross-face request for form %q", form)
	}
	if err != nil {
		t.Fatalf("F3 build %s request: %v", form, err)
	}
	result, err := modelcheckprobe.Execute(context.Background(), request, options)
	if err != nil || result.Success || !strings.Contains(result.ErrorMessage, w1CaptureSentinel) {
		t.Fatalf("F3 sentinel failure must remain visible: result=%+v err=%v", result, err)
	}
	return w1CapturedRequest(t, "F3", transport)
}

func w1RunF4(t *testing.T, form string) w1CapturedFace {
	t.Helper()
	transport := &w1CapturedTransport{}
	now := time.Now().UTC()
	credentialJSON := `{"api_key":"` + w1CrossFaceToken + `"}`
	credentialKind := "api_key"
	profile := "profile_openai_openai_v1"
	typeName := "api_key"
	input := exactkeyprobe.Input{
		AccountID:            w1CrossFaceAccountID,
		InputVersion:         1,
		ConfigRevision:       1,
		DispatchRevision:     1,
		Provider:             "openai",
		ProtocolProfileID:    profile,
		ProtocolCode:         "openai",
		ProtocolVersion:      "v1",
		Type:                 typeName,
		EndpointMode:         "responses_sse",
		HealthModel:          w1CrossFaceModel,
		BaseURL:              w1CrossFaceBaseURL,
		IssuedAt:             now,
		ExpiresAt:            now.Add(time.Hour),
		TLSPolicyVersion:     "w1-cross-face-test",
		AllowInsecureBaseURL: false,
	}
	switch form {
	case "A":
		credentialJSON = `{"access_token":"` + w1CrossFaceToken + `"}`
		credentialKind = "oauth_access"
		profile = "profile_gpt_openai_v1"
		input.ProtocolProfileID = profile
		input.Type = "oauth"
		input.OAuthAccountID = w1CrossFaceAccountID
		tokenExpires := now.Add(time.Hour)
		input.OAuthExpiresAt = &tokenExpires
	case "B":
		input.ClientCompatibility = "codex_responses"
	case "C":
	default:
		t.Fatalf("unsupported F4 cross-face form %q", form)
	}
	ciphertext, err := exactkeyprobe.EncryptV1Envelope(w1CrossFaceSecret, []byte(credentialJSON))
	if err != nil {
		t.Fatalf("F4 EncryptV1Envelope: %v", err)
	}
	result := exactkeyprobe.ProbeOpenAI(context.Background(), input, exactkeyprobe.CredentialEnvelope{
		Kind:       credentialKind,
		Ciphertext: ciphertext,
	}, exactkeyprobe.ProbeOptions{
		Client:  &http.Client{Transport: transport},
		Secret:  w1CrossFaceSecret,
		Timeout: time.Second,
		Now:     func() time.Time { return now },
	})
	if result.Outcome != exactkeyprobe.OutcomeUpstreamFailed || result.ErrorCode == "" {
		t.Fatalf("F4 sentinel transport failure must remain failed: result=%+v", result)
	}
	return w1CapturedRequest(t, "F4", transport)
}

func w1CapturedRequest(t *testing.T, name string, transport *w1CapturedTransport) w1CapturedFace {
	t.Helper()
	if transport.calls != 1 {
		t.Fatalf("%s calls=%d, want exactly one captured call", name, transport.calls)
	}
	if transport.request == nil {
		t.Fatalf("%s did not capture an HTTP request (transport error=%v)", name, transport.roundTrip)
	}
	if transport.readErr != nil {
		t.Fatalf("%s request body read failed: %v", name, transport.readErr)
	}
	if transport.roundTrip == nil || !strings.Contains(transport.roundTrip.Error(), w1CaptureSentinel) {
		t.Fatalf("%s did not preserve capture sentinel: %v", name, transport.roundTrip)
	}
	var body map[string]any
	if err := json.Unmarshal(transport.body, &body); err != nil {
		t.Fatalf("%s request body is not a JSON object: %v", name, err)
	}
	return w1CapturedFace{name: name, request: transport.request, body: body}
}

func w1AssertFaceShape(t *testing.T, face w1CapturedFace, form string) {
	t.Helper()
	request := face.request
	body := face.body
	// v2 直拼：base 无版本根时不再补 /v1（B/C 形态出站=base+catalog 模板；
	// A 形态 Codex 固定上游不变）。
	wantURL := w1CrossFaceBaseURL + "/responses"
	wantAccept := "application/json, text/event-stream"
	wantBeta := ""
	wantBudget := true
	wantIdentity := false
	switch form {
	case "A":
		wantURL = upstreamenvelope.CodexBaseURL + "/responses"
		wantAccept = "text/event-stream"
		wantBeta = "responses=experimental"
		wantBudget = false
		wantIdentity = true
	case "B":
		wantIdentity = true
	case "C":
	default:
		t.Fatalf("unknown cross-face form %q", form)
	}
	if request.Method != http.MethodPost || request.URL.String() != wantURL || request.URL.RawQuery != "" {
		t.Fatalf("%s %s URL/method/query = %s %q %q", form, face.name, request.URL, request.Method, request.URL.RawQuery)
	}
	for header, want := range map[string]string{
		"Accept":        wantAccept,
		"Authorization": "Bearer " + w1CrossFaceToken,
		"Content-Type":  "application/json",
		"Openai-Beta":   wantBeta,
	} {
		if got := request.Header.Get(header); got != want {
			t.Fatalf("%s %s header %s=%q, want %q", form, face.name, header, got, want)
		}
	}
	if form == "A" {
		if got := request.Header.Get("Chatgpt-Account-Id"); got != w1CrossFaceAccountID {
			t.Fatalf("A %s Chatgpt-Account-Id=%q", face.name, got)
		}
	} else if got := request.Header.Get("Chatgpt-Account-Id"); got != "" {
		t.Fatalf("%s %s unexpected Chatgpt-Account-Id=%q", form, face.name, got)
	}
	if body["model"] != w1CrossFaceModel || body["stream"] != true {
		t.Fatalf("%s %s model/stream = %#v/%#v", form, face.name, body["model"], body["stream"])
	}
	if body["input"] == nil {
		t.Fatalf("%s %s missing semantic input", form, face.name)
	}
	if _, ok := body["instructions"].(string); !ok {
		t.Fatalf("%s %s instructions is not a string: %#v", form, face.name, body["instructions"])
	}
	if _, exists := body["max_tokens"]; exists {
		t.Fatalf("%s %s unexpected max_tokens: %#v", form, face.name, body["max_tokens"])
	}
	budget, hasBudget := body["max_output_tokens"]
	if hasBudget != wantBudget || (wantBudget && budget != float64(upstreamenvelope.DefaultProbeOutputTokens)) {
		t.Fatalf("%s %s max_output_tokens=%#v present=%v, want present=%v value=%d", form, face.name, budget, hasBudget, wantBudget, upstreamenvelope.DefaultProbeOutputTokens)
	}
	store, hasStore := body["store"]
	wantStore := form == "A" || form == "B" || (form == "C" && face.name == "F3")
	if hasStore != wantStore || (hasStore && store != false) {
		t.Fatalf("%s %s store=%#v present=%v, want false/present=%v", form, face.name, store, hasStore, wantStore)
	}
	if wantIdentity {
		w1AssertCodexIdentity(t, face, form == "B")
		if got := request.Header.Get("X-OpenAI-Internal-Codex-Responses-Lite"); got != "true" {
			t.Fatalf("%s %s Lite header=%q, want true", form, face.name, got)
		}
	} else {
		for _, header := range []string{"Session-Id", "Thread-Id", "X-Client-Request-Id", "X-Codex-Turn-Metadata", "X-OpenAI-Internal-Codex-Responses-Lite"} {
			if got := request.Header.Get(header); got != "" {
				t.Fatalf("C %s must not receive Codex header %s=%q", face.name, header, got)
			}
		}
		for _, field := range []string{"client_metadata", "prompt_cache_key", "include", "reasoning", "parallel_tool_calls"} {
			if _, exists := body[field]; exists {
				t.Fatalf("C %s must not receive Codex body field %s: %#v", face.name, field, body[field])
			}
		}
	}
	if form == "A" || form == "B" {
		reasoning, ok := body["reasoning"].(map[string]any)
		if !ok || reasoning["context"] != "all_turns" || body["parallel_tool_calls"] != false {
			t.Fatalf("%s %s reasoning contract mismatch: %#v", form, face.name, body)
		}
	}
	if form == "B" {
		include, ok := body["include"].([]any)
		if !ok || !reflect.DeepEqual(include, []any{"reasoning.encrypted_content"}) {
			t.Fatalf("B %s include=%#v", face.name, body["include"])
		}
		if got := body["prompt_cache_key"]; got != request.Header.Get("Session-Id") {
			t.Fatalf("B %s prompt_cache_key=%v session=%q", face.name, got, request.Header.Get("Session-Id"))
		}
	}
}

func w1AssertCodexIdentity(t *testing.T, face w1CapturedFace, withBodyMetadata bool) {
	t.Helper()
	header := face.request.Header
	session := header.Get("Session-Id")
	if session == "" {
		t.Fatalf("%s missing Session-Id", face.name)
	}
	for name, want := range map[string]string{
		"Originator":            "codex_exec",
		"User-Agent":            upstreamidentity.EffectiveCodexUserAgent(),
		"Thread-Id":             session,
		"X-Client-Request-Id":   session,
		"X-Codex-Window-Id":     session + ":0",
		"X-Codex-Beta-Features": "remote_compaction_v2",
	} {
		if got := header.Get(name); got != want {
			t.Fatalf("%s %s=%q, want %q", face.name, name, got, want)
		}
	}
	turnMetadataJSON := header.Get("X-Codex-Turn-Metadata")
	var turnMetadata map[string]any
	if err := json.Unmarshal([]byte(turnMetadataJSON), &turnMetadata); err != nil {
		t.Fatalf("%s X-Codex-Turn-Metadata invalid JSON: %v", face.name, err)
	}
	installation, _ := turnMetadata["installation_id"].(string)
	if installation == "" {
		t.Fatalf("%s turn metadata has no installation_id: %#v", face.name, turnMetadata)
	}
	headerInstallation := header.Get("X-Codex-Installation-Id")
	if face.name == "F3" {
		if headerInstallation != installation {
			t.Fatalf("F3 X-Codex-Installation-Id=%q, want turn metadata installation_id %q", headerInstallation, installation)
		}
	} else if headerInstallation != "" {
		t.Fatalf("%s must keep the shared eight-header contract; unexpected X-Codex-Installation-Id=%q", face.name, headerInstallation)
	}
	if turnMetadata["session_id"] != session ||
		turnMetadata["thread_id"] != session || turnMetadata["window_id"] != session+":0" {
		t.Fatalf("%s turn metadata identity differs from headers: metadata=%#v headers=%v", face.name, turnMetadata, header)
	}
	if turnID, _ := turnMetadata["turn_id"].(string); turnID == "" {
		t.Fatalf("%s turn metadata has no turn_id: %#v", face.name, turnMetadata)
	}
	for _, key := range []string{"request_kind", "thread_source", "sandbox", "turn_started_at_unix_ms"} {
		if _, exists := turnMetadata[key]; !exists {
			t.Fatalf("%s turn metadata missing %s: %#v", face.name, key, turnMetadata)
		}
	}
	if withBodyMetadata {
		metadata, ok := face.body["client_metadata"].(map[string]any)
		if !ok {
			t.Fatalf("%s missing client_metadata: %#v", face.name, face.body["client_metadata"])
		}
		want := map[string]any{
			"session_id":              session,
			"thread_id":               header.Get("Thread-Id"),
			"x-codex-window-id":       header.Get("X-Codex-Window-Id"),
			"x-codex-turn-metadata":   turnMetadataJSON,
			"x-codex-installation-id": installation,
			"turn_id":                 turnMetadata["turn_id"],
		}
		for key, expected := range want {
			if metadata[key] != expected {
				t.Fatalf("%s client_metadata[%s]=%v, want %v", face.name, key, metadata[key], expected)
			}
		}
		if len(metadata) != len(want) {
			t.Fatalf("%s client_metadata keys=%v, want exactly %d identity fields", face.name, metadata, len(want))
		}
	}
}

func w1AssertSharedFaceShape(t *testing.T, faces []w1CapturedFace) {
	t.Helper()
	if len(faces) < 2 {
		return
	}
	first := faces[0]
	for _, face := range faces[1:] {
		if face.request.Method != first.request.Method || face.request.URL.String() != first.request.URL.String() || face.request.URL.RawQuery != first.request.URL.RawQuery {
			t.Fatalf("cross-face URL/method/query differ: %s=%s %s=%s", first.name, first.request.URL, face.name, face.request.URL)
		}
		for _, name := range []string{"Accept", "Authorization", "Content-Type", "Openai-Beta", "Chatgpt-Account-Id"} {
			if face.request.Header.Get(name) != first.request.Header.Get(name) {
				t.Fatalf("cross-face %s differs: %s=%q %s=%q", name, first.name, first.request.Header.Get(name), face.name, face.request.Header.Get(name))
			}
		}
		for _, name := range []string{"model", "stream", "max_output_tokens", "reasoning", "parallel_tool_calls", "include"} {
			if !reflect.DeepEqual(face.body[name], first.body[name]) {
				t.Fatalf("cross-face body field %s differs: %s=%#v %s=%#v", name, first.name, first.body[name], face.name, face.body[name])
			}
		}
		if _, firstHas := first.body["max_output_tokens"]; firstHas != hasBodyField(face.body, "max_output_tokens") {
			t.Fatalf("cross-face budget field presence differs: %s=%#v %s=%#v", first.name, first.body, face.name, face.body)
		}
	}
}

func hasBodyField(body map[string]any, key string) bool {
	_, exists := body[key]
	return exists
}
