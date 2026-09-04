// URL sanitization ported from shared/request-context.ts sanitizeUrlForLog:
// only the OAuth authorize/device paths have their sensitive query parameters
// replaced with '[redacted]'; every other URL passes through untouched, and an
// unparseable URL is returned verbatim.
package publicapilogs

import "net/url"

// oauthSensitiveParamNames mirrors the sensitiveNames set.
var oauthSensitiveParamNames = []string{
	"state",
	"nonce",
	"code_challenge",
	"transaction_id",
	"user_code",
}

// sanitizeURLForLog mirrors sanitizeUrlForLog. On the two OAuth paths Node
// re-serializes the query through URLSearchParams, so brackets, spaces and
// reserved characters are re-encoded there (mirrored by url.Values.Encode);
// other paths keep the caller's original bytes.
func sanitizeURLForLog(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return value
	}
	// Node resolves the value against http://localhost and compares pathname;
	// a value without a path parses with an empty one, so treat "" as "/".
	pathname := parsed.EscapedPath()
	if pathname == "" {
		pathname = "/"
	}
	if pathname != "/oauth/authorize" && pathname != "/oauth/device" {
		return value
	}
	query := parsed.Query()
	for _, name := range oauthSensitiveParamNames {
		if _, ok := query[name]; ok {
			query[name] = []string{"[redacted]"}
		}
	}
	redacted := query.Encode()
	if redacted != "" {
		return pathname + "?" + redacted
	}
	return pathname
}
