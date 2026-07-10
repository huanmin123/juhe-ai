package managementauth

import (
	"context"
	crand "crypto/rand"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/pbkdf2"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	passwordHashAlgorithm  = "pbkdf2"
	passwordHashDigest     = "sha512"
	passwordHashIterations = 120000
	passwordSaltBytes      = 16
	passwordDerivedBytes   = 32
)

var (
	ErrPasswordInvalid      = errors.New("management auth password invalid")
	ErrPasswordWhitespace   = errors.New("management auth password whitespace")
	ErrPasswordOldRequired  = errors.New("management auth old password required")
	ErrPasswordOldIncorrect = errors.New("management auth old password incorrect")
	ErrPasswordAccountGone  = errors.New("management auth password account not found")
)

type PasswordChangeInput struct {
	AuthContext Context
	OldPassword *string
	NewPassword string
}

type PasswordChangeResult struct {
	Account SystemAccountSummary
}

type SystemAccountSummary struct {
	ID                     string
	Username               string
	DisplayName            string
	Description            string
	Role                   string
	Status                 string
	MustChangePassword     bool
	ImageGenerationEnabled bool
	LastLoginAt            *time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

type PasswordService struct {
	store          port.ManagementCurrentUserPasswordChanger
	now            func() time.Time
	hashPassword   func(string) (string, error)
	verifyPassword func(string, string) bool
}

type PasswordServiceOptions struct {
	Store          port.ManagementCurrentUserPasswordChanger
	Now            func() time.Time
	HashPassword   func(string) (string, error)
	VerifyPassword func(string, string) bool
}

func NewPasswordService(store port.ManagementCurrentUserPasswordChanger) *PasswordService {
	return NewPasswordServiceWithOptions(PasswordServiceOptions{Store: store})
}

func NewPasswordServiceWithOptions(opts PasswordServiceOptions) *PasswordService {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	hashPassword := opts.HashPassword
	if hashPassword == nil {
		hashPassword = HashPassword
	}
	verifyPassword := opts.VerifyPassword
	if verifyPassword == nil {
		verifyPassword = VerifyPassword
	}
	return &PasswordService{
		store:          opts.Store,
		now:            now,
		hashPassword:   hashPassword,
		verifyPassword: verifyPassword,
	}
}

func HashPassword(password string) (string, error) {
	salt := make([]byte, passwordSaltBytes)
	if _, err := crand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	return hashPasswordWithSalt(password, salt), nil
}

func VerifyPassword(password string, passwordHash string) bool {
	parts := strings.Split(passwordHash, "$")
	if len(parts) != 5 || parts[0] != passwordHashAlgorithm || parts[1] != passwordHashDigest {
		return false
	}
	iterations, err := strconv.Atoi(parts[2])
	if err != nil || iterations <= 0 {
		return false
	}
	salt, ok := decodePasswordHashPart(parts[3])
	if !ok || len(salt) == 0 {
		return false
	}
	expected, ok := decodePasswordHashPart(parts[4])
	if !ok || len(expected) == 0 {
		return false
	}
	derived := pbkdf2.Key([]byte(password), salt, iterations, len(expected), sha512.New)
	return subtle.ConstantTimeCompare(derived, expected) == 1
}

func (s *PasswordService) ChangePassword(ctx context.Context, input PasswordChangeInput) (PasswordChangeResult, error) {
	if s.store == nil {
		return PasswordChangeResult{}, fmt.Errorf("management auth password store is required")
	}
	if err := validatePasswordChangeInput(input); err != nil {
		return PasswordChangeResult{}, err
	}
	authContext := input.AuthContext
	systemAccountID := strings.TrimSpace(authContext.SystemAccountID)
	if systemAccountID == "" {
		return PasswordChangeResult{}, ErrPasswordAccountGone
	}
	sessionID := strings.TrimSpace(authContext.SessionID)
	if sessionID == "" {
		return PasswordChangeResult{}, fmt.Errorf("management auth session id is required")
	}
	if !authContext.MustChangePassword {
		if input.OldPassword == nil {
			return PasswordChangeResult{}, ErrPasswordOldRequired
		}
		credential, found, err := s.store.FindManagementSystemAccountPasswordByUsername(ctx, authContext.Username)
		if err != nil {
			return PasswordChangeResult{}, err
		}
		if !found ||
			credential.Status != "active" ||
			credential.ID != systemAccountID ||
			!s.verifyPassword(*input.OldPassword, credential.PasswordHash) {
			return PasswordChangeResult{}, ErrPasswordOldIncorrect
		}
	}
	passwordHash, err := s.hashPassword(input.NewPassword)
	if err != nil {
		return PasswordChangeResult{}, err
	}
	account, found, err := s.store.UpdateManagementCurrentUserPassword(ctx, port.ManagementCurrentUserPasswordUpdateInput{
		SystemAccountID: systemAccountID,
		PasswordHash:    passwordHash,
		UpdatedAt:       s.now().UTC(),
	})
	if err != nil {
		return PasswordChangeResult{}, err
	}
	if !found {
		return PasswordChangeResult{}, ErrPasswordAccountGone
	}
	if err := s.store.RevokeOtherManagementSessionsForAccount(ctx, systemAccountID, sessionID); err != nil {
		return PasswordChangeResult{}, err
	}
	return PasswordChangeResult{Account: systemAccountSummaryFromPort(account)}, nil
}

func validatePasswordChangeInput(input PasswordChangeInput) error {
	if utf8.RuneCountInString(input.NewPassword) < 4 {
		return ErrPasswordInvalid
	}
	if input.OldPassword != nil && *input.OldPassword == "" {
		return ErrPasswordInvalid
	}
	if containsPasswordWhitespace(input.NewPassword) ||
		(input.OldPassword != nil && containsPasswordWhitespace(*input.OldPassword)) {
		return ErrPasswordWhitespace
	}
	return nil
}

func hashPasswordWithSalt(password string, salt []byte) string {
	derived := pbkdf2.Key([]byte(password), salt, passwordHashIterations, passwordDerivedBytes, sha512.New)
	return strings.Join([]string{
		passwordHashAlgorithm,
		passwordHashDigest,
		strconv.Itoa(passwordHashIterations),
		base64.RawURLEncoding.EncodeToString(salt),
		base64.RawURLEncoding.EncodeToString(derived),
	}, "$")
}

func decodePasswordHashPart(value string) ([]byte, bool) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err == nil {
		return decoded, true
	}
	decoded, err = base64.URLEncoding.DecodeString(value)
	return decoded, err == nil
}

func containsPasswordWhitespace(value string) bool {
	return strings.ContainsFunc(value, unicode.IsSpace)
}

func systemAccountSummaryFromPort(row port.ManagementSystemAccountSummary) SystemAccountSummary {
	return SystemAccountSummary{
		ID:                     row.ID,
		Username:               row.Username,
		DisplayName:            row.DisplayName,
		Description:            row.Description,
		Role:                   row.Role,
		Status:                 row.Status,
		MustChangePassword:     row.MustChangePassword && !IsAdminRole(row.Role),
		ImageGenerationEnabled: row.ImageGenerationEnabled,
		LastLoginAt:            row.LastLoginAt,
		CreatedAt:              row.CreatedAt,
		UpdatedAt:              row.UpdatedAt,
	}
}
