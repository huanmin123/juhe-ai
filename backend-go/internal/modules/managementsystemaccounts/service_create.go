package managementsystemaccounts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"juhe-ai/backend-go/internal/apikeysecret"
	"juhe-ai/backend-go/internal/secretcrypto"
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
	const defaultRouteResourceCount = 7
	items := make([]port.ManagementDefaultAPIKeyCreateInput, 0, defaultRouteResourceCount+1)
	codec := secretcrypto.NewJSONCodec(s.secret)
	for i := 0; i < defaultRouteResourceCount+1; i++ {
		purpose := "general"
		if i == defaultRouteResourceCount {
			purpose = "chat"
		}
		secret, err := apikeysecret.Generate()
		if err != nil {
			return nil, fmt.Errorf("generate api key secret: %w", err)
		}
		encrypted, err := codec.EncryptJSON(map[string]any{"key": secret})
		if err != nil {
			return nil, fmt.Errorf("encrypt default api key: %w", err)
		}
		items = append(items, port.ManagementDefaultAPIKeyCreateInput{
			ID:                 createID("key"),
			Purpose:            purpose,
			KeyHash:            apikeysecret.Hash(secret),
			KeyPrefix:          apikeysecret.Prefix(secret),
			KeySuffix:          apikeysecret.Suffix(secret),
			KeySecretEncrypted: encrypted,
		})
	}
	return items, nil
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
