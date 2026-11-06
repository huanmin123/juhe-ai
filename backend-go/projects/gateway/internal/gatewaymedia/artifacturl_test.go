package gatewaymedia

import "testing"

func TestValidateArtifactURL(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{name: "https 绝对地址", raw: "https://upstream.example/content.mp4"},
		{name: "http 绝对地址", raw: "http://upstream.example/content.mp4?token=abc"},
		{name: "大写 scheme 归一", raw: "HTTPS://upstream.example/content.mp4"},
		{name: "前后空白", raw: "  https://upstream.example/content.mp4  "},
		{name: "空串视为无产物定位", raw: ""},
		{name: "纯空白视为无产物定位", raw: "   "},
		{name: "javascript scheme", raw: "javascript:alert(1)", wantErr: true},
		{name: "data scheme", raw: "data:text/html;base64,PHNjcmlwdD4=", wantErr: true},
		{name: "file scheme", raw: "file:///etc/passwd", wantErr: true},
		{name: "相对路径", raw: "/content/video.mp4", wantErr: true},
		{name: "无 scheme 裸域名", raw: "upstream.example/content.mp4", wantErr: true},
		{name: "http 无 host", raw: "http:///content.mp4", wantErr: true},
		{name: "https 仅 scheme", raw: "https://", wantErr: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := ValidateArtifactURL(testCase.raw)
			if testCase.wantErr && err == nil {
				t.Fatalf("ValidateArtifactURL(%q) = nil, want error", testCase.raw)
			}
			if !testCase.wantErr && err != nil {
				t.Fatalf("ValidateArtifactURL(%q) = %v, want nil", testCase.raw, err)
			}
		})
	}
}

func TestSafeArtifactURL(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{name: "https 保留原值", raw: "https://upstream.example/content.mp4?expires=1", want: "https://upstream.example/content.mp4?expires=1", ok: true},
		{name: "http 保留原值", raw: "http://upstream.example/content.mp4", want: "http://upstream.example/content.mp4", ok: true},
		{name: "去空白后返回", raw: "  https://upstream.example/content.mp4  ", want: "https://upstream.example/content.mp4", ok: true},
		{name: "空串", raw: "", want: "", ok: false},
		{name: "纯空白", raw: "   ", want: "", ok: false},
		{name: "javascript 拒绝", raw: "javascript:alert(1)", want: "", ok: false},
		{name: "data 拒绝", raw: "data:text/plain,hello", want: "", ok: false},
		{name: "相对路径拒绝", raw: "/content/video.mp4", want: "", ok: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := SafeArtifactURL(testCase.raw)
			if got != testCase.want || ok != testCase.ok {
				t.Fatalf("SafeArtifactURL(%q) = (%q, %v), want (%q, %v)", testCase.raw, got, ok, testCase.want, testCase.ok)
			}
		})
	}
}
