// 波次 w14j：组合根 PG 装配链 fail-fast 臂（复用 wg/w9h 子进程协议）。
// 这些臂通过坏/缺失连接串驱动 Acquire/配置校验失败，不依赖共享库形状。
package main

import (
	"os/exec"
	"testing"
	"time"
)

func TestW14JMainPGFailFastArms(t *testing.T) {
	if testing.Short() {
		t.Skip("fail-fast 子进程场景在 -short 下跳过")
	}
	scenarios := []struct {
		name  string
		patch map[string]string
	}{
		// J1 开启且 store=postgres 但连接串非法 → Acquire 失败。
		{"j1-pg-bad-url", map[string]string{
			"JUHE_AI_ACCOUNT_HEALTH_ENABLED":      "true",
			"JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER":   "go",
			"JUHE_AI_ACCOUNT_HEALTH_STORE":        "postgres",
			"JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL": "pgx://w14j-invalid-url",
		}},
		// J1 PG 直连输入缺业务库连接串 → LoadConfig fail closed。
		{"j1-input-pg-missing-url", map[string]string{
			"JUHE_AI_ACCOUNT_HEALTH_ENABLED":      "true",
			"JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER":   "go",
			"JUHE_AI_ACCOUNT_HEALTH_STORE":        "postgres",
			"JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL": "pgx://w14j-invalid-url",
			"JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE": "postgres",
		}},
		// J2 store=postgres 但连接串缺失 → LoadRuntimeConfig fail closed。
		{"j2-pg-missing-url", map[string]string{
			"JUHE_AI_ACCOUNT_BALANCE_ENABLED":      "true",
			"JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER":   "go",
			"JUHE_AI_ACCOUNT_BALANCE_STORE":        "postgres",
			"JUHE_AI_ACCOUNT_BALANCE_OWNER_ID":     "w14j-j2",
			"JUHE_AI_ACCOUNT_BALANCE_POSTGRES_URL": "",
		}},
		// J3a 管理接口开启而 J3a owner 未开启 → 守卫 fail。
		{"j3a-management-without-j3a", map[string]string{
			"JUHE_AI_PROXY_LATENCY_MANAGEMENT_ENABLED":        "true",
			"JUHE_AI_PROXY_LATENCY_MANAGEMENT_POSTGRES_URL":   "pgx://w14j-invalid-url",
			"JUHE_AI_PROXY_LATENCY_MANAGEMENT_LISTEN_ADDRESS": "127.0.0.1:0",
		}},
		// J3a 开启但 jobs 连接串非法 → Acquire 失败。
		{"j3a-pg-bad-url", map[string]string{
			"JUHE_AI_PROXY_LATENCY_ENABLED":        "true",
			"JUHE_AI_PROXY_LATENCY_JOBS_OWNER":     "go",
			"JUHE_AI_PROXY_LATENCY_STORE":          "postgres",
			"JUHE_AI_PROXY_LATENCY_POSTGRES_URL":   "pgx://w14j-invalid-url",
			"JUHE_AI_PROXY_LATENCY_INSTANCE_ID":    "w14j-j3a",
			"JUHE_AI_PROXY_LATENCY_CREDENTIAL_SECRET": "0123456789abcdef0123456789abcdef",
		}},
		// worker 组合根声明 postgres 却缺连接串 → loadWorkerConfig fail。
		{"worker-pg-missing-url", map[string]string{
			"JUHE_AI_DATABASE_DRIVER": "postgres",
		}},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			root := t.TempDir()
			env := wgOwnerModeMainEnv(t, root)
			for key, value := range scenario.patch {
				env[key] = value
			}
			port := wgFreePort(t)
			cmd := wgSpawnMainChild(t, env, "w14j-"+scenario.name, port)
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				exitErr, ok := err.(*exec.ExitError)
				if !ok {
					t.Fatalf("fail-fast 臂 %s 应以非零码退出，实际 err=%v", scenario.name, err)
				}
				if code := exitErr.ExitCode(); code != 1 {
					t.Fatalf("fail-fast 臂 %s 退出码=%d want 1", scenario.name, code)
				}
			case <-time.After(90 * time.Second):
				_ = cmd.Process.Kill()
				t.Fatalf("fail-fast 臂 %s 未在限期内退出", scenario.name)
			}
		})
	}
}
