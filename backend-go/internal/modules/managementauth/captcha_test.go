package managementauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	redisplatform "juhe-ai/backend-go/internal/platform/redis"
)

func TestCaptchaIssueChallengeStoresRedisChallenge(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	store := &captchaStateStoreStub{
		decision: redisplatform.FixedWindowDecision{Allowed: true},
	}
	service := NewCaptchaServiceWithOptions(CaptchaServiceOptions{
		Store:          store,
		Now:            func() time.Time { return now },
		NewID:          func() string { return "captcha-id" },
		GenerateAnswer: func() (string, error) { return "abcd2", nil },
		RenderImage:    func(answer string) (string, error) { return "data:image/png;base64," + answer, nil },
	})

	challenge, err := service.IssueChallenge(context.Background(), "203.0.113.10")
	if err != nil {
		t.Fatalf("IssueChallenge() error = %v", err)
	}

	if challenge.CaptchaID != "captcha-id" ||
		challenge.Image != "data:image/png;base64,ABCD2" ||
		challenge.ExpiresAt != "2026-07-08T12:05:00.000Z" {
		t.Fatalf("challenge = %+v", challenge)
	}
	if store.setKey != "auth_captcha:challenge:captcha-id" || store.setTTL != CaptchaTTL {
		t.Fatalf("set key/ttl = %q/%s", store.setKey, store.setTTL)
	}
	var record captchaChallengeRecord
	if err := json.Unmarshal(store.setValue, &record); err != nil {
		t.Fatalf("unmarshal record: %v", err)
	}
	if record.Answer != "ABCD2" || record.ExpiresAt != now.Add(CaptchaTTL).UnixMilli() {
		t.Fatalf("record = %+v", record)
	}
	if got, want := len(store.limits), 1; got != want {
		t.Fatalf("limits length = %d, want %d", got, want)
	}
	limit := store.limits[0]
	if limit.Limit != CaptchaIssueThreshold || limit.Window != CaptchaIssueWindow {
		t.Fatalf("limit = %+v", limit)
	}
	if !strings.HasPrefix(limit.Key, "auth_captcha:issue:") || strings.Contains(limit.Key, "203.0.113.10") {
		t.Fatalf("limit key = %q", limit.Key)
	}
}

func TestCaptchaIssueChallengeRateLimitDoesNotStoreChallenge(t *testing.T) {
	store := &captchaStateStoreStub{
		decision: redisplatform.FixedWindowDecision{Allowed: false, RetryAfterSeconds: 12},
	}
	service := NewCaptchaServiceWithOptions(CaptchaServiceOptions{
		Store:          store,
		GenerateAnswer: func() (string, error) { return "ABCD2", nil },
		RenderImage:    func(string) (string, error) { return "data:image/png;base64,test", nil },
	})

	_, err := service.IssueChallenge(context.Background(), "203.0.113.10")

	var limitErr *CaptchaIssueLimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("IssueChallenge() error = %v, want CaptchaIssueLimitError", err)
	}
	if limitErr.RetryAfterSeconds != 12 {
		t.Fatalf("RetryAfterSeconds = %d, want 12", limitErr.RetryAfterSeconds)
	}
	if store.setCalled {
		t.Fatal("rate-limited captcha issue should not store a challenge")
	}
}

func TestCaptchaIssueChallengeRenderErrorDoesNotStoreChallenge(t *testing.T) {
	store := &captchaStateStoreStub{
		decision: redisplatform.FixedWindowDecision{Allowed: true},
	}
	service := NewCaptchaServiceWithOptions(CaptchaServiceOptions{
		Store:          store,
		GenerateAnswer: func() (string, error) { return "ABCD2", nil },
		RenderImage:    func(string) (string, error) { return "", errors.New("render failed") },
	})

	_, err := service.IssueChallenge(context.Background(), "203.0.113.10")

	if err == nil {
		t.Fatal("IssueChallenge() error = nil, want render error")
	}
	if store.setCalled {
		t.Fatal("render failure should not store a challenge")
	}
}

func TestCaptchaVerifyChallengeConsumesAndNormalizesAnswer(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	record, err := json.Marshal(captchaChallengeRecord{
		Answer:    "ABCD2",
		ExpiresAt: now.Add(time.Minute).UnixMilli(),
	})
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	store := &captchaStateStoreStub{getDeleteValue: record}
	service := NewCaptchaServiceWithOptions(CaptchaServiceOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})

	ok, err := service.VerifyChallenge(context.Background(), "captcha-id", " a b c d 2 ")
	if err != nil {
		t.Fatalf("VerifyChallenge() error = %v", err)
	}
	if !ok {
		t.Fatal("VerifyChallenge() = false, want true")
	}
	if store.getDeleteKey != "auth_captcha:challenge:captcha-id" {
		t.Fatalf("getDeleteKey = %q", store.getDeleteKey)
	}
}

func TestCaptchaVerifyChallengeReturnsFalseForMissingOrExpired(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	service := NewCaptchaServiceWithOptions(CaptchaServiceOptions{
		Store: &captchaStateStoreStub{getDeleteErr: redisplatform.ErrNotFound},
		Now:   func() time.Time { return now },
	})
	ok, err := service.VerifyChallenge(context.Background(), "missing", "ABCD2")
	if err != nil {
		t.Fatalf("VerifyChallenge() missing error = %v", err)
	}
	if ok {
		t.Fatal("missing challenge verified")
	}

	expiredRecord, err := json.Marshal(captchaChallengeRecord{
		Answer:    "ABCD2",
		ExpiresAt: now.Add(-time.Second).UnixMilli(),
	})
	if err != nil {
		t.Fatalf("marshal expired record: %v", err)
	}
	service = NewCaptchaServiceWithOptions(CaptchaServiceOptions{
		Store: &captchaStateStoreStub{getDeleteValue: expiredRecord},
		Now:   func() time.Time { return now },
	})
	ok, err = service.VerifyChallenge(context.Background(), "expired", "ABCD2")
	if err != nil {
		t.Fatalf("VerifyChallenge() expired error = %v", err)
	}
	if ok {
		t.Fatal("expired challenge verified")
	}
}

func TestCaptchaRenderImageReturnsPNGDataURL(t *testing.T) {
	dataURL, err := RenderCaptchaImage("ABCD2")
	if err != nil {
		t.Fatalf("RenderCaptchaImage() error = %v", err)
	}
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(dataURL, prefix) {
		t.Fatalf("dataURL prefix = %q", dataURL[:min(len(dataURL), len(prefix))])
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(dataURL, prefix))
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}
	wantHeader := []byte{137, 80, 78, 71, 13, 10, 26, 10}
	if len(raw) < len(wantHeader) || string(raw[:len(wantHeader)]) != string(wantHeader) {
		t.Fatalf("PNG header = %v", raw[:min(len(raw), len(wantHeader))])
	}
	text := string(raw)
	if strings.Contains(text, "<svg") || strings.Contains(text, "</svg") || strings.Contains(text, "ABCD2") {
		t.Fatalf("captcha image leaks text content")
	}
}

func TestGenerateCaptchaAnswerUsesAllowedChars(t *testing.T) {
	answer, err := GenerateCaptchaAnswer()
	if err != nil {
		t.Fatalf("GenerateCaptchaAnswer() error = %v", err)
	}
	if len(answer) != captchaAnswerLength {
		t.Fatalf("answer length = %d, want %d", len(answer), captchaAnswerLength)
	}
	for _, item := range answer {
		if !strings.ContainsRune(captchaChars, item) {
			t.Fatalf("answer %q contains disallowed char %q", answer, item)
		}
	}
}

type captchaStateStoreStub struct {
	setCalled      bool
	setKey         string
	setValue       []byte
	setTTL         time.Duration
	getDeleteKey   string
	getDeleteValue []byte
	getDeleteErr   error
	limits         []redisplatform.FixedWindowLimit
	decision       redisplatform.FixedWindowDecision
	decisionErr    error
}

func (s *captchaStateStoreStub) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	s.setCalled = true
	s.setKey = key
	s.setValue = append([]byte(nil), value...)
	s.setTTL = ttl
	return nil
}

func (s *captchaStateStoreStub) GetDelete(_ context.Context, key string) ([]byte, error) {
	s.getDeleteKey = key
	if s.getDeleteErr != nil {
		return nil, s.getDeleteErr
	}
	return append([]byte(nil), s.getDeleteValue...), nil
}

func (s *captchaStateStoreStub) AllowFixedWindow(_ context.Context, limits []redisplatform.FixedWindowLimit) (redisplatform.FixedWindowDecision, error) {
	s.limits = append([]redisplatform.FixedWindowLimit(nil), limits...)
	return s.decision, s.decisionErr
}
