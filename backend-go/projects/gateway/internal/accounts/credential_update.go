package accounts

import "strings"

// Credential patch/merge semantics ported from
// backend/src/modules/accounts/account-credential-update.ts (BUG-0175
// D-164/D-165): the credentialsPatch tri-state applies null keys as deletions
// before the normalization pipeline, and the legacy full-credentials submit
// pre-merges the multi-Key pool (single api_key replaces api_keys/strategy/
// weights; a pool submit drops the lone api_key).

// applyAccountCredentialsPatch mirrors applyAccountCredentialsPatch: a JSON
// null patch value deletes the key from the current record; every other value
// overwrites. The merged record then flows through the regular
// NormalizeAccountCredentialsForWrite pipeline, so deletion of an optional
// field simply leaves it out of the re-derived output instead of failing the
// 不能为空 validations.
func applyAccountCredentialsPatch(current, patch Credentials) Credentials {
	credentials := Credentials{}
	for key, value := range current {
		credentials[key] = value
	}
	for key, value := range patch {
		if value == nil {
			delete(credentials, key)
			continue
		}
		credentials[key] = value
	}
	return credentials
}

// mergeAccountCredentialsForUpdate mirrors mergeAccountCredentialsForUpdate:
// the legacy full-record submit keeps optional current fields unless the
// request carries a replacement, and api_key accounts resolve the
// single-key/pool replacement pair.
func mergeAccountCredentialsForUpdate(accountType string, current, requested Credentials) Credentials {
	credentials := Credentials{}
	for key, value := range current {
		credentials[key] = value
	}
	for key, value := range requested {
		credentials[key] = value
	}
	preserveCredentialText(credentials, current, "base_url")
	preserveCredentialArray(credentials, current, "supported_endpoint_modes")
	switch accountType {
	case "api_key":
		replacesWithSingleKey := hasCredentialText(requested["api_key"]) && !hasCredentialStringArray(requested["api_keys"])
		replacesWithMultipleKeys := hasCredentialStringArray(requested["api_keys"])
		if replacesWithSingleKey {
			delete(credentials, "api_keys")
			delete(credentials, "api_key_strategy")
			delete(credentials, "api_key_weights")
		} else if replacesWithMultipleKeys {
			delete(credentials, "api_key")
			preserveCredentialText(credentials, current, "api_key_strategy")
			preserveCredentialArray(credentials, current, "api_key_weights")
		} else {
			preserveCredentialText(credentials, current, "api_key")
			preserveCredentialArray(credentials, current, "api_keys")
			preserveCredentialText(credentials, current, "api_key_strategy")
			preserveCredentialArray(credentials, current, "api_key_weights")
		}
	case "oauth":
		for _, key := range []string{
			"access_token", "refresh_token", "expires_at", "client_id", "id_token",
			"token_type", "scope", "email", "account_id", "organization_id",
			"chatgpt_user_id", "plan_type", "sub", "team_id", "subscription_tier",
			"entitlement_status",
		} {
			preserveCredentialText(credentials, current, key)
		}
	case "google_oauth":
		for _, key := range []string{
			"access_token", "refresh_token", "expires_at", "client_id",
			"client_secret", "quota_project_id", "oauth_type", "project_id",
			"tier_id", "scope", "token_type", "drive_storage_limit",
			"drive_storage_usage", "drive_tier_updated_at",
		} {
			preserveCredentialText(credentials, current, key)
		}
	}
	return credentials
}

// preserveCredentialText mirrors preserveCredentialText: a non-empty text
// output value wins; otherwise the current text value is restored.
func preserveCredentialText(output, source Credentials, key string) {
	if hasCredentialText(output[key]) {
		return
	}
	if value := source[key]; hasCredentialText(value) {
		output[key] = value
	}
}

// preserveCredentialArray mirrors preserveCredentialArray: a non-empty array
// output value wins; otherwise the current array value is restored.
func preserveCredentialArray(output, source Credentials, key string) {
	if list, ok := output[key].([]any); ok && len(list) > 0 {
		return
	}
	if value, ok := source[key].([]any); ok && len(value) > 0 {
		output[key] = value
	}
}

// hasCredentialText mirrors hasCredentialText.
func hasCredentialText(value any) bool {
	text, ok := value.(string)
	return ok && strings.TrimSpace(text) != ""
}

// hasCredentialStringArray mirrors hasCredentialStringArray: at least one
// non-empty text entry.
func hasCredentialStringArray(value any) bool {
	list, ok := value.([]any)
	if !ok {
		return false
	}
	for _, item := range list {
		if hasCredentialText(item) {
			return true
		}
	}
	return false
}
