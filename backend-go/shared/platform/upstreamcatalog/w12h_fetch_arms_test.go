package upstreamcatalog

// w12h 补充 arms：共享 client 直连/代理错误分类、请求构造失败、空响应、
// 有界读取错误、gemini google_oauth 头、非对象 payload 与条目过滤、
// IntersectModelIDs 的空串/去重与提前收口。

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestW12HGeminiOAuthHeaders(t *testing.T) {
	var gotHeader http.Header
	doer := &stubDoer{handler: func(r *http.Request) (*http.Response, error) {
		gotHeader = r.Header.Clone()
		return jsonResponse(http.StatusOK, `{"models":[{"name":"models/gemini-pro"}]}`)
	}}
	ids, err := FetchUpstreamModelIDs(context.Background(), FetchOptions{
		BaseURL: "https://w12h.invalid", ProtocolCode: "gemini", Credential: "w12h-token",
		CredentialType: "google_oauth", OAuthQuotaProjectID: "w12h-project", Doer: doer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotHeader.Get("Authorization") != "Bearer w12h-token" || gotHeader.Get("x-goog-user-project") != "w12h-project" || gotHeader.Get("x-goog-api-key") != "" {
		t.Fatalf("google_oauth 头不符: %v", gotHeader)
	}
	if len(ids) != 1 || ids[0] != "gemini-pro" {
		t.Fatalf("gemini 目录解析不符: %v", ids)
	}

	// 无 quota project 时不写 x-goog-user-project。
	doer.handler = func(r *http.Request) (*http.Response, error) {
		gotHeader = r.Header.Clone()
		return jsonResponse(http.StatusOK, `{"models":[]}`)
	}
	if _, err := FetchUpstreamModelIDs(context.Background(), FetchOptions{
		BaseURL: "https://w12h.invalid", ProtocolCode: "gemini", Credential: "w12h-token",
		CredentialType: "google_oauth", Doer: doer,
	}); err != nil {
		t.Fatal(err)
	}
	if gotHeader.Get("Authorization") != "Bearer w12h-token" || gotHeader.Get("x-goog-user-project") != "" {
		t.Fatalf("无 project 的 google_oauth 头不符: %v", gotHeader)
	}
}

func TestW12HSharedClientAndProxyErrorClassification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	}))
	defer server.Close()

	// doer=nil + 空 ProxyURL 走共享 client 直连。
	ids, err := FetchUpstreamModelIDs(context.Background(), FetchOptions{
		BaseURL: server.URL, ProtocolCode: "openai", Credential: "w12h-key",
	})
	if err != nil || len(ids) != 1 || ids[0] != "m1" {
		t.Fatalf("共享 client 直连失败: %v %v", ids, err)
	}

	// 不受支持的代理协议。
	_, err = FetchUpstreamModelIDs(context.Background(), FetchOptions{
		BaseURL: server.URL, ProtocolCode: "openai", Credential: "w12h-key",
		ProxyURL: "ftp://w12h.invalid:21",
	})
	if err == nil || !strings.Contains(err.Error(), "代理协议不受支持") {
		t.Fatalf("ftp 代理必须分类为不受支持: %v", err)
	}

	// 解析失败的代理 URL。
	_, err = FetchUpstreamModelIDs(context.Background(), FetchOptions{
		BaseURL: server.URL, ProtocolCode: "openai", Credential: "w12h-key",
		ProxyURL: "not-a-proxy-url",
	})
	if err == nil || !strings.Contains(err.Error(), "代理 URL 无效") {
		t.Fatalf("无效代理 URL 必须分类为无效: %v", err)
	}
}

func TestW12HFetchRequestConstructionAndEmptyResponseArms(t *testing.T) {
	// NewRequest 失败：base URL 是非法 URL。
	if _, err := FetchUpstreamModelIDs(context.Background(), FetchOptions{
		BaseURL: "http://[::1", ProtocolCode: "openai", Credential: "w12h-key",
	}); err == nil || !strings.Contains(err.Error(), "请求构造失败") {
		t.Fatalf("非法 endpoint 必须报请求构造失败: %v", err)
	}

	// Doer 返回 (nil, nil)：上游响应为空。
	if _, err := FetchUpstreamModelIDs(context.Background(), FetchOptions{
		BaseURL: "https://w12h.invalid", Credential: "w12h-key",
		Doer: &stubDoer{handler: func(r *http.Request) (*http.Response, error) { return nil, nil }},
	}); err == nil || !strings.Contains(err.Error(), "上游响应为空") {
		t.Fatalf("空响应必须报错: %v", err)
	}
}

func TestW12HFetchBoundedReadArms(t *testing.T) {
	// 响应超过大小限制。
	if _, err := FetchUpstreamModelIDs(context.Background(), FetchOptions{
		BaseURL: "https://w12h.invalid", Credential: "w12h-key", MaxBodyBytes: 1,
		Doer: &stubDoer{handler: func(r *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, `{"data":[]}`)
		}},
	}); err == nil || !strings.Contains(err.Error(), "超过大小限制") {
		t.Fatalf("超限响应必须报错: %v", err)
	}

	// 读取中途失败。
	if _, err := FetchUpstreamModelIDs(context.Background(), FetchOptions{
		BaseURL: "https://w12h.invalid", Credential: "w12h-key",
		Doer: &stubDoer{handler: func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(errReader{}),
				Header:     http.Header{},
			}, nil
		}},
	}); err == nil || !strings.Contains(err.Error(), "上游响应读取失败") {
		t.Fatalf("读取失败必须报错: %v", err)
	}
}

type errReader struct{}

func (errReader) Read(p []byte) (int, error) { return 0, errors.New("w12h read failure") }

func TestW12HPayloadShapeArms(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"非对象 payload", `[1,2]`, []string{}},
		{"data 含非对象条目", `{"data":[1,{"id":"a"},"skip"]}`, []string{"a"}},
		{"models 含非对象条目", `{"models":[7,{"name":"models/b"}]}`, []string{"b"}},
		{"data 空串与重复过滤", `{"data":[{"id":" "},{"id":"a"},{"id":"a"}]}`, []string{"a"}},
	}
	for _, test := range cases {
		ids, err := FetchUpstreamModelIDs(context.Background(), FetchOptions{
			BaseURL: "https://w12h.invalid", Credential: "w12h-key",
			Doer: &stubDoer{handler: func(r *http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusOK, test.body)
			}},
		})
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		if len(ids) != len(test.want) {
			t.Fatalf("%s: got %v want %v", test.name, ids, test.want)
		}
		for i := range test.want {
			if ids[i] != test.want[i] {
				t.Fatalf("%s: got %v want %v", test.name, ids, test.want)
			}
		}
	}

	// 缺少凭据的入口守卫。
	if _, err := FetchUpstreamModelIDs(context.Background(), FetchOptions{BaseURL: "https://w12h.invalid"}); err == nil {
		t.Fatal("缺少凭据必须报错")
	}
}

func TestW12HIntersectModelIDSSkipAndEarlyBreak(t *testing.T) {
	// 首目录的空串与重复条目被跳过，交集保序。
	got := IntersectModelIDs([][]string{{"", "a", "b", "a"}, {"b", "a"}})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("交集不符: %v", got)
	}
	// 某一目录为空时交集清空并提前收口。
	if got := IntersectModelIDs([][]string{{"a", "b"}, {}}); len(got) != 0 {
		t.Fatalf("空目录必须得到空交集: %v", got)
	}
}
