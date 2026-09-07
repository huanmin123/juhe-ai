// Route-level contract validation for the authz slice, mirroring the Zod
// schemas of authorizations.routes.ts and shared/http.ts parseOrBadRequest:
// the first issue message (in schema-field declaration order) is rendered
// verbatim as 400 {message}.
package authz

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// usageStatsDatePattern mirrors the shared `^\d{4}-\d{2}-\d{2}$` regex used by
// every usage/list startDate/endDate schema.
var usageStatsDatePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// jsonBodyOnly mirrors kernel's express.json() media-type gate: only the exact
// type application/json (parameters stripped, case-insensitive) is parsed.
func jsonBodyOnly(header http.Header) bool {
	contentType := header.Get("Content-Type")
	if contentType == "" {
		return false
	}
	mediaType := strings.TrimSpace(contentType)
	if index := strings.IndexByte(mediaType, ';'); index >= 0 {
		mediaType = strings.TrimSpace(mediaType[:index])
	}
	return strings.EqualFold(mediaType, "application/json")
}

// requestHasBody mirrors type-is hasBody: a transfer-encoding header or a
// numeric content-length header.
func requestHasBody(r *http.Request) bool {
	if r.Header.Get("Transfer-Encoding") != "" {
		return true
	}
	contentLength := r.Header.Get("Content-Length")
	if contentLength == "" {
		return false
	}
	_, err := strconv.ParseInt(strings.TrimSpace(contentLength), 10, 64)
	return err == nil
}

// readJSONBody mirrors kernel.DecodeJSON's transport handling (media type
// gate, MaxBytesReader 413, malformed JSON 400) so strict decoding keeps the
// Express.json() + handleJsonBodyError contract. An absent or unparsed body
// returns (nil, true) and leaves the field-presence decision to the caller,
// matching Express leaving the body unparsed.
func readJSONBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if !jsonBodyOnly(r.Header) || !requestHasBody(r) {
		return nil, true
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.Header().Set("Cache-Control", "no-store")
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			kernel.WriteError(w, http.StatusRequestEntityTooLarge, "请求体过大")
		} else {
			kernel.WriteError(w, http.StatusBadRequest, "请求体无效")
		}
		return nil, false
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, true
	}
	return body, true
}

// decodeStrictJSON mirrors the Zod .strict() object parse used by the create
// (authorizations.routes.ts:106), update (:123-133), expire (:135-144) and
// mutation-version (:33-35) schemas: unknown keys reject the request with the
// zod default unrecognized-keys message (firstIssueMessage renders it
// verbatim). Non-JSON or empty bodies fall through; the per-field checks then
// produce the missing-field error like zod parsing {}.
func decodeStrictJSON(w http.ResponseWriter, r *http.Request, target any, allowedKeys map[string]bool) bool {
	body, ok := readJSONBody(w, r)
	if !ok {
		return false
	}
	if len(body) == 0 {
		return true
	}
	var present map[string]json.RawMessage
	if err := json.Unmarshal(body, &present); err != nil {
		w.Header().Set("Cache-Control", "no-store")
		kernel.WriteError(w, http.StatusBadRequest, "请求体无效")
		return false
	}
	var unknown []string
	for key := range present {
		if !allowedKeys[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sortStrings(unknown)
		quoted := make([]string, 0, len(unknown))
		for _, key := range unknown {
			quoted = append(quoted, "'"+key+"'")
		}
		// zod v3 unrecognized_keys default message; Node returns the English
		// zod text verbatim, so flag the response as upstream to keep the
		// gateway localizer from rewriting it.
		kernel.MarkUpstreamError(w)
		kernel.WriteBadRequest(w, "Unrecognized key(s) in object: "+strings.Join(quoted, ", "))
		return false
	}
	if err := json.Unmarshal(body, target); err != nil {
		w.Header().Set("Cache-Control", "no-store")
		kernel.WriteError(w, http.StatusBadRequest, "请求体无效")
		return false
	}
	return true
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// queryParser accumulates the first zod-style issue while mirroring the field
// declaration order of the Node schemas (parseOrBadRequest reports
// error.issues[0].message).
type queryParser struct {
	query url.Values
	issue string
}

func newQueryParser(r *http.Request) *queryParser {
	return &queryParser{query: r.URL.Query()}
}

func (p *queryParser) present(name string) bool {
	_, ok := p.query[name]
	return ok
}

// text mirrors z.string().trim().min(1, message).optional(): a missing key is
// unset, a present key must survive trimming.
func (p *queryParser) text(name, emptyMessage string) (string, bool) {
	if !p.present(name) {
		return "", true
	}
	value := strings.TrimSpace(p.query.Get(name))
	if value == "" {
		p.fail(emptyMessage)
		return "", false
	}
	return value, true
}

// enum mirrors z.enum([...]).optional() with the zod default invalid_enum
// message.
func (p *queryParser) enum(name string, allowed ...string) (string, bool) {
	if !p.present(name) {
		return "", true
	}
	value := p.query.Get(name)
	for _, candidate := range allowed {
		if value == candidate {
			return value, true
		}
	}
	quoted := make([]string, 0, len(allowed))
	for _, candidate := range allowed {
		quoted = append(quoted, "'"+candidate+"'")
	}
	p.fail("Invalid enum value. Expected " + strings.Join(quoted, " | ") + ", received '" + value + "'")
	return "", false
}

// date mirrors z.string().trim().regex(/^\d{4}-\d{2}-\d{2}$/, message).optional().
func (p *queryParser) date(name, formatMessage string) (string, bool) {
	if !p.present(name) {
		return "", true
	}
	value := strings.TrimSpace(p.query.Get(name))
	if !usageStatsDatePattern.MatchString(value) {
		p.fail(formatMessage)
		return "", false
	}
	return value, true
}

// int mirrors z.coerce.number().int().min(1, minMessage).max(max, maxMessage)
// .optional(): a missing key is unset, an empty string coerces to 0 and fails
// min(1), an unparseable value fails int() as received nan, and a fractional
// value fails int() with its numeric rendering.
func (p *queryParser) int(name, minMessage string, max int, maxMessage string) (int, bool) {
	if !p.present(name) {
		return 0, true
	}
	text := strings.TrimSpace(p.query.Get(name))
	if text == "" {
		p.fail(minMessage)
		return 0, false
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		p.fail("Expected integer, received nan")
		return 0, false
	}
	if value != float64(int64(value)) {
		p.fail("Expected integer, received " + strconv.FormatFloat(value, 'g', -1, 64))
		return 0, false
	}
	number := int(value)
	if number < 1 {
		p.fail(minMessage)
		return 0, false
	}
	if max > 0 && number > max {
		p.fail(maxMessage)
		return 0, false
	}
	return number, true
}

func (p *queryParser) fail(message string) {
	if p.issue == "" {
		p.issue = message
	}
}

// writeBadRequest renders the first accumulated issue and reports whether the
// request may continue. English zod default messages (invalid enum, integer
// casts) must survive the gateway localizer verbatim like the Node responses;
// Chinese messages pass through untouched either way.
func (p *queryParser) writeBadRequest(w http.ResponseWriter, fallback string) bool {
	if p.issue != "" {
		if !containsCJK(p.issue) {
			kernel.MarkUpstreamError(w)
		}
		kernel.WriteBadRequest(w, p.issue)
		return false
	}
	return true
}

// containsCJK reports whether the text carries any CJK ideograph (the
// kernel localizer keeps Chinese messages verbatim already).
func containsCJK(text string) bool {
	for _, runeValue := range text {
		if runeValue >= 0x4E00 && runeValue <= 0x9FFF {
			return true
		}
	}
	return false
}
