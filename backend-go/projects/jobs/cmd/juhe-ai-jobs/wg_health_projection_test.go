package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
)

// TestHealthProjectionEnvParsing 覆盖投影面轮询/批量 env 的默认值、合法
// 边界与 fail loud 报错分支。
func TestHealthProjectionEnvParsing(t *testing.T) {
	cases := []struct {
		name     string
		env      map[string]string
		poll     time.Duration
		batch    int
		pollErr  bool
		batchErr bool
	}{
		{"默认值", map[string]string{}, accounthealth.DefaultProjectionPollInterval, accounthealth.DefaultProjectionBatchSize, false, false},
		{"合法下界", map[string]string{
			healthProjectionPollEnvVar: "100", healthProjectionBatchEnvVar: "1",
		}, 100 * time.Millisecond, 1, false, false},
		{"合法上界", map[string]string{
			healthProjectionPollEnvVar: "60000", healthProjectionBatchEnvVar: "1000",
		}, 60 * time.Second, 1000, false, false},
		{"轮询非整数", map[string]string{healthProjectionPollEnvVar: "abc"}, 0, 0, true, false},
		{"轮询越下界", map[string]string{healthProjectionPollEnvVar: "99"}, 0, 0, true, false},
		{"轮询越上界", map[string]string{healthProjectionPollEnvVar: "60001"}, 0, 0, true, false},
		{"批量非整数", map[string]string{healthProjectionBatchEnvVar: "abc"}, accounthealth.DefaultProjectionPollInterval, 0, false, true},
		{"批量越下界", map[string]string{healthProjectionBatchEnvVar: "0"}, accounthealth.DefaultProjectionPollInterval, 0, false, true},
		{"批量越上界", map[string]string{healthProjectionBatchEnvVar: "1001"}, accounthealth.DefaultProjectionPollInterval, 0, false, true},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			poll, err := healthProjectionPollInterval(getenvFrom(item.env))
			if (err != nil) != item.pollErr {
				t.Fatalf("poll wantErr=%v, err=%v", item.pollErr, err)
			}
			if !item.pollErr && poll != item.poll {
				t.Fatalf("poll=%v 期望 %v", poll, item.poll)
			}
			batch, err := healthProjectionBatchSize(getenvFrom(item.env))
			if (err != nil) != item.batchErr {
				t.Fatalf("batch wantErr=%v, err=%v", item.batchErr, err)
			}
			if !item.batchErr && !item.pollErr && batch != item.batch {
				t.Fatalf("batch=%d 期望 %d", batch, item.batch)
			}
		})
	}
}

// TestWireHealthOutcomeProjectorBranches 覆盖投影装配的合法缺席与
// fail loud 分支（成功路径由 main e2e 子进程覆盖）。
func TestWireHealthOutcomeProjectorBranches(t *testing.T) {
	// store 为 nil（J1 未启用）→ (nil, nil)。
	minimal := newWorkerAssembly(workerConfig{Driver: "sqlite"}, nil)
	projector, err := minimal.wireHealthOutcomeProjector(getenvFrom(map[string]string{}), nil)
	if err != nil || projector != nil {
		t.Fatalf("store 缺席必须合法缺席: %v %v", projector, err)
	}
	// store 就绪但 J1 config 非法（签名键坏）→ 装配 fail loud。
	store, storeErr := accounthealth.OpenStore(accounthealth.StoreConfig{
		Mode:         accounthealth.StoreSQLite,
		DatabasePath: filepath.Join(t.TempDir(), "j1-store.sqlite3"),
	})
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	t.Cleanup(func() { _ = store.Close() })
	badConfigEnv := wgProjectionEnabledEnv(t)
	badConfigEnv["JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY"] = "short"
	if _, err := minimal.wireHealthOutcomeProjector(getenvFrom(badConfigEnv), store); err == nil {
		t.Fatal("非法 J1 配置必须 fail loud")
	}
	// 轮询 env 非法 → 装配 fail loud（业务库缺失先于 env 检查，构造最小装配体）。
	badPollEnv := wgProjectionEnabledEnv(t)
	badPollEnv[healthProjectionPollEnvVar] = "abc"
	if _, err := minimal.wireHealthOutcomeProjector(getenvFrom(badPollEnv), store); err == nil {
		t.Fatal("非法轮询 env 必须 fail loud")
	}
}

// wgProjectionEnabledEnv 构造 J1 启用的最小 env（LoadConfig 通过）。
func wgProjectionEnabledEnv(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER":        "go",
		"JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID":       "wg-projection",
		"JUHE_AI_ACCOUNT_HEALTH_STORE":             "sqlite",
		"JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH":     filepath.Join(t.TempDir(), "j1.sqlite3"),
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":   filepath.Join(t.TempDir(), "inputs"),
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA0",
		"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET": "0123456789abcdef0123456789abcdef",
	}
}
