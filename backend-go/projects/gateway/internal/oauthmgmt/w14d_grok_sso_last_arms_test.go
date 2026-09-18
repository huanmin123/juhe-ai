// w14d_grok_sso_last_arms_test.go covers the verification-GET transport
// error, the default-sleep cancellation arm and the default requester's
// read-error arm of the grok SSO device flow.
package oauthmgmt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestW14dGrokSSOVerificationGETTransportError(t *testing.T) {
	// Two successful steps then a transport failure: the third call is the
	// verification-page GET.
	device := &errAfterSteps{
		steps: []SSODeviceResponse{
			ssoStep(http.StatusOK, nil, "<html>accounts</html>"),
			ssoStep(http.StatusOK, nil, `{"device_code":"dc","user_code":"UC","verification_uri_complete":"https://auth.x.ai/device?user_code=UC","interval":1,"expires_in":600}`),
		},
		err: errBoom(),
	}
	if _, err := convertGrokSSOToOAuth(context.Background(), "sso-token", SSODeviceDeps{
		Request: device,
		Sleep:   func(ctx context.Context, d time.Duration) error { return nil },
		Now:     func() time.Time { return time.UnixMilli(1) },
	}); err == nil || !strings.Contains(err.Error(), "w14d transport boom") {
		t.Fatalf("verification GET transport error: %v", err)
	}
}

func TestW14dDefaultSleepCancellation(t *testing.T) {
	// The default sleep observes context cancellation mid-wait.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	device := &errAfterSteps{steps: []SSODeviceResponse{
		ssoStep(http.StatusOK, nil, "<html>accounts</html>"),
		ssoStep(http.StatusOK, nil, `{"device_code":"dc","user_code":"UC","verification_uri_complete":"https://auth.x.ai/device?user_code=UC","interval":1,"expires_in":600}`),
		ssoStep(http.StatusOK, nil, "<html>device</html>"),
		ssoStep(http.StatusFound, map[string]string{"location": "https://auth.x.ai/oauth2/device/consent"}, ""),
		ssoStep(http.StatusOK, nil, "<html>consent</html>"),
		ssoStep(http.StatusFound, map[string]string{"location": "https://auth.x.ai/oauth2/device/done"}, ""),
		ssoStep(http.StatusOK, nil, "<html>done</html>"),
	}}
	_, err := convertGrokSSOToOAuth(ctx, "sso-token", SSODeviceDeps{Request: device})
	if err == nil || !strings.Contains(err.Error(), "已取消或超时") {
		t.Fatalf("cancelled sleep: %v", err)
	}
}

func TestW14dDefaultRequesterReadError(t *testing.T) {
	// A server that closes the connection mid-body fails the read.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1024")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		flusher.Flush()
		_, _ = w.Write([]byte("short"))
		conn, _, _ := w.(http.Hijacker).Hijack()
		_ = conn.Close()
	}))
	defer server.Close()
	requester := defaultSSODeviceRequester()
	if _, err := requester.Do(context.Background(), SSODeviceRequest{Method: http.MethodGet, URL: server.URL}); err == nil {
		t.Fatal("mid-body close must fail the read")
	}
}
