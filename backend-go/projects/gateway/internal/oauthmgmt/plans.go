package oauthmgmt

import "context"

// providerPlans mirrors the four Node modules' route/service wiring. The
// exchanges close over the per-provider services; upstream transport stays
// behind the injected TokenExchanger.
func providerPlans() []providerPlan {
	return []providerPlan{openAIPlan(), anthropicPlan(), geminiPlan(), grokPlan()}
}

// --- OpenAI (gpt) -----------------------------------------------------------

func openAIPlan() providerPlan {
	patch := func(body map[string]any) (map[string]any, bool) { return parseOpenAICredentialsPatch(body) }
	plan := providerPlan{
		slug:                    "openai",
		module:                  "openai_oauth",
		providerCode:            ProviderGPT,
		accountType:             "oauth",
		label:                   "OpenAI",
		defaultAccountName:      "OpenAI OAuth Account",
		emailNameFallback:       true,
		preserveBaseURL:         false,
		revisionConflictMessage: "OpenAI OAuth 账户已被其他操作更新，请刷新页面后重试",
		authURLKeys:             []string{},
		createCodeKeys:          []string{"sessionId", "callbackUrl"},
		createRefreshKeys:       []string{"refreshToken", "clientId"},
		reauthCodeKeys:          []string{"sessionId", "callbackUrl", "expectedConfigRevision"},
		reauthRefreshKeys:       []string{"refreshToken", "clientId", "expectedConfigRevision"},
		parseCredentialsPatch:   patch,
	}
	plan.authURL = func(ctx context.Context, s *Store, _ map[string]any, ownerID string) (map[string]any, error) {
		return s.generateOpenAIAuthURL(ownerID)
	}
	plan.exchangeCode = func(ctx context.Context, s *Store, body map[string]any, ownerID string) (*tokenOutcome, error) {
		sessionID, ok := requiredTrimmedString(body, "sessionId")
		if !ok {
			return nil, &ValidationError{Message: "sessionId 不能为空"}
		}
		callbackURL, ok := requiredTrimmedString(body, "callbackUrl")
		if !ok {
			return nil, &ValidationError{Message: "callbackUrl 不能为空"}
		}
		safePatch, patchOK := safePatch(plan, body)
		if !patchOK {
			return nil, &ValidationError{Message: "credentialsPatch 无效"}
		}
		info, err := s.exchangeOpenAIAuthorizationCode(ctx, sessionID, callbackURL, ownerID)
		if err != nil {
			return nil, err
		}
		return &tokenOutcome{
			Credentials: mergePatchOpenAI(safePatch, buildOpenAIOAuthCredentials(info, "")),
			Name:        info.Email,
		}, nil
	}
	plan.exchangeRefresh = func(ctx context.Context, s *Store, body map[string]any) (*tokenOutcome, error) {
		refreshToken, ok := requiredTrimmedString(body, "refreshToken")
		if !ok {
			return nil, &ValidationError{Message: "refreshToken 不能为空"}
		}
		clientID, _ := bodyString(body, "clientId")
		safePatch, patchOK := safePatch(plan, body)
		if !patchOK {
			return nil, &ValidationError{Message: "credentialsPatch 无效"}
		}
		info, err := s.refreshOpenAIToken(ctx, refreshToken, clientID)
		if err != nil {
			return nil, err
		}
		return &tokenOutcome{
			Credentials: mergePatchOpenAI(safePatch, buildOpenAIOAuthCredentials(info, refreshToken)),
			Name:        info.Email,
		}, nil
	}
	plan.refreshStored = func(ctx context.Context, s *Store, current *rotationAccount) (map[string]any, error) {
		info, err := s.refreshOpenAIToken(ctx, stringCredential(current.Credentials, "refresh_token"),
			stringCredential(current.Credentials, "client_id"))
		if err != nil {
			return nil, err
		}
		return buildOpenAIOAuthCredentials(info, ""), nil
	}
	plan.refreshInput = func(ctx context.Context, s *Store, body map[string]any, current *rotationAccount) (map[string]any, error) {
		refreshToken, ok := requiredTrimmedString(body, "refreshToken")
		if !ok {
			return nil, &ValidationError{Message: "refreshToken 不能为空"}
		}
		clientID, present := bodyString(body, "clientId")
		if !present || trim(clientID) == "" {
			clientID = stringCredential(current.Credentials, "client_id")
		}
		info, err := s.refreshOpenAIToken(ctx, refreshToken, clientID)
		if err != nil {
			return nil, err
		}
		return buildOpenAIOAuthCredentials(info, refreshToken), nil
	}
	return plan
}

// --- Anthropic ---------------------------------------------------------------

func anthropicPlan() providerPlan {
	patch := func(body map[string]any) (map[string]any, bool) { return parseAnthropicCredentialsPatch(body) }
	plan := providerPlan{
		slug:                  "anthropic",
		module:                "anthropic_oauth",
		providerCode:          ProviderAnthropic,
		accountType:           "oauth",
		label:                 "Anthropic",
		defaultAccountName:    "Anthropic OAuth Account",
		emailNameFallback:     true,
		preserveBaseURL:       true,
		authURLKeys:           []string{},
		createCodeKeys:        []string{"sessionId", "callbackUrl"},
		createRefreshKeys:     []string{"refreshToken"},
		reauthCodeKeys:        []string{"sessionId", "callbackUrl", "expectedConfigRevision"},
		reauthRefreshKeys:     []string{"refreshToken", "expectedConfigRevision"},
		parseCredentialsPatch: patch,
	}
	plan.authURL = func(ctx context.Context, s *Store, _ map[string]any, ownerID string) (map[string]any, error) {
		return s.generateAnthropicAuthURL(ownerID)
	}
	plan.exchangeCode = func(ctx context.Context, s *Store, body map[string]any, ownerID string) (*tokenOutcome, error) {
		sessionID, ok := requiredTrimmedString(body, "sessionId")
		if !ok {
			return nil, &ValidationError{Message: "sessionId 不能为空"}
		}
		callbackURL, ok := requiredTrimmedString(body, "callbackUrl")
		if !ok {
			return nil, &ValidationError{Message: "callbackUrl 不能为空"}
		}
		safePatch, patchOK := safePatch(plan, body)
		if !patchOK {
			return nil, &ValidationError{Message: "credentialsPatch 无效"}
		}
		info, err := s.exchangeAnthropicAuthorizationCode(ctx, sessionID, callbackURL, ownerID)
		if err != nil {
			return nil, err
		}
		return &tokenOutcome{
			Credentials: mergePatchLast(buildAnthropicOAuthCredentials(info, ""), safePatch),
			Name:        info.Email,
		}, nil
	}
	plan.exchangeRefresh = func(ctx context.Context, s *Store, body map[string]any) (*tokenOutcome, error) {
		refreshToken, ok := requiredTrimmedString(body, "refreshToken")
		if !ok {
			return nil, &ValidationError{Message: "refreshToken 不能为空"}
		}
		safePatch, patchOK := safePatch(plan, body)
		if !patchOK {
			return nil, &ValidationError{Message: "credentialsPatch 无效"}
		}
		info, err := s.refreshAnthropicToken(ctx, refreshToken, "")
		if err != nil {
			return nil, err
		}
		return &tokenOutcome{
			Credentials: mergePatchLast(buildAnthropicOAuthCredentials(info, refreshToken), safePatch),
			Name:        info.Email,
		}, nil
	}
	plan.refreshStored = func(ctx context.Context, s *Store, current *rotationAccount) (map[string]any, error) {
		info, err := s.refreshAnthropicToken(ctx, stringCredential(current.Credentials, "refresh_token"),
			stringCredential(current.Credentials, "client_id"))
		if err != nil {
			return nil, err
		}
		return buildAnthropicOAuthCredentials(info, ""), nil
	}
	plan.refreshInput = func(ctx context.Context, s *Store, body map[string]any, current *rotationAccount) (map[string]any, error) {
		refreshToken, ok := requiredTrimmedString(body, "refreshToken")
		if !ok {
			return nil, &ValidationError{Message: "refreshToken 不能为空"}
		}
		info, err := s.refreshAnthropicToken(ctx, refreshToken, stringCredential(current.Credentials, "client_id"))
		if err != nil {
			return nil, err
		}
		return buildAnthropicOAuthCredentials(info, refreshToken), nil
	}
	return plan
}

// --- Gemini ------------------------------------------------------------------

func geminiPlan() providerPlan {
	patch := func(body map[string]any) (map[string]any, bool) { return parseGeminiCredentialsPatch(body) }
	plan := providerPlan{
		slug:                    "gemini",
		module:                  "gemini_oauth",
		providerCode:            ProviderGemini,
		accountType:             "google_oauth",
		label:                   "Gemini",
		defaultAccountName:      "Gemini OAuth Account",
		emailNameFallback:       false,
		preserveBaseURL:         true,
		capabilities:            true,
		revisionConflictMessage: "Gemini OAuth 账户已被其他操作更新，请刷新页面后重试",
		authURLKeys:             []string{"oauthType", "clientId", "clientSecret", "projectId", "tierId", "quotaProjectId", "baseUrl"},
		createCodeKeys: []string{"sessionId", "callbackUrl", "oauthType", "clientId", "clientSecret",
			"projectId", "tierId", "quotaProjectId", "baseUrl"},
		createRefreshKeys: []string{"refreshToken", "oauthType", "clientId", "clientSecret",
			"projectId", "tierId", "quotaProjectId", "baseUrl"},
		reauthCodeKeys: []string{"sessionId", "callbackUrl", "oauthType", "clientId", "clientSecret",
			"projectId", "tierId", "quotaProjectId", "baseUrl", "expectedConfigRevision"},
		reauthRefreshKeys: []string{"refreshToken", "oauthType", "clientId", "clientSecret",
			"projectId", "tierId", "quotaProjectId", "baseUrl", "expectedConfigRevision"},
		parseCredentialsPatch: patch,
	}
	plan.authURL = func(ctx context.Context, s *Store, body map[string]any, ownerID string) (map[string]any, error) {
		if err := validateGeminiBodyFields(body); err != nil {
			return nil, err
		}
		return s.generateGeminiAuthURL(geminiAuthURLOptions{
			OAuthType:      optionalTrimmedText(body, "oauthType"),
			ClientID:       optionalTrimmedText(body, "clientId"),
			ClientSecret:   optionalTrimmedText(body, "clientSecret"),
			ProjectID:      optionalTrimmedText(body, "projectId"),
			TierID:         optionalTrimmedText(body, "tierId"),
			QuotaProjectID: optionalTrimmedText(body, "quotaProjectId"),
			BaseURL:        optionalTrimmedText(body, "baseUrl"),
			OwnerID:        ownerID,
		})
	}
	plan.exchangeCode = func(ctx context.Context, s *Store, body map[string]any, ownerID string) (*tokenOutcome, error) {
		if err := validateGeminiBodyFields(body); err != nil {
			return nil, err
		}
		safePatch, patchOK := safePatch(plan, body)
		if !patchOK {
			return nil, &ValidationError{Message: "credentialsPatch 无效"}
		}
		info, err := s.exchangeGeminiAuthorizationCode(ctx, geminiExchangeOptions{
			SessionID:      optionalTrimmedText(body, "sessionId"),
			CallbackURL:    optionalTrimmedText(body, "callbackUrl"),
			OAuthType:      optionalTrimmedText(body, "oauthType"),
			ClientID:       optionalTrimmedText(body, "clientId"),
			ClientSecret:   optionalTrimmedText(body, "clientSecret"),
			ProjectID:      optionalTrimmedText(body, "projectId"),
			TierID:         optionalTrimmedText(body, "tierId"),
			QuotaProjectID: optionalTrimmedText(body, "quotaProjectId"),
			BaseURL:        optionalTrimmedText(body, "baseUrl"),
			OwnerID:        ownerID,
		})
		if err != nil {
			return nil, err
		}
		return &tokenOutcome{
			Credentials: mergePatchLast(buildGeminiOAuthCredentials(info, nil), safePatch),
		}, nil
	}
	plan.exchangeRefresh = func(ctx context.Context, s *Store, body map[string]any) (*tokenOutcome, error) {
		refreshToken, ok := requiredTrimmedString(body, "refreshToken")
		if !ok {
			return nil, &ValidationError{Message: "refreshToken 不能为空"}
		}
		if err := validateGeminiBodyFields(body); err != nil {
			return nil, err
		}
		safePatch, patchOK := safePatch(plan, body)
		if !patchOK {
			return nil, &ValidationError{Message: "credentialsPatch 无效"}
		}
		quotaProjectID := optionalTrimmedText(body, "quotaProjectId")
		if quotaProjectID == "" {
			quotaProjectID = trim(textFrom(safePatch, "quota_project_id"))
		}
		baseURL := optionalTrimmedText(body, "baseUrl")
		if baseURL == "" {
			baseURL = trim(textFrom(safePatch, "base_url"))
		}
		info, err := s.refreshGeminiToken(ctx, refreshToken, geminiAuthURLOptions{
			OAuthType:      optionalTrimmedText(body, "oauthType"),
			ClientID:       optionalTrimmedText(body, "clientId"),
			ClientSecret:   optionalTrimmedText(body, "clientSecret"),
			ProjectID:      optionalTrimmedText(body, "projectId"),
			TierID:         optionalTrimmedText(body, "tierId"),
			QuotaProjectID: quotaProjectID,
			BaseURL:        baseURL,
		})
		if err != nil {
			return nil, err
		}
		return &tokenOutcome{
			Credentials: mergePatchLast(buildGeminiOAuthCredentials(info, &geminiCredentialFallback{
				RefreshToken:   refreshToken,
				QuotaProjectID: quotaProjectID,
				BaseURL:        baseURL,
			}), safePatch),
		}, nil
	}
	plan.refreshStored = func(ctx context.Context, s *Store, current *rotationAccount) (map[string]any, error) {
		credentials := current.Credentials
		info, err := s.refreshGeminiToken(ctx, stringCredential(credentials, "refresh_token"), geminiAuthURLOptions{
			OAuthType:      geminiAccountOAuthType(credentials),
			ClientID:       stringCredential(credentials, "client_id"),
			ClientSecret:   stringCredential(credentials, "client_secret"),
			ProjectID:      stringCredential(credentials, "project_id"),
			TierID:         stringCredential(credentials, "tier_id"),
			QuotaProjectID: stringCredential(credentials, "quota_project_id"),
			BaseURL:        stringCredential(credentials, "base_url"),
			Scope:          stringCredential(credentials, "scope"),
		})
		if err != nil {
			return nil, err
		}
		return buildGeminiOAuthCredentials(info, &geminiCredentialFallback{
			RefreshToken: stringCredential(credentials, "refresh_token"),
			OAuthType:    geminiAccountOAuthType(credentials),
			ProjectID:    stringCredential(credentials, "project_id"),
			TierID:       stringCredential(credentials, "tier_id"),
			BaseURL:      stringCredential(credentials, "base_url"),
			Scope:        stringCredential(credentials, "scope"),
		}), nil
	}
	plan.refreshInput = func(ctx context.Context, s *Store, body map[string]any, current *rotationAccount) (map[string]any, error) {
		refreshToken, ok := requiredTrimmedString(body, "refreshToken")
		if !ok {
			return nil, &ValidationError{Message: "refreshToken 不能为空"}
		}
		credentials := current.Credentials
		pick := func(key string, fromBody string) string {
			if fromBody != "" {
				return fromBody
			}
			return stringCredential(credentials, key)
		}
		oauthType := optionalTrimmedText(body, "oauthType")
		if oauthType == "" {
			oauthType = geminiAccountOAuthType(credentials)
		}
		info, err := s.refreshGeminiToken(ctx, refreshToken, geminiAuthURLOptions{
			OAuthType:      oauthType,
			ClientID:       pick("client_id", optionalTrimmedText(body, "clientId")),
			ClientSecret:   pick("client_secret", optionalTrimmedText(body, "clientSecret")),
			ProjectID:      pick("project_id", optionalTrimmedText(body, "projectId")),
			TierID:         pick("tier_id", optionalTrimmedText(body, "tierId")),
			QuotaProjectID: pick("quota_project_id", optionalTrimmedText(body, "quotaProjectId")),
			BaseURL:        pick("base_url", optionalTrimmedText(body, "baseUrl")),
			Scope:          stringCredential(credentials, "scope"),
		})
		if err != nil {
			return nil, err
		}
		return buildGeminiOAuthCredentials(info, &geminiCredentialFallback{
			RefreshToken: refreshToken,
			ProjectID:    stringCredential(credentials, "project_id"),
			TierID:       stringCredential(credentials, "tier_id"),
			BaseURL:      stringCredential(credentials, "base_url"),
			Scope:        stringCredential(credentials, "scope"),
		}), nil
	}
	return plan
}

// --- Grok (xai) --------------------------------------------------------------

func grokPlan() providerPlan {
	patch := func(body map[string]any) (map[string]any, bool) { return parseGrokCredentialsPatch(body) }
	plan := providerPlan{
		slug:                  "grok",
		module:                "grok_oauth",
		providerCode:          ProviderXAI,
		accountType:           "oauth",
		label:                 "Grok",
		defaultAccountName:    "Grok OAuth Account",
		emailNameFallback:     true,
		requiredProfileID:     ProfileXAIOpenAIV1,
		preserveBaseURL:       true,
		sso:                   true,
		authURLKeys:           []string{},
		createCodeKeys:        []string{"sessionId", "callbackUrl"},
		createRefreshKeys:     []string{"refreshToken"},
		reauthCodeKeys:        []string{"sessionId", "callbackUrl", "expectedConfigRevision"},
		reauthRefreshKeys:     []string{"refreshToken", "expectedConfigRevision"},
		parseCredentialsPatch: patch,
	}
	plan.authURL = func(ctx context.Context, s *Store, _ map[string]any, ownerID string) (map[string]any, error) {
		return s.generateGrokAuthURL(ownerID)
	}
	plan.exchangeCode = func(ctx context.Context, s *Store, body map[string]any, ownerID string) (*tokenOutcome, error) {
		sessionID, ok := requiredTrimmedString(body, "sessionId")
		if !ok {
			return nil, &grokOAuthError{Message: "sessionId 不能为空", StatusCode: 400}
		}
		callbackURL, ok := requiredTrimmedString(body, "callbackUrl")
		if !ok {
			return nil, &grokOAuthError{Message: "callbackUrl 不能为空", StatusCode: 400}
		}
		safePatch, patchOK := safePatch(plan, body)
		if !patchOK {
			return nil, &ValidationError{Message: "credentialsPatch 无效"}
		}
		info, err := s.exchangeGrokAuthorizationCode(ctx, sessionID, callbackURL, ownerID)
		if err != nil {
			return nil, err
		}
		return &tokenOutcome{
			Credentials: mergePatchLast(buildGrokOAuthCredentials(info, ""), safePatch),
			Name:        info.Email,
		}, nil
	}
	plan.exchangeRefresh = func(ctx context.Context, s *Store, body map[string]any) (*tokenOutcome, error) {
		refreshToken, ok := requiredTrimmedString(body, "refreshToken")
		if !ok {
			return nil, &grokOAuthError{Message: "refreshToken 不能为空", StatusCode: 400}
		}
		safePatch, patchOK := safePatch(plan, body)
		if !patchOK {
			return nil, &ValidationError{Message: "credentialsPatch 无效"}
		}
		info, err := s.refreshGrokToken(ctx, refreshToken, "")
		if err != nil {
			return nil, err
		}
		return &tokenOutcome{
			Credentials: mergePatchLast(buildGrokOAuthCredentials(info, refreshToken), safePatch),
			Name:        info.Email,
		}, nil
	}
	plan.refreshStored = func(ctx context.Context, s *Store, current *rotationAccount) (map[string]any, error) {
		refreshToken := stringCredential(current.Credentials, "refresh_token")
		if refreshToken == "" {
			return nil, &grokOAuthError{Message: "Grok OAuth 账户缺少 Refresh Token", StatusCode: 400}
		}
		info, err := s.refreshGrokToken(ctx, refreshToken, stringCredential(current.Credentials, "client_id"))
		if err != nil {
			return nil, err
		}
		return buildGrokOAuthCredentials(info, ""), nil
	}
	plan.refreshInput = func(ctx context.Context, s *Store, body map[string]any, current *rotationAccount) (map[string]any, error) {
		refreshToken, ok := requiredTrimmedString(body, "refreshToken")
		if !ok {
			return nil, &grokOAuthError{Message: "refreshToken 不能为空", StatusCode: 400}
		}
		info, err := s.refreshGrokToken(ctx, refreshToken, stringCredential(current.Credentials, "client_id"))
		if err != nil {
			return nil, err
		}
		return buildGrokOAuthCredentials(info, refreshToken), nil
	}
	return plan
}

// textFrom renders a string entry of a patch map ("" when absent/typed).
func textFrom(patch map[string]any, key string) string {
	if patch == nil {
		return ""
	}
	if text, ok := patch[key].(string); ok {
		return text
	}
	return ""
}

// validateGeminiBodyFields mirrors the gemini zod contracts the generic body
// walker cannot express: the oauthType enum and the baseUrl URL format.
func validateGeminiBodyFields(body map[string]any) error {
	if raw := optionalTrimmedText(body, "oauthType"); raw != "" {
		switch raw {
		case "code_assist", "google_one", "ai_studio":
		default:
			return &ValidationError{Message: "oauthType 无效"}
		}
	}
	if raw := optionalTrimmedText(body, "baseUrl"); raw != "" && !isHTTPURL(raw) {
		return &ValidationError{Message: "baseUrl 必须是有效 URL"}
	}
	return nil
}
