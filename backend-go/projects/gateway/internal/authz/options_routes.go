package authz

// Authorization-options family (Node
// backend/src/modules/authorization-options/authorization-options.routes.ts +
// storage/authorization-options.repository.ts). Three grantee option reads
// mounted on both the admin /authorization-options prefix (requireAdmin) and
// the forceSelfAccessScope /my-authorization-options prefix. The option
// queries themselves are scope-free (Node's repo functions void the access
// scope); the surface only decides visibility of the route.

import (
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// MountAuthorizationOptions wires the grantee option route family.
func (d *Deps) MountAuthorizationOptions(k *kernel.Kernel) {
	prefix := "/__aisys__/api"
	admin := d.RequireAdmin
	self := d.RequireSelf

	for _, surface := range []struct {
		base string
		wrap func(http.Handler) http.Handler
	}{
		{prefix + "/authorization-options", admin},
		{prefix + "/my-authorization-options", self},
	} {
		base := surface.base
		wrap := surface.wrap
		k.Register("GET "+base+"/grantee-accounts", wrap(http.HandlerFunc(d.granteeAccounts)))
		k.Register("GET "+base+"/grantee-teams", wrap(http.HandlerFunc(d.granteeTeams)))
		k.Register("GET "+base+"/grantee-groups", wrap(http.HandlerFunc(d.granteeGroups)))
	}
}

func (d *Deps) granteeAccounts(w http.ResponseWriter, r *http.Request) {
	options := parseAuthorizationOptionListOptions(r.URL.Query())
	rows, err := d.Store.ListAuthorizationGranteeAccounts(r.Context(), options)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	kernel.WriteOK(w, rows, "")
}

func (d *Deps) granteeTeams(w http.ResponseWriter, r *http.Request) {
	options := parseAuthorizationOptionListOptions(r.URL.Query())
	rows, err := d.Store.ListAuthorizationGranteeTeams(r.Context(), options)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	kernel.WriteOK(w, rows, "")
}

func (d *Deps) granteeGroups(w http.ResponseWriter, r *http.Request) {
	options := parseAuthorizationGranteeGroupOptionListOptions(r.URL.Query())
	if options.GranteeSystemAccountID == "" {
		kernel.WriteBadRequest(w, "被授权用户不能为空")
		return
	}
	rows, err := d.Store.ListAuthorizationGranteeGroups(r.Context(), options)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	kernel.WriteOK(w, rows, "")
}

// parseAuthorizationOptionListOptions mirrors
// parseAuthorizationOptionListOptions: comma-splitting ids (max 50), trimmed
// keyword, limit clamped to 1..50 (default 50).
func parseAuthorizationOptionListOptions(values url.Values) authorizationPrincipalOptionListOptions {
	return authorizationPrincipalOptionListOptions{
		IDs:     queryTextList(values["ids"], 50),
		Keyword: strings.TrimSpace(values.Get("keyword")),
		Limit:   optionLimitValue(integerQueryOrZero(values.Get("limit")), integerQueryPresent(values.Get("limit"))),
	}
}

// parseAuthorizationGranteeGroupOptionListOptions mirrors the group variant.
func parseAuthorizationGranteeGroupOptionListOptions(values url.Values) authorizationGranteeGroupOptionListOptions {
	base := parseAuthorizationOptionListOptions(values)
	return authorizationGranteeGroupOptionListOptions{
		authorizationPrincipalOptionListOptions: base,
		GranteeSystemAccountID:                  strings.TrimSpace(values.Get("granteeSystemAccountId")),
		ProviderCode:                            strings.TrimSpace(values.Get("providerCode")),
		PreferDefault:                           booleanQueryValue(values.Get("preferDefault")),
		HasPreferDefault:                        values.Has("preferDefault"),
	}
}

// queryTextList mirrors shared/query-values.ts queryTextList: comma-splitting
// within each repeated value, trim, dedupe in first-seen order, capped.
func queryTextList(raw []string, maxItems int) []string {
	if maxItems < 1 {
		maxItems = 1
	}
	seen := map[string]bool{}
	normalized := []string{}
	for _, value := range raw {
		for _, item := range strings.Split(value, ",") {
			text := strings.TrimSpace(item)
			if text == "" || seen[text] {
				continue
			}
			seen[text] = true
			normalized = append(normalized, text)
			if len(normalized) >= maxItems {
				return normalized
			}
		}
	}
	return normalized
}

func integerQueryOrZero(raw string) int {
	text := strings.TrimSpace(raw)
	if text == "" {
		return 0
	}
	value := 0
	isDigits := text != ""
	for _, char := range text {
		if char < '0' || char > '9' {
			isDigits = false
			break
		}
		value = value*10 + int(char-'0')
	}
	if !isDigits {
		return 0
	}
	return value
}

func integerQueryPresent(raw string) bool {
	return strings.TrimSpace(raw) != ""
}

// optionLimitValue mirrors optionLimitValue: absent keeps the repo default 50.
func optionLimitValue(value int, present bool) int {
	if !present {
		return 50
	}
	if value < 1 {
		return 1
	}
	if value > 50 {
		return 50
	}
	return value
}

// booleanQueryValue mirrors booleanQueryValue: 1/true/yes -> true, 0/false/no
// -> false, anything else undefined.
func booleanQueryValue(raw string) *bool {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	switch normalized {
	case "1", "true", "yes":
		value := true
		return &value
	case "0", "false", "no":
		value := false
		return &value
	default:
		return nil
	}
}

// normalizeTextList mirrors normalizeTextList in the option repository.
func normalizeTextList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]bool{}
	normalized := []string{}
	for _, value := range values {
		text := strings.TrimSpace(value)
		if text == "" || seen[text] {
			continue
		}
		seen[text] = true
		normalized = append(normalized, text)
	}
	sort.Strings(normalized)
	if len(normalized) > 50 {
		normalized = normalized[:50]
	}
	return normalized
}

// textPrefixUpperBound mirrors the repository's textPrefixUpperBound.
func textPrefixUpperBound(value string) string {
	chars := []rune(value)
	for index := len(chars) - 1; index >= 0; index-- {
		codePoint := chars[index]
		if codePoint >= 0x10ffff {
			continue
		}
		return string(chars[:index]) + string(codePoint+1)
	}
	return value + "\U0010ffff"
}

type authorizationPrincipalOptionListOptions struct {
	IDs     []string
	Keyword string
	Limit   int
}

type authorizationGranteeGroupOptionListOptions struct {
	authorizationPrincipalOptionListOptions
	GranteeSystemAccountID string
	ProviderCode           string
	PreferDefault          *bool
	HasPreferDefault       bool
}
