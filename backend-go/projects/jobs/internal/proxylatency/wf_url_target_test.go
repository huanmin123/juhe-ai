package proxylatency

import (
	"strings"
	"testing"
)

func TestWFParseTargetURLRejects(t *testing.T) {
	for _, bad := range []string{
		"http://x.invalid/a%2Fb",                         // 路径携带编码斜杠
		"http://x.invalid/a/../b",                        // 点段
		"http://x.invalid/a\b",                           // 反斜杠
		"http://user:pw@x.invalid/v1",                    // 携带 userinfo
		"http://x.invalid/v1?query=1",                    // 携带查询
		"http://x.invalid/v1#fragment",                   // 携带片段
		"http://%x.invalid/v1",                           // host 携带百分号
		"http://x.invalid:0/v1",                          // 端口 0
		"https://" + strings.Repeat("a", 64) + ".com/v1", // label 超长
	} {
		if _, err := parseTargetURL(bad); err == nil {
			t.Fatalf("%q 必须拒绝", bad)
		}
	}
	if _, err := parseTargetURL("http://x.invalid" + strings.Repeat("/segment", 1)); err != nil {
		t.Fatal("合法路径不得拒绝")
	}
}

