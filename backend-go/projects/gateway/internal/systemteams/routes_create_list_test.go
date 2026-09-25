package systemteams

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// The 201 create response must carry application/json: headers set after
// WriteHeader are dropped, so the create route sets them first.
func TestCreateRouteSendsJSONContentType(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "st-ct-admin", "st-password-01", "admin")
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/system-teams", `{"name":"st-ct-a"}`)
	if code != http.StatusCreated {
		t.Fatalf("create = %d (%v)", code, payload)
	}
	if _, ok := payload["data"].(map[string]any); !ok {
		t.Fatalf("envelope drift: %#v", payload)
	}

	env.mu.Lock()
	jar := env.jars["st-ct-admin"]
	env.mu.Unlock()
	request, err := http.NewRequest(http.MethodPost, env.server.URL+"/__aisys__/api/system-teams", strings.NewReader(`{"name":"st-ct-b"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	for name, value := range jar {
		request.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("raw create = %d", response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("201 Content-Type = %q", contentType)
	}
}

// List metadata must echo the store-normalized window, not the raw query
// (pageSize=1000 used to report itself verbatim).
func TestListMetadataUsesNormalizedWindow(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "st-norm-admin", "st-password-01", "admin")
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/system-teams?page=2&pageSize=1000", "")
	if code != http.StatusOK {
		t.Fatalf("list = %d (%v)", code, payload)
	}
	list, _ := payload["data"].(map[string]any)
	if list == nil {
		t.Fatalf("missing data envelope: %#v", payload)
	}
	if list["page"] != float64(2) || list["pageSize"] != float64(maxPageSize) {
		t.Fatalf("oversized window echo = %v/%v", list["page"], list["pageSize"])
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/system-teams?page=-1&pageSize=-3", "")
	if code != http.StatusOK {
		t.Fatalf("degenerate list = %d (%v)", code, payload)
	}
	list, _ = payload["data"].(map[string]any)
	if list["page"] != float64(1) || list["pageSize"] != float64(defaultPageSize) {
		t.Fatalf("degenerate window echo = %v/%v", list["page"], list["pageSize"])
	}
}

// keywordUpperBound must not wrap a trailing U+10FFFF to 0 (the bound would
// shrink below the prefix and hide matches), and a U+10FFFF keyword must not
// break the route.
func TestKeywordUpperBoundNoRuneOverflow(t *testing.T) {
	if keywordUpperBound("abc") != "abd" {
		t.Fatalf("plain bound = %q", keywordUpperBound("abc"))
	}
	if got := keywordUpperBound("a" + string(rune(0x10ffff))); got != "b" {
		t.Fatalf("trailing U+10FFFF bound = %q", got)
	}
	saturated := string(rune(0x10ffff)) + string(rune(0x10ffff))
	if got := keywordUpperBound(saturated); got != saturated+"\uffff" {
		t.Fatalf("saturated bound = %q", got)
	}

	env := newTestEnv(t)
	env.login(t, "st-kw-admin", "st-password-01", "admin")
	code, list := env.do(t, http.MethodGet, "/__aisys__/api/system-teams?keyword=%F4%8F%BF%BF", "")
	if code != http.StatusOK {
		t.Fatalf("U+10FFFF keyword list = %d (%v)", code, list)
	}
}
