package accounthealth

import (
	"context"
	"testing"
	"time"
)

// TestLoadConfigErrorMatrix 表驱动覆盖 LoadConfig 的 fail closed 分支。
func TestLoadConfigErrorMatrix(t *testing.T) {
	base := func() map[string]string {
		return map[string]string{
			"JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER":        "go",
			"JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID":       "wg",
			"JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL":      "postgres://wg-input.invalid/jobs",
			"JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL": "postgres://wg-input.invalid/business",
			"JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH":     "/tmp/wg/j1.sqlite3",
			"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":   "/tmp/wg/inputs",
			"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA0",
			"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET": "secret",
		}
	}
	cases := []struct {
		name   string
		mutate func(env map[string]string)
	}{
		{"缺 owner 声明", func(env map[string]string) { env["JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER"] = "node" }},
		{"PG 缺 URL", func(env map[string]string) { delete(env, "JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL") }},
		{"input 直读缺 URL", func(env map[string]string) { delete(env, "JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL") }},
		{"签名键过短", func(env map[string]string) { env["JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY"] = "AAAA" }},
		// 「缺凭据 secret」非生产臂已随 2026-09-19 零配置决策回退开发密钥；
		// production 下缺凭据 secret 仍 fail closed。
		{"production 缺凭据 secret", func(env map[string]string) {
			env["JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET"] = ""
			env["NODE_ENV"] = "production"
		}},
		// 「sqlite 缺路径」「缺 input 目录」「签名键缺失」三个失败臂已删除
		// （2026-09-19 零配置决策：三项均改为派生缺省，不再 fail closed）。
		{"owner lease 小于探针超时", func(env map[string]string) {
			env["JUHE_AI_ACCOUNT_HEALTH_OWNER_LEASE"] = "16s"
			env["JUHE_AI_ACCOUNT_HEALTH_PROBE_TIMEOUT"] = "30s"
		}},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			env := base()
			item.mutate(env)
			if _, err := LoadConfig(getenvFrom(env)); err == nil {
				t.Fatal("非法配置必须 fail closed")
			}
		})
	}
	// 合法基线必须通过。
	if _, err := LoadConfig(getenvFrom(base())); err != nil {
		t.Fatalf("基线配置必须通过: %v", err)
	}
}

type wgFakeOutboxStore struct {
	rows      []ProbeOutboxRow
	completed []string
	claimErr  error
}

func (s *wgFakeOutboxStore) ClaimPendingProbeRequests(_ context.Context, limit int, _ time.Time) ([]ProbeOutboxRow, error) {
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	if limit > len(s.rows) {
		limit = len(s.rows)
	}
	return s.rows[:limit], nil
}

func (s *wgFakeOutboxStore) CompleteProbeRequest(_ context.Context, requestID string, _ time.Time) (bool, error) {
	s.completed = append(s.completed, requestID)
	return true, nil
}

type wgFakeBoundary struct {
	revision int64
	ok       bool
	err      error
}

func (b *wgFakeBoundary) CurrentProbeInput(context.Context, string) (int64, int64, int64, bool, error) {
	if b.err != nil {
		return 0, 0, 0, false, b.err
	}
	return b.revision, b.revision, 1, b.ok, nil
}

