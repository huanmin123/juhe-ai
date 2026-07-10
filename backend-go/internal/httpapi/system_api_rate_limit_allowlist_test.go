package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	"juhe-ai/backend-go/internal/store/port"
)

func TestRouterSystemAPIRateLimitClientIPAllowlist(t *testing.T) {
	tests := []struct {
		name               string
		allowlisted        bool
		allowlistErr       error
		ipBlocked          bool
		wantStatus         int
		wantRetryAfter     string
		wantAllowlistCalls int
		wantIPLimiterCalls int
		wantUserCalls      int
	}{
		{
			name:               "allowlisted client bypasses both layers",
			allowlisted:        true,
			wantStatus:         http.StatusOK,
			wantAllowlistCalls: 1,
		},
		{
			name:               "non allowlisted client uses both layers",
			wantStatus:         http.StatusOK,
			wantAllowlistCalls: 1,
			wantIPLimiterCalls: 1,
			wantUserCalls:      1,
		},
		{
			name:               "allowlist read failure continues limiting",
			allowlistErr:       errors.New("postgres unavailable"),
			wantStatus:         http.StatusOK,
			wantAllowlistCalls: 2,
			wantIPLimiterCalls: 1,
			wantUserCalls:      1,
		},
		{
			name:               "allowlist read failure still enforces IP limit",
			allowlistErr:       errors.New("postgres unavailable"),
			ipBlocked:          true,
			wantStatus:         http.StatusTooManyRequests,
			wantRetryAfter:     "7",
			wantAllowlistCalls: 1,
			wantIPLimiterCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allowlistReader := &systemAPIClientIPAllowlistReaderStub{
				allowlisted: tt.allowlisted,
				err:         tt.allowlistErr,
			}
			ipDecision := SystemAPIRateLimitDecision{Allowed: true}
			if tt.ipBlocked {
				ipDecision = SystemAPIRateLimitDecision{RetryAfterSeconds: 7}
			}
			ipLimiter := &publicSettingsRateLimiterStub{decision: ipDecision}
			userLimiter := &systemAPIAuthenticatedRateLimiterStub{
				decision: SystemAPIRateLimitDecision{Allowed: true},
			}
			authenticator := &managementAPIAuthenticatorStub{
				context: managementauth.Context{
					SystemAccountID: "sys_user",
					Username:        "user",
					Role:            "user",
					SessionID:       "sess_user",
				},
			}
			router := NewRouter(RouterOptions{
				Config: config.Config{
					Host:                 "127.0.0.1",
					Port:                 3000,
					ManagementAPIEnabled: true,
				},
				Logger:                            slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
				SystemAPIRateLimitReader:          systemAPIRateLimitReaderStub{settings: port.SystemAPIRateLimitSettings{IPReadPerMinute: 600, IPReadBurstPer10Seconds: 120, UserReadPerMinute: 300}},
				SystemAPIClientIPAllowlistReader:  allowlistReader,
				SystemAPIIPRateLimiter:            ipLimiter,
				SystemAPIAuthenticatedRateLimiter: userLimiter,
				ManagementAPIAuthMiddleware:       NewManagementAPIAuthMiddleware(authenticator),
				ManagementProvidersHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					writeData(w, http.StatusOK, []string{})
				}),
			})

			req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/providers", nil)
			req.RemoteAddr = "203.0.113.9:43123"
			req.Header.Set("Cookie", "juhe_ai_session=session-token")
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if retryAfter := rec.Header().Get("Retry-After"); retryAfter != tt.wantRetryAfter {
				t.Fatalf("Retry-After = %q, want %q", retryAfter, tt.wantRetryAfter)
			}
			if allowlistReader.calls != tt.wantAllowlistCalls {
				t.Fatalf("allowlist calls = %d, want %d", allowlistReader.calls, tt.wantAllowlistCalls)
			}
			if ipLimiter.calls != tt.wantIPLimiterCalls {
				t.Fatalf("IP limiter calls = %d, want %d", ipLimiter.calls, tt.wantIPLimiterCalls)
			}
			if userLimiter.calls != tt.wantUserCalls {
				t.Fatalf("user limiter calls = %d, want %d", userLimiter.calls, tt.wantUserCalls)
			}
			if tt.allowlistErr == nil && allowlistReader.lastIPHash != "1238ae70e54c7b3a7b287d070d543bf2ad7288a734688f5cad1cdfb44d9a76eb" {
				t.Fatalf("allowlist ip hash = %q", allowlistReader.lastIPHash)
			}
		})
	}
}

func TestRouterSystemAPIRateLimitAllowlistDoesNotBypassAuthentication(t *testing.T) {
	allowlistReader := &systemAPIClientIPAllowlistReaderStub{allowlisted: true}
	ipLimiter := &publicSettingsRateLimiterStub{
		decision: SystemAPIRateLimitDecision{Allowed: true},
	}
	userLimiter := &systemAPIAuthenticatedRateLimiterStub{
		decision: SystemAPIRateLimitDecision{Allowed: true},
	}
	handlerCalls := 0
	router := NewRouter(RouterOptions{
		Config: config.Config{
			Host:                 "127.0.0.1",
			Port:                 3000,
			ManagementAPIEnabled: true,
		},
		Logger:                            slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		SystemAPIRateLimitReader:          systemAPIRateLimitReaderStub{settings: port.SystemAPIRateLimitSettings{IPReadPerMinute: 600, IPReadBurstPer10Seconds: 120, UserReadPerMinute: 300}},
		SystemAPIClientIPAllowlistReader:  allowlistReader,
		SystemAPIIPRateLimiter:            ipLimiter,
		SystemAPIAuthenticatedRateLimiter: userLimiter,
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			err: &managementauth.AuthError{StatusCode: http.StatusUnauthorized, Message: "请先登录"},
		}),
		ManagementProvidersHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			handlerCalls++
			writeData(w, http.StatusOK, []string{})
		}),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/providers", nil)
	req.RemoteAddr = "203.0.113.9:43123"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body = %s", rec.Code, rec.Body.String())
	}
	if allowlistReader.calls != 1 || ipLimiter.calls != 0 || userLimiter.calls != 0 || handlerCalls != 0 {
		t.Fatalf(
			"allowlist=%d ip=%d user=%d handler=%d",
			allowlistReader.calls,
			ipLimiter.calls,
			userLimiter.calls,
			handlerCalls,
		)
	}
}

func TestSystemAPIClientIPPolicyHashMatchesNodeIPv4Contract(t *testing.T) {
	hash, ok := systemAPIClientIPPolicyHash("203.0.113.9")
	if !ok {
		t.Fatal("IPv4 hash should be available")
	}
	if hash != "1238ae70e54c7b3a7b287d070d543bf2ad7288a734688f5cad1cdfb44d9a76eb" {
		t.Fatalf("hash = %q", hash)
	}
	for _, value := range []string{"unknown", "2001:db8::1", ""} {
		if hash, ok := systemAPIClientIPPolicyHash(value); ok || hash != "" {
			t.Fatalf("hash(%q) = %q, %v", value, hash, ok)
		}
	}
}

func TestSystemAPIClientIPAllowlistInspectorCachesForThirtySeconds(t *testing.T) {
	reader := &systemAPIClientIPAllowlistReaderStub{allowlisted: true}
	inspector := newSystemAPIClientIPAllowlistInspector(reader, nil)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	for _, at := range []time.Time{now, now.Add(29 * time.Second), now.Add(30 * time.Second)} {
		allowlisted, err := inspector.allowlisted(context.Background(), "203.0.113.9", at)
		if err != nil {
			t.Fatalf("allowlisted at %s: %v", at, err)
		}
		if !allowlisted {
			t.Fatalf("allowlisted at %s = false", at)
		}
	}
	if reader.calls != 2 {
		t.Fatalf("reader calls = %d, want 2", reader.calls)
	}
}

func TestSystemAPIClientIPAllowlistInspectorDoesNotCachePastPolicyExpiry(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(time.Second)
	reader := &systemAPIClientIPAllowlistReaderStub{
		allowlisted: true,
		expiresAt:   &expiresAt,
	}
	inspector := newSystemAPIClientIPAllowlistInspector(reader, nil)

	for _, at := range []time.Time{now, now.Add(500 * time.Millisecond)} {
		allowlisted, err := inspector.allowlisted(context.Background(), "203.0.113.9", at)
		if err != nil {
			t.Fatalf("allowlisted at %s: %v", at, err)
		}
		if !allowlisted {
			t.Fatalf("allowlisted at %s = false", at)
		}
	}
	allowlisted, err := inspector.allowlisted(context.Background(), "203.0.113.9", expiresAt)
	if err != nil {
		t.Fatalf("allowlisted at expiry: %v", err)
	}
	if allowlisted {
		t.Fatal("allowlisted at expiry = true")
	}
	if reader.calls != 2 {
		t.Fatalf("reader calls = %d, want 2", reader.calls)
	}
}

func TestSystemAPIClientIPAllowlistInspectorInvalidatesOnSharedVersionChange(t *testing.T) {
	reader := &systemAPIClientIPAllowlistReaderStub{allowlisted: true}
	versionReader := &systemAPIClientIPAllowlistVersionReaderStub{
		versions: []string{"version-a", "version-a", "version-b"},
	}
	inspector := newSystemAPIClientIPAllowlistInspector(reader, versionReader)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	for attempt := 0; attempt < 3; attempt++ {
		allowlisted, err := inspector.allowlisted(
			context.Background(),
			"203.0.113.9",
			now.Add(time.Duration(attempt)*time.Second),
		)
		if err != nil {
			t.Fatalf("attempt %d allowlisted: %v", attempt+1, err)
		}
		if !allowlisted {
			t.Fatalf("attempt %d allowlisted = false", attempt+1)
		}
	}
	if reader.calls != 2 {
		t.Fatalf("reader calls = %d, want 2", reader.calls)
	}
}

func TestSystemAPIClientIPAllowlistInspectorVersionFailureSkipsPolicyRead(t *testing.T) {
	reader := &systemAPIClientIPAllowlistReaderStub{allowlisted: true}
	versionReader := &systemAPIClientIPAllowlistVersionReaderStub{err: errors.New("redis unavailable")}
	inspector := newSystemAPIClientIPAllowlistInspector(reader, versionReader)

	allowlisted, err := inspector.allowlisted(context.Background(), "203.0.113.9", time.Now())
	if err == nil || allowlisted {
		t.Fatalf("allowlisted = %v, err = %v", allowlisted, err)
	}
	if reader.calls != 0 {
		t.Fatalf("policy reader calls = %d, want 0", reader.calls)
	}
}

func TestRedisSystemAPIClientIPAllowlistVersionReader(t *testing.T) {
	tests := []struct {
		name        string
		raw         []byte
		err         error
		wantVersion string
		wantErr     bool
	}{
		{name: "value", raw: []byte(" version-a "), wantVersion: "version-a"},
		{name: "missing", err: redisplatform.ErrNotFound},
		{name: "failure", err: errors.New("redis unavailable"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &systemAPIClientIPAllowlistVersionStoreStub{raw: tt.raw, err: tt.err}
			reader, err := NewRedisSystemAPIClientIPAllowlistVersionReader(store, "juhe-ai")
			if err != nil {
				t.Fatalf("construct version reader: %v", err)
			}
			version, err := reader.SystemAPIClientIPAllowlistVersion(context.Background())
			if (err != nil) != tt.wantErr {
				t.Fatalf("version error = %v, wantErr %v", err, tt.wantErr)
			}
			if version != tt.wantVersion {
				t.Fatalf("version = %q, want %q", version, tt.wantVersion)
			}
			if store.key != "juhe-ai:juhe-ai:cache-version:gateway:client-ip-policy-by-ip" {
				t.Fatalf("version key = %q", store.key)
			}
		})
	}
}

type systemAPIClientIPAllowlistReaderStub struct {
	allowlisted bool
	expiresAt   *time.Time
	err         error
	calls       int
	lastIPHash  string
	lastNow     time.Time
}

func (s *systemAPIClientIPAllowlistReaderStub) FindSystemAPIClientIPAllowlistPolicy(
	_ context.Context,
	ipHash string,
	now time.Time,
) (port.SystemAPIClientIPAllowlistPolicy, bool, error) {
	s.calls++
	s.lastIPHash = ipHash
	s.lastNow = now
	found := s.allowlisted
	if s.expiresAt != nil && !now.Before(*s.expiresAt) {
		found = false
	}
	return port.SystemAPIClientIPAllowlistPolicy{
		ID:        "policy_allowlist",
		ExpiresAt: s.expiresAt,
	}, found, s.err
}

type systemAPIClientIPAllowlistVersionReaderStub struct {
	versions []string
	calls    int
	err      error
}

func (s *systemAPIClientIPAllowlistVersionReaderStub) SystemAPIClientIPAllowlistVersion(
	context.Context,
) (string, error) {
	s.calls++
	if s.err != nil {
		return "", s.err
	}
	index := min(s.calls-1, len(s.versions)-1)
	return s.versions[index], nil
}

type systemAPIClientIPAllowlistVersionStoreStub struct {
	raw []byte
	err error
	key string
}

func (s *systemAPIClientIPAllowlistVersionStoreStub) GetRaw(
	_ context.Context,
	key string,
) ([]byte, error) {
	s.key = key
	return s.raw, s.err
}
