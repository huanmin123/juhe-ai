package managementauth

import (
	"context"
	crand "crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"juhe-ai/backend-go/internal/store/port"
)

const ManagementSessionTTL = 14 * 24 * time.Hour

var (
	ErrLoginInvalidInput        = errors.New("management login input invalid")
	ErrLoginWhitespace          = errors.New("management login username or password contains whitespace")
	ErrLoginCaptchaInvalid      = errors.New("management login captcha invalid")
	ErrLoginCredentialsInvalid  = errors.New("management login credentials invalid")
	ErrLoginSessionNotCompleted = errors.New("management login session not completed")
)

type LoginCaptchaVerifier interface {
	VerifyChallenge(ctx context.Context, captchaID string, captchaCode string) (bool, error)
}

type LoginGuard interface {
	CheckAllowed(ctx context.Context, clientIP string, username string) (LoginGuardDecision, error)
	RecordFailed(ctx context.Context, clientIP string, username string) (LoginGuardDecision, error)
	RecordSuccess(ctx context.Context, clientIP string, username string) error
}

type LoginInput struct {
	Username    string
	Password    string
	CaptchaID   string
	CaptchaCode string
	ClientIP    string
}

type LoginResult struct {
	Account          SystemAccountSummary
	SessionToken     string
	SessionID        string
	SessionExpiresAt time.Time
}

type LoginLimitError struct {
	Message           string
	RetryAfterSeconds int
}

func (e *LoginLimitError) Error() string {
	return "management login rate limited"
}

type LoginService struct {
	store           port.ManagementLoginStore
	captcha         LoginCaptchaVerifier
	guard           LoginGuard
	now             func() time.Time
	newSessionToken func() (string, error)
	newSessionID    func(time.Time) (string, error)
	verifyPassword  func(string, string) bool
	captchaDisabled bool
}

type LoginServiceOptions struct {
	Store           port.ManagementLoginStore
	Captcha         LoginCaptchaVerifier
	Guard           LoginGuard
	Now             func() time.Time
	NewSessionToken func() (string, error)
	NewSessionID    func(time.Time) (string, error)
	VerifyPassword  func(string, string) bool
	CaptchaDisabled bool
}

func NewLoginService(store port.ManagementLoginStore, captcha LoginCaptchaVerifier, guard LoginGuard) *LoginService {
	return NewLoginServiceWithOptions(LoginServiceOptions{Store: store, Captcha: captcha, Guard: guard})
}

func NewLoginServiceWithOptions(opts LoginServiceOptions) *LoginService {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	newSessionToken := opts.NewSessionToken
	if newSessionToken == nil {
		newSessionToken = GenerateSessionToken
	}
	newSessionID := opts.NewSessionID
	if newSessionID == nil {
		newSessionID = GenerateSessionID
	}
	verifyPassword := opts.VerifyPassword
	if verifyPassword == nil {
		verifyPassword = VerifyPassword
	}
	return &LoginService{
		store:           opts.Store,
		captcha:         opts.Captcha,
		guard:           opts.Guard,
		now:             now,
		newSessionToken: newSessionToken,
		newSessionID:    newSessionID,
		verifyPassword:  verifyPassword,
		captchaDisabled: opts.CaptchaDisabled,
	}
}

func (s *LoginService) Login(ctx context.Context, input LoginInput) (LoginResult, error) {
	if s == nil || s.store == nil {
		return LoginResult{}, errors.New("management login store is required")
	}
	if !s.captchaDisabled && s.captcha == nil {
		return LoginResult{}, errors.New("management login captcha verifier is required")
	}
	if s.guard == nil {
		return LoginResult{}, errors.New("management login guard is required")
	}
	if err := validateLoginInput(input, s.captchaDisabled); err != nil {
		return LoginResult{}, err
	}

	if !s.captchaDisabled {
		captchaOK, err := s.captcha.VerifyChallenge(ctx, strings.TrimSpace(input.CaptchaID), input.CaptchaCode)
		if err != nil {
			return LoginResult{}, err
		}
		if !captchaOK {
			return LoginResult{}, ErrLoginCaptchaInvalid
		}
	}

	if decision, err := s.guard.CheckAllowed(ctx, input.ClientIP, input.Username); err != nil {
		return LoginResult{}, err
	} else if decision.Blocked {
		return LoginResult{}, &LoginLimitError{Message: decision.Message, RetryAfterSeconds: decision.RetryAfterSeconds}
	}

	credential, found, err := s.store.FindManagementSystemAccountPasswordByUsername(ctx, input.Username)
	if err != nil {
		return LoginResult{}, err
	}
	if !found || credential.Status != "active" || !s.verifyPassword(input.Password, credential.PasswordHash) {
		decision, err := s.guard.RecordFailed(ctx, input.ClientIP, input.Username)
		if err != nil {
			return LoginResult{}, err
		}
		if decision.Blocked {
			return LoginResult{}, &LoginLimitError{Message: decision.Message, RetryAfterSeconds: decision.RetryAfterSeconds}
		}
		return LoginResult{}, ErrLoginCredentialsInvalid
	}

	loggedInAt := s.now().UTC()
	sessionToken, err := s.newSessionToken()
	if err != nil {
		return LoginResult{}, err
	}
	sessionID, err := s.newSessionID(loggedInAt)
	if err != nil {
		return LoginResult{}, err
	}
	sessionExpiresAt := loggedInAt.Add(ManagementSessionTTL)
	session, found, err := s.store.CompleteManagementLogin(ctx, port.ManagementLoginSessionInput{
		SystemAccountID:      credential.ID,
		VerifiedPasswordHash: credential.PasswordHash,
		SessionID:            sessionID,
		TokenHash:            HashSessionToken(sessionToken),
		LoggedInAt:           loggedInAt,
		ExpiresAt:            sessionExpiresAt,
	})
	if err != nil {
		return LoginResult{}, err
	}
	if !found {
		return LoginResult{}, ErrLoginCredentialsInvalid
	}
	if session.SessionID == "" || session.SessionExpiresAt.IsZero() {
		return LoginResult{}, ErrLoginSessionNotCompleted
	}
	if err := s.guard.RecordSuccess(ctx, input.ClientIP, credential.Username); err != nil {
		return LoginResult{}, err
	}
	return LoginResult{
		Account:          systemAccountSummaryFromPort(session.Account),
		SessionToken:     sessionToken,
		SessionID:        session.SessionID,
		SessionExpiresAt: session.SessionExpiresAt,
	}, nil
}

func validateLoginInput(input LoginInput, captchaDisabled bool) error {
	if input.Username == "" || input.Password == "" {
		return ErrLoginInvalidInput
	}
	if !captchaDisabled && (strings.TrimSpace(input.CaptchaID) == "" || strings.TrimSpace(input.CaptchaCode) == "") {
		return ErrLoginInvalidInput
	}
	if containsLoginWhitespace(input.Username) || containsLoginWhitespace(input.Password) {
		return ErrLoginWhitespace
	}
	return nil
}

func GenerateSessionToken() (string, error) {
	token := make([]byte, 32)
	if _, err := crand.Read(token); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(token), nil
}

func GenerateSessionID(now time.Time) (string, error) {
	randomPart := make([]byte, 4)
	if _, err := crand.Read(randomPart); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	return fmt.Sprintf("sess_%d_%s", now.UTC().UnixMilli(), hex.EncodeToString(randomPart)), nil
}

func containsLoginWhitespace(value string) bool {
	return strings.ContainsFunc(value, unicode.IsSpace)
}
