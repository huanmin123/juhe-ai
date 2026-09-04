package authsys

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbeListHTTP(t *testing.T) {
	deps, k, server := newTestEnv(t)
	seedAccount(t, deps, "admin1", "super-secret", "super_admin")
	var jar []*http.Cookie
	do := func(req *http.Request) (int, string) {
		for _, c := range jar {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		k.Handler().ServeHTTP(rec, req)
		jar = append(jar, rec.Result().Cookies()...)
		raw, _ := io.ReadAll(rec.Body)
		return rec.Code, string(raw)
	}
	loginReq, _ := http.NewRequest(http.MethodPost, server.URL+"/__aisys__/api/auth/login", strings.NewReader(`{"username":"admin1","password":"super-secret"}`))
	code, body := do(loginReq)
	t.Logf("login: %d %s", code, body)
	var cookie string
	for _, c := range rangeCookies(body) {
		cookie = c
	}
	dupReq, _ := http.NewRequest(http.MethodPost, server.URL+"/__aisys__/api/system-accounts", strings.NewReader(`{"username":"SECOND","displayName":"Other_Name","password":"second-pass"}`))
	dupReq.Header.Set("Content-Type", "application/json")
	dupReq.Header.Set("Cookie", cookie)
	code, body = do(dupReq)
	t.Logf("duplicate: %d %s", code, body)
	listReq, _ := http.NewRequest(http.MethodGet, server.URL+"/__aisys__/api/system-accounts?page=1&pageSize=20", nil)
	listReq.Header.Set("Cookie", cookie)
	code, body = do(listReq)
	t.Logf("list: %d %s", code, body)
}

func rangeCookies(body string) []string {
	// parse Set-Cookie headers is done via response; simplified: capture via helper below
	return parseSetCookies(body)
}

func parseSetCookies(body string) []string {
	_ = body
	return nil
}
