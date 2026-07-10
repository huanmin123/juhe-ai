package managementsystemaccounts

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"juhe-ai/backend-go/internal/store/port"
)

var (
	ErrSystemAccountCreateInvalid     = errors.New("management system account create invalid")
	ErrSystemAccountUsernameExists    = errors.New("management system account username exists")
	ErrSystemAccountDisplayNameExists = errors.New("management system account display name exists")
	ErrSystemAccountWhitespace        = errors.New("management system account whitespace")
)

type CreateInput struct {
	Username               string
	DisplayName            string
	Description            *string
	Password               string
	Role                   string
	Status                 string
	MustChangePassword     *bool
	ImageGenerationEnabled *bool
}

type CreateResult struct {
	Account          Summary
	DefaultGroupIDs  []string
	DefaultAPIKeyIDs []string
}

func (s *Service) Create(ctx context.Context, input CreateInput) (CreateResult, error) {
	creator, ok := s.store.(port.ManagementSystemAccountCreator)
	if !ok || creator == nil {
		return CreateResult{}, fmt.Errorf("management system account creator is required")
	}

	username := input.Username
	if username == "" || utf8.RuneCountInString(username) < 2 {
		return CreateResult{}, ErrSystemAccountCreateInvalid
	}
	displayName := input.DisplayName
	if displayName == "" {
		return CreateResult{}, ErrSystemAccountCreateInvalid
	}
	password := input.Password
	if password == "" || utf8.RuneCountInString(password) < 4 {
		return CreateResult{}, ErrSystemAccountCreateInvalid
	}

	if containsWhitespace(username) || containsWhitespace(displayName) || containsWhitespace(password) {
		return CreateResult{}, ErrSystemAccountWhitespace
	}

	role := normalizeCreateRole(input.Role)
	if role == "" {
		return CreateResult{}, ErrSystemAccountCreateInvalid
	}
	status := normalizeCreateStatus(input.Status)
	if status == "" {
		return CreateResult{}, ErrSystemAccountCreateInvalid
	}
	mustChangePassword := normalizeMustChangePassword(input.MustChangePassword, role)
	imageGenerationEnabled := normalizeBoolDefault(input.ImageGenerationEnabled, false)

	var description *string
	if input.Description != nil {
		text := strings.TrimSpace(*input.Description)
		if text != "" {
			if utf8.RuneCountInString(text) > maxDescriptionRunes {
				return CreateResult{}, ErrSystemAccountCreateInvalid
			}
			description = &text
		}
	}

	passwordHash, err := s.hashPassword(password)
	if err != nil {
		return CreateResult{}, fmt.Errorf("hash system account password: %w", err)
	}
	defaultAPIKeys, err := s.defaultAPIKeyInputs()
	if err != nil {
		return CreateResult{}, err
	}

	now := s.now().UTC()
	id := createID("sysacc")

	storeInput := port.ManagementSystemAccountCreateInput{
		ID:                     id,
		Username:               username,
		DisplayName:            displayName,
		Description:            description,
		Role:                   role,
		Status:                 status,
		PasswordHash:           passwordHash,
		MustChangePassword:     mustChangePassword,
		ImageGenerationEnabled: imageGenerationEnabled,
		DefaultAPIKeys:         defaultAPIKeys,
		CreatedAt:              now,
		UpdatedAt:              now,
	}

	result, err := creator.CreateManagementSystemAccount(ctx, storeInput)
	if err != nil {
		if errors.Is(err, port.ErrManagementSystemAccountUsernameExists) {
			return CreateResult{}, ErrSystemAccountUsernameExists
		}
		if errors.Is(err, port.ErrManagementSystemAccountDisplayNameExists) {
			return CreateResult{}, ErrSystemAccountDisplayNameExists
		}
		return CreateResult{}, err
	}

	summary := systemAccountSummaryFromPort(result.Account)
	summary.LastLoginAt = ""

	return CreateResult{
		Account:          summary,
		DefaultGroupIDs:  result.DefaultGroupIDs,
		DefaultAPIKeyIDs: result.DefaultAPIKeyIDs,
	}, nil
}

func (s *Service) defaultAPIKeyInputs() ([]port.ManagementDefaultAPIKeyCreateInput, error) {
	const defaultRouteResourceCount = 6
	items := make([]port.ManagementDefaultAPIKeyCreateInput, 0, defaultRouteResourceCount)
	for i := 0; i < defaultRouteResourceCount; i++ {
		secret, err := createAPIKeySecret()
		if err != nil {
			return nil, err
		}
		encrypted, err := s.encryptJSON(map[string]any{"key": secret})
		if err != nil {
			return nil, fmt.Errorf("encrypt default api key: %w", err)
		}
		items = append(items, port.ManagementDefaultAPIKeyCreateInput{
			ID:                 createID("key"),
			KeyHash:            hashSecret(secret),
			KeyPrefix:          secretPrefix(secret),
			KeySuffix:          secretSuffix(secret),
			KeySecretEncrypted: encrypted,
		})
	}
	return items, nil
}

func createAPIKeySecret() (string, error) {
	var bytes [32]byte
	if _, err := io.ReadFull(rand.Reader, bytes[:]); err != nil {
		return "", fmt.Errorf("generate api key secret: %w", err)
	}
	return "sk-" + hex.EncodeToString(bytes[:]), nil
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func secretPrefix(secret string) string {
	if len(secret) <= 8 {
		return secret
	}
	return secret[:8]
}

func secretSuffix(secret string) string {
	if len(secret) <= 8 {
		return secret
	}
	return secret[len(secret)-8:]
}

func (s *Service) encryptJSON(value map[string]any) (string, error) {
	secret := strings.TrimSpace(s.secret)
	if secret == "" {
		secret = "juhe-ai-go-development-secret"
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	plain, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sealed := aead.Seal(nil, nonce, plain, nil)
	tagSize := aead.Overhead()
	ciphertext := sealed[:len(sealed)-tagSize]
	tag := sealed[len(sealed)-tagSize:]
	encode := base64.RawURLEncoding.EncodeToString
	return "v1:" + encode(nonce) + ":" + encode(tag) + ":" + encode(ciphertext), nil
}

func normalizeCreateRole(role string) string {
	switch role {
	case "":
		return "user"
	case "admin":
		return "admin"
	case "user":
		return "user"
	default:
		return ""
	}
}

func normalizeCreateStatus(status string) string {
	switch status {
	case "":
		return "active"
	case "active":
		return "active"
	case "disabled":
		return "disabled"
	default:
		return ""
	}
}

func normalizeMustChangePassword(raw *bool, role string) bool {
	if role == "admin" || role == "super_admin" {
		return false
	}
	if raw != nil {
		return *raw
	}
	return role == "user"
}

func normalizeBoolDefault(raw *bool, fallback bool) bool {
	if raw != nil {
		return *raw
	}
	return fallback
}

func containsWhitespace(value string) bool {
	return strings.ContainsFunc(value, unicode.IsSpace)
}

func createID(prefix string) string {
	return prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}
