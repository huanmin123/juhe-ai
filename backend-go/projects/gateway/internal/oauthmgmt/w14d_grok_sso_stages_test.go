// w14d_grok_sso_stages_test.go covers the remaining grok SSO device-flow
// transport-error stages (device, verification page, verify, approve, poll)
// and the poll detail-less branches that the shared fixtures leave open.
package oauthmgmt

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func errBoom() error { return errors.New("w14d transport boom") }

// errAfterSteps fails once the scripted steps run out.
type errAfterSteps struct {
	steps  []SSODeviceResponse
	err    error
	cursor int
}

func (e *errAfterSteps) Do(context.Context, SSODeviceRequest) (SSODeviceResponse, error) {
	if e.cursor < len(e.steps) {
		step := e.steps[e.cursor]
		e.cursor++
		return step, nil
	}
	return SSODeviceResponse{}, e.err
}

func TestW14dGrokSSOStageTransportErrors(t *testing.T) {
	boom := errBoom()
	pre := []SSODeviceResponse{
		ssoStep(http.StatusOK, nil, "<html>accounts</html>"),
	}
	throughDevice := append([]SSODeviceResponse{
		ssoStep(http.StatusOK, nil, "<html>accounts</html>"),
		ssoStep(http.StatusOK, nil, `{"device_code":"dc","user_code":"UC","verification_uri_complete":"https://auth.x.ai/device?user_code=UC","interval":1,"expires_in":600}`),
		ssoStep(http.StatusOK, nil, "<html>device</html>"),
		ssoStep(http.StatusFound, map[string]string{"location": "https://auth.x.ai/oauth2/device/consent"}, ""),
	}, ssoStep(http.StatusOK, nil, "<html>consent</html>"))

	// The device stage fails once the accounts answer succeeds.
	device := &errAfterSteps{steps: pre, err: boom}
	if _, err := convertGrokSSOToOAuth(context.Background(), "sso-token", SSODeviceDeps{
		Request: device,
		Sleep:   func(context.Context, time.Duration) error { return nil },
		Now:     func() time.Time { return time.UnixMilli(1) },
	}); err == nil {
		t.Fatal("device stage must fail on transport error")
	}
	// The verification-page stage fails after the device answer.
	verification := &errAfterSteps{steps: throughDevice, err: boom}
	if _, err := convertGrokSSOToOAuth(context.Background(), "sso-token", SSODeviceDeps{
		Request: verification,
		Sleep:   func(context.Context, time.Duration) error { return nil },
		Now:     func() time.Time { return time.UnixMilli(1) },
	}); err == nil {
		t.Fatal("verification stage must fail on transport error")
	}

	// The verification GET stage fails after the device answer.
	verificationGET := &errAfterSteps{steps: []SSODeviceResponse{
		ssoStep(http.StatusOK, nil, "<html>accounts</html>"),
		ssoStep(http.StatusOK, nil, `{"device_code":"dc","user_code":"UC","verification_uri_complete":"https://auth.x.ai/device?user_code=UC","interval":1,"expires_in":600}`),
		ssoStep(http.StatusOK, nil, "<html>device</html>"),
	}, err: boom}
	if _, err := convertGrokSSOToOAuth(context.Background(), "sso-token", SSODeviceDeps{
		Request: verificationGET,
		Sleep:   func(context.Context, time.Duration) error { return nil },
		Now:     func() time.Time { return time.UnixMilli(1) },
	}); err == nil {
		t.Fatal("verification GET stage must fail on transport error")
	}

	// The verify POST stage fails after the verification page.
	verifyPOST := &errAfterSteps{steps: []SSODeviceResponse{
		ssoStep(http.StatusOK, nil, "<html>accounts</html>"),
		ssoStep(http.StatusOK, nil, `{"device_code":"dc","user_code":"UC","verification_uri_complete":"https://auth.x.ai/device?user_code=UC","interval":1,"expires_in":600}`),
		ssoStep(http.StatusOK, nil, "<html>device</html>"),
		ssoStep(http.StatusFound, map[string]string{"location": "https://auth.x.ai/oauth2/device/consent"}, ""),
		ssoStep(http.StatusOK, nil, "<html>consent</html>"),
	}, err: boom}
	if _, err := convertGrokSSOToOAuth(context.Background(), "sso-token", SSODeviceDeps{
		Request: verifyPOST,
		Sleep:   func(context.Context, time.Duration) error { return nil },
		Now:     func() time.Time { return time.UnixMilli(1) },
	}); err == nil {
		t.Fatal("verify POST stage must fail on transport error")
	}

	// A missing done page renders the dedicated diagnostic.
	missingDone := &wcScriptedDevice{steps: []SSODeviceResponse{
		ssoStep(http.StatusOK, nil, "<html>accounts</html>"),
		ssoStep(http.StatusOK, nil, `{"device_code":"dc","user_code":"UC","verification_uri_complete":"https://auth.x.ai/device?user_code=UC","interval":1,"expires_in":600}`),
		ssoStep(http.StatusOK, nil, "<html>device</html>"),
		ssoStep(http.StatusFound, map[string]string{"location": "https://auth.x.ai/oauth2/device/consent"}, ""),
		ssoStep(http.StatusOK, nil, "<html>consent</html>"),
		ssoStep(http.StatusFound, map[string]string{"location": "https://auth.x.ai/oauth2/device/consent"}, ""),
		ssoStep(http.StatusOK, nil, "<html>consent-again</html>"),
	}}
	if _, err := convertGrokSSOToOAuth(context.Background(), "sso-token", SSODeviceDeps{
		Request: missingDone,
		Sleep:   func(context.Context, time.Duration) error { return nil },
		Now:     func() time.Time { return time.UnixMilli(1) },
	}); err == nil || err.Error() != "xAI device 授权未进入 done 页面" {
		t.Fatalf("missing done: %v", err)
	}

	// The approve/poll stage fails after verify enters consent.
	approve := &errAfterSteps{
		steps: []SSODeviceResponse{
			ssoStep(http.StatusOK, nil, "<html>accounts</html>"),
			ssoStep(http.StatusOK, nil, `{"device_code":"dc","user_code":"UC","verification_uri_complete":"https://auth.x.ai/device?user_code=UC","interval":1,"expires_in":600}`),
			ssoStep(http.StatusOK, nil, "<html>device</html>"),
			ssoStep(http.StatusFound, map[string]string{"location": "https://auth.x.ai/oauth2/device/consent"}, ""),
			ssoStep(http.StatusOK, nil, "<html>consent</html>"),
			ssoStep(http.StatusFound, map[string]string{"location": "https://auth.x.ai/oauth2/device/done"}, ""),
			ssoStep(http.StatusOK, nil, "<html>done</html>"),
		},
		err: boom,
	}
	if _, err := convertGrokSSOToOAuth(context.Background(), "sso-token", SSODeviceDeps{
		Request: approve,
		Sleep:   func(context.Context, time.Duration) error { return nil },
		Now:     func() time.Time { return time.UnixMilli(1) },
	}); err == nil {
		t.Fatal("poll stage transport failure must surface")
	}
}

func TestW14dGrokSSOPollDetailArms(t *testing.T) {
	clock := &wcClock{nowMs: 1_000_000}
	// A detail-less non-2xx error renders the status suffix. The token poll
	// starts after the seven pre-poll steps.
	prePoll := wcDeviceSuccessSteps(`{"access_token":"x","expires_in":60}`)[:7]
	_, err := wcConvert(t, append(append([]SSODeviceResponse{}, prePoll...),
		ssoStep(http.StatusServiceUnavailable, nil, `{"error":"mystery"}`)), clock)
	if err == nil || !containsText(err.Error(), "HTTP 503") {
		t.Fatalf("detail-less poll error: %v", err)
	}
	// A 2xx answer without an error code renders the bare failure.
	_, err = wcConvert(t, append(append([]SSODeviceResponse{}, prePoll...),
		ssoStep(http.StatusAccepted, nil, `{"unrelated":true}`)), clock)
	if err == nil || !containsText(err.Error(), "HTTP 202") {
		t.Fatalf("2xx no-token poll error: %v", err)
	}
}

func containsText(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
