package publicapi

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

type apiDocsCatalogSnapshot struct {
	BasePath string                `json:"basePath"`
	AuthType string                `json:"authType"`
	Items    []apiDocsItemSnapshot `json:"items"`
}

type apiDocsItemSnapshot struct {
	ID              string                  `json:"id"`
	Name            string                  `json:"name"`
	Summary         string                  `json:"summary"`
	Status          string                  `json:"status"`
	Method          string                  `json:"method"`
	Path            string                  `json:"path"`
	Scope           string                  `json:"scope"`
	Headers         []apiDocsHeaderSnapshot `json:"headers"`
	Query           []apiDocsFieldSnapshot  `json:"query"`
	RequestBody     *apiDocsBodySnapshot    `json:"requestBody"`
	ResponseFields  []apiDocsFieldSnapshot  `json:"responseFields"`
	ResponseExample json.RawMessage         `json:"responseExample"`
}

type apiDocsHeaderSnapshot struct {
	Name        string `json:"name"`
	Required    bool   `json:"required"`
	Description string `json:"description"`
	Example     string `json:"example"`
}

type apiDocsFieldSnapshot struct {
	Name        string          `json:"name"`
	Type        string          `json:"type"`
	Required    bool            `json:"required"`
	Description string          `json:"description"`
	Example     json.RawMessage `json:"example"`
}

type apiDocsBodySnapshot struct {
	ContentType string                 `json:"contentType"`
	Fields      []apiDocsFieldSnapshot `json:"fields"`
	Example     json.RawMessage        `json:"example"`
}

func TestAPIDocsCatalogMatchesEndpoints(t *testing.T) {
	catalog := decodeAPIDocsCatalog(t)

	if catalog.BasePath != Prefix {
		t.Fatalf("basePath = %q, want %q", catalog.BasePath, Prefix)
	}
	if catalog.AuthType != AuthTypeBearer {
		t.Fatalf("authType = %q, want %q", catalog.AuthType, AuthTypeBearer)
	}

	endpoints := Endpoints()
	if got, want := len(catalog.Items), 16; got != want {
		t.Fatalf("len(items) = %d, want %d", got, want)
	}
	if len(catalog.Items) != len(endpoints) {
		t.Fatalf("len(items) = %d, len(Endpoints()) = %d", len(catalog.Items), len(endpoints))
	}

	for i, item := range catalog.Items {
		endpoint := endpoints[i]
		if item.ID != endpoint.ID || item.Method != endpoint.Method || item.Path != endpoint.Path || item.Scope != endpoint.Scope {
			t.Errorf(
				"item[%d] endpoint = {%q %q %q %q}, want {%q %q %q %q}",
				i,
				item.ID,
				item.Method,
				item.Path,
				item.Scope,
				endpoint.ID,
				endpoint.Method,
				endpoint.Path,
				endpoint.Scope,
			)
		}
	}
}

func TestAPIDocsCatalogKeepsRichDocumentation(t *testing.T) {
	catalog := decodeAPIDocsCatalog(t)
	validStatuses := map[string]bool{"available": true, "mock": true}
	queryExamples := 0
	bodyFieldExamples := 0
	responseFieldExamples := 0

	for _, item := range catalog.Items {
		if strings.TrimSpace(item.Name) == "" {
			t.Errorf("item %q has no name", item.ID)
		}
		if strings.TrimSpace(item.Summary) == "" {
			t.Errorf("item %q has no summary", item.ID)
		}
		if !validStatuses[item.Status] {
			t.Errorf("item %q status = %q", item.ID, item.Status)
		}
		if len(item.Headers) == 0 {
			t.Errorf("item %q has no headers", item.ID)
		}
		for i, header := range item.Headers {
			if strings.TrimSpace(header.Name) == "" || strings.TrimSpace(header.Description) == "" || strings.TrimSpace(header.Example) == "" {
				t.Errorf("item %q header[%d] is incomplete: %+v", item.ID, i, header)
			}
			if !header.Required {
				t.Errorf("item %q header[%d] is not required", item.ID, i)
			}
		}

		queryExamples += validateAPIDocsFields(t, item.ID, "query", item.Query)
		if len(item.ResponseFields) == 0 {
			t.Errorf("item %q has no responseFields", item.ID)
		}
		responseFieldExamples += validateAPIDocsFields(t, item.ID, "responseFields", item.ResponseFields)
		if !hasJSONValue(item.ResponseExample) {
			t.Errorf("item %q has no responseExample", item.ID)
		}

		switch item.Method {
		case "GET":
			if len(item.Query) == 0 {
				t.Errorf("GET item %q has no query documentation", item.ID)
			}
			if item.RequestBody != nil {
				t.Errorf("GET item %q unexpectedly has requestBody", item.ID)
			}
		case "POST":
			if item.RequestBody == nil {
				t.Errorf("POST item %q has no requestBody", item.ID)
				continue
			}
			if item.RequestBody.ContentType != "application/json" {
				t.Errorf("item %q requestBody contentType = %q", item.ID, item.RequestBody.ContentType)
			}
			if len(item.RequestBody.Fields) == 0 {
				t.Errorf("item %q requestBody has no fields", item.ID)
			}
			bodyFieldExamples += validateAPIDocsFields(t, item.ID, "requestBody.fields", item.RequestBody.Fields)
			if !hasJSONValue(item.RequestBody.Example) {
				t.Errorf("item %q requestBody has no example", item.ID)
			}
		default:
			t.Errorf("item %q method = %q, want GET or POST", item.ID, item.Method)
		}
	}

	if queryExamples == 0 {
		t.Error("query field examples were lost from the catalog")
	}
	if bodyFieldExamples == 0 {
		t.Error("request body field examples were lost from the catalog")
	}
	if responseFieldExamples == 0 {
		t.Error("response field examples were lost from the catalog")
	}
}

func TestAPIDocsCatalogReturnsDefensiveCopy(t *testing.T) {
	returned := APIDocsCatalog()
	if len(returned) == 0 {
		t.Fatal("APIDocsCatalog() returned an empty snapshot")
	}
	want := append(json.RawMessage(nil), returned...)

	for i := range returned {
		returned[i] = 0
	}

	fresh := APIDocsCatalog()
	if !bytes.Equal(fresh, want) {
		t.Fatal("mutating APIDocsCatalog() result changed the embedded snapshot")
	}
}

func decodeAPIDocsCatalog(t *testing.T) apiDocsCatalogSnapshot {
	t.Helper()
	raw := APIDocsCatalog()
	if !json.Valid(raw) {
		t.Fatal("APIDocsCatalog() returned invalid JSON")
	}

	var catalog apiDocsCatalogSnapshot
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatalf("decode APIDocsCatalog(): %v", err)
	}
	return catalog
}

func validateAPIDocsFields(t *testing.T, itemID string, section string, fields []apiDocsFieldSnapshot) int {
	t.Helper()
	examples := 0
	for i, field := range fields {
		if strings.TrimSpace(field.Name) == "" || strings.TrimSpace(field.Type) == "" || strings.TrimSpace(field.Description) == "" {
			t.Errorf("item %q %s[%d] is incomplete: %+v", itemID, section, i, field)
		}
		if hasJSONValue(field.Example) {
			examples++
		}
	}
	return examples
}

func hasJSONValue(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}
