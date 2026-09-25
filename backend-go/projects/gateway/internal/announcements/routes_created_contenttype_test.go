package announcements

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// The 201 create response must carry application/json: headers set after
// WriteHeader are dropped, so the create route sets them first.
func TestCreateAnnouncementSendsJSONContentType(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/announcements", `{"title":"ct-a","content":"正文"}`)
	if code != http.StatusCreated {
		t.Fatalf("create = %d (%v)", code, created)
	}
	if _, ok := created["data"].(map[string]any); !ok {
		t.Fatalf("envelope drift: %#v", created)
	}

	env.mu.Lock()
	jar := make(map[string]string, len(env.jar))
	for name, value := range env.jar {
		jar[name] = value
	}
	env.mu.Unlock()
	request, err := http.NewRequest(http.MethodPost, env.server.URL+"/__aisys__/api/announcements", strings.NewReader(`{"title":"ct-b","content":"正文"}`))
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
