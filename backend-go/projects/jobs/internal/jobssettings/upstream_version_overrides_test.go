package jobssettings

import (
	"context"
	"testing"
	"time"
)

// UpstreamClientVersionOverrides：过滤语义（合法项保留、未知键/非字符串/
// 坏 semver 忽略）、缺行回退默认 {}、非对象值返回空 map、坏 JSON 返回
// error（strict 读取，不降级默认）。
func TestUpstreamClientVersionOverridesFiltering(t *testing.T) {
	cases := []struct {
		name      string
		valueJSON string
		want      map[string]string
	}{
		{"合法对象保留全部", `{"codex":"0.160.0","claudeCode":"2.1.300","geminiCLI":"0.62.0","zcode":"3.15.0","grokCLI":"1.0.14"}`,
			map[string]string{"codex": "0.160.0", "claudeCode": "2.1.300", "geminiCLI": "0.62.0", "zcode": "3.15.0", "grokCLI": "1.0.14"}},
		{"未知家族键忽略", `{"codex":"0.160.0","other":"1.0.0"}`, map[string]string{"codex": "0.160.0"}},
		{"非字符串值忽略", `{"codex":1}`, map[string]string{}},
		{"坏 semver 忽略", `{"codex":"0.160","grokCLI":"1.0.14"}`, map[string]string{"grokCLI": "1.0.14"}},
		{"空对象", `{}`, map[string]string{}},
		{"数组值整体为空", `["codex"]`, map[string]string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			warn := &warnCollector{}
			db := newSettingsDB(t, true)
			insertSetting(t, db, "upstreamClientVersionOverrides", tc.valueJSON)
			source := newTestSource(db, warn, func() time.Time { return time.Unix(0, 0) })
			got, err := source.UpstreamClientVersionOverrides(context.Background())
			if err != nil {
				t.Fatalf("err=%v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got=%v want=%v", got, tc.want)
			}
			for key, value := range tc.want {
				if got[key] != value {
					t.Fatalf("got=%v want=%v", got, tc.want)
				}
			}
		})
	}
}

func TestUpstreamClientVersionOverridesMissingRowFallsBackToEmpty(t *testing.T) {
	warn := &warnCollector{}
	db := newSettingsDB(t, true)
	source := newTestSource(db, warn, func() time.Time { return time.Unix(0, 0) })
	got, err := source.UpstreamClientVersionOverrides(context.Background())
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(got) != 0 {
		t.Fatalf("missing row must fall back to empty, got=%v", got)
	}
}

// strict 读取：坏 JSON 必须返回 error（不得降级成默认 {} 静默清空已配置
// 的覆盖）。
func TestUpstreamClientVersionOverridesBadJSONReturnsError(t *testing.T) {
	warn := &warnCollector{}
	db := newSettingsDB(t, true)
	insertSetting(t, db, "upstreamClientVersionOverrides", `{not-json`)
	source := newTestSource(db, warn, func() time.Time { return time.Unix(0, 0) })
	if _, err := source.UpstreamClientVersionOverrides(context.Background()); err == nil {
		t.Fatal("bad JSON must return an error (strict read), got nil")
	}
}

// strict 读取：DB 层读错误必须返回 error（PG 瞬断时调用方保持既有覆盖）。
func TestUpstreamClientVersionOverridesReadErrorPropagates(t *testing.T) {
	warn := &warnCollector{}
	db := newSettingsDB(t, true)
	insertSetting(t, db, "upstreamClientVersionOverrides", `{"codex":"0.160.0"}`)
	source := newTestSource(db, warn, func() time.Time { return time.Unix(0, 0) })
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := source.UpstreamClientVersionOverrides(context.Background()); err == nil {
		t.Fatal("read failure must propagate (strict read), got nil")
	}
}
