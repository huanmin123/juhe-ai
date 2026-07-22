// Package gatewaycredentials extracts inbound gateway credentials from
// transport-neutral request values. It intentionally has no HTTP, database,
// cache, logging, or routing dependencies.
package gatewaycredentials

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxCredentialBytes = 4096

var (
	// ErrMissingCredential indicates that no eligible credential was supplied.
	ErrMissingCredential = errors.New("gateway credential missing")
	// ErrMalformedCredential indicates that an eligible credential source had an invalid format.
	ErrMalformedCredential = errors.New("gateway credential malformed")
	// ErrAmbiguousCredential indicates that one credential source was supplied more than once.
	ErrAmbiguousCredential = errors.New("gateway credential ambiguous")
)

// Source identifies the transport field selected by Extract.
type Source string

const (
	SourceBearer       Source = "authorization_bearer"
	SourceXAPIKey      Source = "x_api_key"
	SourceGeminiHeader Source = "x_goog_api_key"
	SourceGeminiQuery  Source = "gemini_query_key"
)

// Input contains already-extracted request values. Slices preserve repeated
// header and query fields so a transport adapter cannot silently choose one.
// GeminiHeaderKey and GeminiQueryKey are eligible only when GeminiNative is
// true; determining protocol ownership remains outside this package.
type Input struct {
	Authorization   []string
	XAPIKey         []string
	GeminiHeaderKey []string
	GeminiQueryKey  []string
	GeminiNative    bool
}

// Credential is the selected credential. Callers must not include the value
// returned by Secret in logs, responses, errors, metrics, or trace attributes.
type Credential struct {
	secret string
	Source Source
}

// Secret returns the credential for the authentication boundary. Keeping the
// field private prevents accidental JSON serialization and default formatting.
func (c Credential) Secret() string {
	return c.secret
}

// String intentionally omits the credential secret.
func (c Credential) String() string {
	return "gateway credential (" + string(c.Source) + ")"
}

// GoString intentionally omits the credential secret from %#v formatting.
func (c Credential) GoString() string {
	return c.String()
}

// Extract applies the compatible source priority used by the Node gateway:
// Authorization Bearer, x-api-key, Gemini x-goog-api-key, then Gemini key
// query. A malformed selected source is rejected rather than falling through.
// This prevents a malformed higher-priority credential from changing which
// lower-priority credential authenticates the request.
func Extract(input Input) (Credential, error) {
	if secret, present, err := bearerCredential(input.Authorization); err != nil {
		return Credential{}, err
	} else if present {
		return Credential{secret: secret, Source: SourceBearer}, nil
	}

	if secret, present, err := opaqueCredential(input.XAPIKey); err != nil {
		return Credential{}, err
	} else if present {
		return Credential{secret: secret, Source: SourceXAPIKey}, nil
	}

	if input.GeminiNative {
		if secret, present, err := opaqueCredential(input.GeminiHeaderKey); err != nil {
			return Credential{}, err
		} else if present {
			return Credential{secret: secret, Source: SourceGeminiHeader}, nil
		}

		if secret, present, err := opaqueCredential(input.GeminiQueryKey); err != nil {
			return Credential{}, err
		} else if present {
			return Credential{secret: secret, Source: SourceGeminiQuery}, nil
		}
	}

	return Credential{}, ErrMissingCredential
}

func bearerCredential(values []string) (secret string, present bool, err error) {
	value, present, err := singleNonEmptyValue(values)
	if err != nil || !present {
		return "", present, err
	}
	if strings.Contains(value, ",") {
		return "", true, ErrAmbiguousCredential
	}

	scheme, secret, found := strings.Cut(value, " ")
	secret = strings.TrimLeft(secret, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || !safeCredential(secret) {
		return "", true, ErrMalformedCredential
	}
	return secret, true, nil
}

func opaqueCredential(values []string) (secret string, present bool, err error) {
	value, present, err := singleNonEmptyValue(values)
	if err != nil || !present {
		return "", present, err
	}
	if strings.Contains(value, ",") {
		return "", true, ErrAmbiguousCredential
	}
	if !safeCredential(value) {
		return "", true, ErrMalformedCredential
	}
	return value, true, nil
}

func singleNonEmptyValue(values []string) (value string, present bool, err error) {
	if len(values) > 1 {
		return "", true, ErrAmbiguousCredential
	}
	if len(values) == 0 {
		return "", false, nil
	}
	value = strings.Trim(values[0], " \t")
	return value, value != "", nil
}

func safeCredential(value string) bool {
	if value == "" || len(value) > maxCredentialBytes || !utf8.ValidString(value) {
		return false
	}
	return strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) < 0
}
