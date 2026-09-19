package acceptance

// 一次性手工 E2E（不入库；JUHE_AI_E2E_MANUAL=1 才运行，跑完删除本文件）。
// 拓扑：dev PostgreSQL 临时子库 + dev Redis 独立 namespace（性能模式驱动）
// + mock 上游（场景控制器）+ 挂死主机账户 + 真实上游账户。
// 场景：真实账户直连（含 opencode）、dial 失败换号不污染熔断、应用 500
// 换号熔断、403 额度 → rate_limited fence → J1 PG direct input 复测恢复、
// 成本护栏混合。

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	platformmock "github.com/huanminabc/juhe-ai/backend-go-platform/mockupstream"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const e2eRealModel = "glm-5.3-flash"

func e2eManualEnabled() bool { return os.Getenv("JUHE_AI_E2E_MANUAL") == "1" }

func e2eSharedEnv(key string) string {
	data, err := os.ReadFile(`F:/sub2api-lite/.local/project-resources/dev/env/shared.env`)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, key+"=") {
			return strings.TrimPrefix(line, key+"=")
		}
	}
	return ""
}

func e2eRequireEnv(key string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		panic("E2E 手工测试缺少环境变量 " + key)
	}
	return value
}

// e2eEnsureTempDatabase 以管理员直连创建临时子库（已存在则复用），返回应用 DSN。
func e2eEnsureTempDatabase(t *testing.T) string {
	t.Helper()
	host := e2eSharedEnv("DEV_POSTGRES_HOST")
	port := e2eSharedEnv("DEV_POSTGRES_DIRECT_PORT")
	if port == "" {
		port = "5432"
	}
	user := e2eSharedEnv("DEV_POSTGRES_ADMIN_USERNAME")
	password := e2eSharedEnv("DEV_POSTGRES_ADMIN_PASSWORD")
	appUser := e2eSharedEnv("DEV_POSTGRES_APP_USERNAME")
	if host == "" || user == "" || password == "" || appUser == "" {
		t.Fatal("shared.env 缺少 PostgreSQL 管理员或应用角色配置")
	}
	dbName := os.Getenv("E2E_DB_NAME")
	if dbName == "" {
		dbName = "juhe_ai_sub2api_dev_e2e0919"
	}
	adminDSN := fmt.Sprintf("postgres://%s:%s@%s:%s/postgres?sslmode=disable", user, password, host, port)
	sharedURL, urlErr := url.Parse(e2eSharedEnv("JUHE_AI_POSTGRES_URL"))
	if urlErr != nil || sharedURL.User == nil {
		t.Fatal("shared.env JUHE_AI_POSTGRES_URL 格式异常")
	}
	appPassword, _ := sharedURL.User.Password()

	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatalf("管理员连接失败: %v", err)
	}
	defer admin.Close()
	var exists int
	if err := admin.QueryRow(`SELECT 1 FROM pg_database WHERE datname = $1`, dbName).Scan(&exists); err != nil {
		if err != sql.ErrNoRows {
			t.Fatalf("查询临时库失败: %v", err)
		}
	}
	if exists != 1 {
		if _, err := admin.Exec(fmt.Sprintf(`CREATE DATABASE %s OWNER %s`, dbName, appUser)); err != nil {
			t.Fatalf("创建临时库失败: %v", err)
		}
	}
	appPassword = strings.ReplaceAll(appPassword, "'", "\\'")
	pgHost := host
	pgPort := "6432"
	if os.Getenv("E2E_PG_HOST") != "" {
		pgHost = os.Getenv("E2E_PG_HOST")
		pgPort = os.Getenv("E2E_PG_PORT")
	}
	appDSN := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", appUser, appPassword, pgHost, pgPort, dbName)
	// juhe_jobs schema 必须在临时库内预置（J1 store 只校验存在 + owner，
	// 表由 J1 自建）。注意必须在临时库连接上执行，不能落在 postgres 库。
	temp, err := sql.Open("pgx", appDSN)
	if err != nil {
		t.Fatalf("临时库连接失败: %v", err)
	}
	if _, err := temp.Exec(fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS juhe_jobs AUTHORIZATION %s`, appUser)); err != nil {
		t.Fatalf("创建 juhe_jobs schema 失败: %v", err)
	}
	temp.Close()
	// jobs 进程的 fixture env map 不含 PG URL（sqlite 装配），经 os.Environ
	// 追加语义导出同值 DSN 供 J1 PG direct input 使用；gateway/maintenance
	// 的 map 内同名字段与该值一致，无行为差。
	os.Setenv("JUHE_AI_POSTGRES_URL", appDSN)
	os.Setenv("JUHE_AI_DATABASE_DRIVER", "postgres")
	os.Setenv("JUHE_AI_POSTGRES_MAX_OPEN_CONNS", "10")
	os.Setenv("JUHE_AI_POSTGRES_MAX_IDLE_CONNS", "2")
	os.Setenv("JUHE_AI_ACCOUNT_HEALTH_ENABLED", "true")
	os.Setenv("JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER", "go")

	os.Setenv("JUHE_AI_ACCOUNT_HEALTH_STORE", "postgres")
	os.Setenv("JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL", appDSN)
	os.Setenv("JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER", "go")
	os.Setenv("JUHE_AI_RUNTIME_LOG_STORE", "postgres")
	os.Setenv("JUHE_AI_RUNTIME_LOG_POSTGRES_URL", appDSN)
	os.Setenv("JUHE_AI_TABLE_MONITOR_STORE", "postgres")
	os.Setenv("JUHE_AI_TABLE_MONITOR_POSTGRES_URL", appDSN)
	if os.Getenv("E2E_USE_DEV_REDIS") == "1" {
		for _, key := range []string{"JUHE_AI_REDIS_CACHE_URL", "JUHE_AI_REDIS_STATE_URL", "JUHE_AI_REDIS_QUEUE_URL"} {
			if value := e2eSharedEnv(key); value != "" {
				os.Setenv(key, value)
			}
		}
		os.Setenv("JUHE_AI_RUNTIME_MODE", "performance")
		os.Setenv("JUHE_AI_REDIS_NAMESPACE", "juhe-ai:dev:e2e0919")
		os.Setenv("JUHE_AI_CACHE_DRIVER", "redis")
		os.Setenv("JUHE_AI_RUNTIME_STATE_DRIVER", "redis")
		os.Setenv("JUHE_AI_QUEUE_DRIVER", "redis_stream")
	}
	return appDSN
}

func e2eDropTempDatabase(t *testing.T) {
	if os.Getenv("E2E_KEEP_DB") == "1" {
		t.Log("E2E_KEEP_DB=1：保留临时子库供排查")
		return
	}
	host := e2eSharedEnv("DEV_POSTGRES_HOST")
	user := e2eSharedEnv("DEV_POSTGRES_ADMIN_USERNAME")
	password := e2eSharedEnv("DEV_POSTGRES_ADMIN_PASSWORD")
	admin, err := sql.Open("pgx", fmt.Sprintf("postgres://%s:%s@%s:%s/postgres?sslmode=disable", user, password, host, e2eSharedEnv("DEV_POSTGRES_DIRECT_PORT")))
	if err != nil {
		return
	}
	defer admin.Close()
	dbName := os.Getenv("E2E_DB_NAME")
	if dbName == "" {
		dbName = "juhe_ai_sub2api_dev_e2e0919"
	}
	_, _ = admin.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, dbName)
	_, _ = admin.Exec(`DROP DATABASE IF EXISTS ` + dbName)
	t.Log("e2e 临时子库已删除:", dbName)
}

// e2eCreateRealAccount 创建真实上游账户（OpenAI-compatible chat）。
func (f *fullchainFixture) e2eCreateRealAccount(name, groupID, baseURL, apiKey, model string, extraCreds ...map[string]any) string {
	f.t.Helper()
	credentials := map[string]any{"api_key": apiKey, "base_url": baseURL}
	for _, extra := range extraCreds {
		for key, value := range extra {
			credentials[key] = value
		}
	}
	payload := map[string]any{
		"providerCode":              "openai",
		"providerProtocolProfileId": "profile_openai_openai_v1",
		"name":                      name,
		"type":                      "api_key",
		"credentials":               credentials,
		"supportedModels":           []string{model},
		"healthCheckModel":          model,
		"status":                    "active",
		"groupId":                   groupID,
	}
	_, created := f.admin.do(http.MethodPost, "/__aisys__/api/accounts", payload, wantStatus(http.StatusCreated))
	accountID := str(data(created)["id"])
	if accountID == "" {
		f.t.Fatalf("real account create payload wrong: %#v", created)
	}
	return accountID
}

func TestE2EManualRealMix(t *testing.T) {
	if !e2eManualEnabled() {
		t.Skip("手工 E2E：设置 JUHE_AI_E2E_MANUAL=1 运行")
	}
	realBaseURL := e2eRequireEnv("E2E_REAL_BASE_URL")
	realKey := e2eRequireEnv("E2E_REAL_KEY")
	appDSN := e2eEnsureTempDatabase(t)
	t.Cleanup(func() { e2eDropTempDatabase(t) })

	deadBaseURL := "http://127.0.0.1:9"

	fixture := &fullchainFixture{}
	mock := newFullchainMockUpstream(t)
	gw := startGateway(t, gatewayEnvOptions{ChainEnabled: true, PGDSN: appDSN})
	// LIFO：本 cleanup 在 harness TempDir 清理前运行，抢出进程完整日志
	//（panic 栈排查必需；子测试 Fatalf 会中断父测试主体，cleanup 兜底）。
	t.Cleanup(func() {
		if gw.process != nil && gw.process.logPath != "" {
			if data, err := os.ReadFile(gw.process.logPath); err == nil {
				_ = os.WriteFile(`F:/sub2api-lite/.local/project-resources/dev/logs/e2e-gateway.log`, data, 0o600)
			}
		}
	})
	// J1 进程配置必须在 startFullchainJobsWorker 之前导出（jobs 启动即校验）。
	// CREDENTIAL_SECRET 必须等于 gateway fixture 的随机 secret——J1 复测探针
	// 需要用它解密账户上游密钥。
	inputDir, err := os.MkdirTemp("", "e2e-j1-input")
	if err != nil {
		t.Fatalf("J1 input dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(inputDir) })
	os.Setenv("JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY", inputDir)
	os.Setenv("JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID", "e2e0919-j1")
	os.Setenv("JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE", "postgres")
	os.Setenv("JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL", appDSN)
	os.Setenv("JUHE_AI_ACCOUNT_HEALTH_INPUT_TTL_MS", "600000")
	os.Setenv("JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY", "YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFh")
	os.Setenv("JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET", gw.secret)
	admin := &acceptanceClient{t: t, http: gw.admin, baseURL: gw.baseURL}
	jobs := startFullchainJobsWorker(t, gw)
	t.Cleanup(func() {
		if jobs != nil && jobs.logPath != "" {
			if data, err := os.ReadFile(jobs.logPath); err == nil {
				_ = os.WriteFile(`F:/sub2api-lite/.local/project-resources/dev/logs/e2e-jobs.log`, data, 0o600)
			}
		}
	})
	fixture.t = t
	fixture.gw = gw
	fixture.admin = admin
	fixture.mock = mock
	fixture.jobs = jobs
	fixture.raiseSystemAPIRateLimits()
	f := fixture

	realGroup := f.createGroupWithProvider("e2e-真号组", "openai")
	realAccount := f.e2eCreateRealAccount("e2e-真号-aijh", realGroup, realBaseURL, realKey, e2eRealModel)
	t.Logf("real account id=%s", realAccount)

	// ---- S1 真号直连：管理面链路 + opencode 真实回复 ----
	t.Run("S1_real_direct", func(t *testing.T) {
		strategy := f.createStrategy("e2e-S1", "normal", []map[string]any{{"groupId": realGroup, "priority": 1, "weight": 100}}, nil)
		apiKey := f.createAPIKey("e2e-S1-key", strategy)
		response := f.chat(apiKey, fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"只回复两个字:成功"}],"max_tokens":64}`, e2eRealModel))
		if response.Status != http.StatusOK {
			t.Fatalf("S1 status=%d body=%s", response.Status, response.Body)
		}
		t.Logf("S1 real reply: %.120s", response.Body)
		apiKeyID := f.apiKeyIDByName("e2e-S1-key")
		detail := f.waitAuditLogDetail(t, apiKeyID, func(log fullchainAuditLog) bool {
			return log.Success && len(log.Attempts) > 0
		}, "S1 successful attempt")
		if str(detail.Attempts[0]["accountId"]) != realAccount {
			t.Fatalf("S1 attempt account = %v", detail.Attempts[0])
		}

		// opencode 真实客户端（chat_completions → 真号）。
		home := clientTempDir(t)
		workDir := clientTempDir(t)
		opencodeConfig := fmt.Sprintf(`{
  "$schema": "https://opencode.ai/config.json",
  "provider": {
    "e2e": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "E2E",
      "options": {"baseURL": %q, "apiKey": %q},
      "models": {%q: {"name": %q}}
    }
  }
}
`, gw.baseURL+"/v1", apiKey, e2eRealModel, e2eRealModel)
		writeFile(t, workDir+"/opencode.json", opencodeConfig)
		env := append(clientBaseEnv(t, home),
			"OPENCODE_DISABLE_AUTOUPDATE=1",
			"PWD="+workDir)
		result := runFullchainClient(t, workDir, env, "opencode.cmd", "run", "--print-logs",
			"-m", "e2e/"+e2eRealModel, "只回复两个字:成功")
		trimmed := strings.TrimSpace(result.Stdout)
		if result.ExitCode != 0 || trimmed == "" {
			t.Fatalf("S1 opencode exit=%d stdout=%.200s stderr=%.200s", result.ExitCode, result.Stdout, result.Stderr)
		}
		t.Logf("S1 opencode reply: %.120s", trimmed)
	})

	// ---- S2 dial 失败换号：挂死主机优先 + 真号兜底；熔断不被 dial 污染 ----
	t.Run("S2_dial_failover", func(t *testing.T) {
		deadGroup := f.createGroupWithProvider("e2e-挂死组", "openai")
		deadKey := "sk-e2e-dead"
		deadAccount := f.e2eCreateRealAccount("e2e-挂死账户", deadGroup, deadBaseURL, deadKey, e2eRealModel)
		strategy := f.createStrategy("e2e-S2", "failover", []map[string]any{
			{"groupId": deadGroup, "priority": 1, "weight": 100},
			{"groupId": realGroup, "priority": 2, "weight": 100},
		}, nil)
		apiKey := f.createAPIKey("e2e-S2-key", strategy)
		for round := 1; round <= 3; round++ {
			response := f.chat(apiKey, fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"S2 第 %d 轮"}],"max_tokens":32}`, e2eRealModel, round))
			if response.Status != http.StatusOK {
				t.Fatalf("S2 round %d status=%d body=%.200s", round, response.Status, response.Body)
			}
		}
		apiKeyID := f.apiKeyIDByName("e2e-S2-key")
		detail := f.waitAuditLogDetail(t, apiKeyID, func(log fullchainAuditLog) bool {
			return log.Success && len(log.Attempts) >= 2
		}, "S2 dial+real attempts")
		t.Logf("S2 attempts=%d", len(detail.Attempts))
		// 真号账户必须保持 active（dial 失败不写共享熔断/状态）。
		snapshot := f.accountSnapshot(realAccount)
		if status := str(snapshot["status"]); status != "active" {
			t.Fatalf("S2 real account status = %s, want active", status)
		}
		_ = deadAccount
	})

	// ---- S3 应用 500 换号：mock 恒 500 + 真号；熔断确认后后续请求跳过 mock ----
	t.Run("S3_mock500_failover", func(t *testing.T) {
		mockGroup := f.createGroupWithProvider("e2e-mock500组", "openai")
		mockKey := "sk-e2e-mock500"
		f.e2eCreateRealAccount("e2e-mock500账户", mockGroup, mock.server.URL, mockKey, e2eRealModel)
		mock.setDefault(mockKey, platformmock.ScenarioStatus500)
		strategy := f.createStrategy("e2e-S3", "failover", []map[string]any{
			{"groupId": mockGroup, "priority": 1, "weight": 100},
			{"groupId": realGroup, "priority": 2, "weight": 100},
		}, nil)
		apiKey := f.createAPIKey("e2e-S3-key", strategy)
		for round := 1; round <= 3; round++ {
			response := f.chat(apiKey, fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"S3 第 %d 轮"}],"max_tokens":32}`, e2eRealModel, round))
			if response.Status != http.StatusOK {
				t.Fatalf("S3 round %d status=%d", round, response.Status)
			}
		}
		callsAfterThree := len(mock.protocolCallsByKey(mockKey))
		response := f.chat(apiKey, fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"S3 第 4 轮"}],"max_tokens":32}`, e2eRealModel))
		if response.Status != http.StatusOK {
			t.Fatalf("S3 round 4 status=%d", response.Status)
		}
		callsAfterFour := len(mock.protocolCallsByKey(mockKey))
		t.Logf("S3 mock calls: after3=%d after4=%d（熔断确认后应停增）", callsAfterThree, callsAfterFour)
		if callsAfterFour > callsAfterThree+1 {
			t.Fatalf("S3 mock 仍被持续尝试: %d -> %d", callsAfterThree, callsAfterFour)
		}
	})

	// ---- S4 临时不可调用 fence：500 → temporary_unavailable（3 秒初始退避）+ fence → J1 PG direct input 复测恢复 ----
	t.Run("S4_quota_fence_recovery", func(t *testing.T) {
		quotaGroup := f.createGroupWithProvider("e2e-quota组", "gpt")
		quotaKey := fullchainUpstreamKey(t, "S4-quota")
		f.createAccount("e2e-quota账户", quotaKey, quotaGroup, map[string]any{
			// 账户级规则：500 → cooldown(temporary_unavailable)。temporary
			// 不可调用走 3 秒初始退避 + J1 fence 复测（额度类 403 的
			// quota_recovery_policy 下限 30 分钟，超出 E2E 观察窗，故用本路径）。
			"credentials": map[string]any{
				"error_handling_rules": []any{
					map[string]any{
						"enabled":        true,
						"name":           "e2e-500-cooldown",
						"priority":       1,
						"action":         "temp_unschedulable",
						"status_codes":   []any{500},
						"reset_strategy": "duration",
						"duration_hours": 1,
					},
				},
			},
		})
		f.mock.setDefault(quotaKey, platformmock.ScenarioStatus500)
		strategy := f.createStrategy("e2e-S4", "normal", []map[string]any{
			{"groupId": quotaGroup, "priority": 1, "weight": 100},
		}, nil)
		apiKey := f.createAPIKey("e2e-S4-key", strategy)
		response := f.chat(apiKey, fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"S4"}],"max_tokens":16}`, "fullchain-health-probe-model"))
		t.Logf("S4 exhausted status=%d", response.Status)
		quotaAccount := f.accountIDByName("e2e-quota账户")
		deadline := time.Now().Add(15 * time.Second)
		unavailable := false
		var cooldown any
		for time.Now().Before(deadline) {
			snapshot := f.accountSnapshot(quotaAccount)
			if str(snapshot["status"]) == "temporary_unavailable" {
				unavailable = true
				cooldown = snapshot["cooldownUntil"]
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if !unavailable {
			snapshot := f.accountSnapshot(quotaAccount)
			t.Fatalf("S4 账户未进入 rate_limited: %v", snapshot)
		}
		t.Logf("S4 rate_limited 确认, cooldown=%v（等待 J1 fence 复测恢复）", cooldown)
		// 放开 mock：403 后改为成功，J1 复测应通过并恢复 active。
		mock.setDefault(quotaKey, platformmock.ScenarioChatOK)
		deadline = time.Now().Add(120 * time.Second)
		recovered := false
		for time.Now().Before(deadline) {
			snapshot := f.accountSnapshot(quotaAccount)
			if str(snapshot["status"]) == "active" {
				recovered = true
				break
			}
			time.Sleep(2 * time.Second)
		}
		if !recovered {
			snapshot := f.accountSnapshot(quotaAccount)
			t.Fatalf("S4 J1 复测未恢复 active: %v", snapshot)
		}
		t.Log("S4 J1 fence 复测恢复 active")
		response = f.chat(apiKey, fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"S4 恢复后"}],"max_tokens":16}`, "fullchain-health-probe-model"))
		if response.Status != http.StatusOK || !strings.Contains(response.Body, "MOCK-OK") {
			t.Fatalf("S4 恢复后服务异常 status=%d body=%.120s", response.Status, response.Body)
		}
	})

	// ---- S5 成本护栏：真号优先 normal，mock 零调用 ----
	t.Run("S5_cost_guard", func(t *testing.T) {
		mockGroup := f.createGroupWithProvider("e2e-guard组", "openai")
		okKey := "sk-e2e-guardmock"
		f.e2eCreateRealAccount("e2e-guardmock账户", mockGroup, mock.server.URL, okKey, e2eRealModel)
		strategy := f.createStrategy("e2e-S5", "failover", []map[string]any{
			{"groupId": realGroup, "priority": 1, "weight": 100},
			{"groupId": mockGroup, "priority": 2, "weight": 100},
		}, nil)
		apiKey := f.createAPIKey("e2e-S5-key", strategy)
		response := f.chat(apiKey, fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"S5"}],"max_tokens":32}`, e2eRealModel))
		if response.Status != http.StatusOK {
			t.Fatalf("S5 status=%d", response.Status)
		}
		if calls := len(mock.protocolCallsByKey(okKey)); calls != 0 {
			t.Fatalf("S5 真号可用时 mock 被调用 %d 次", calls)
		}
	})

	// 汇总落盘（脱敏：不写 key）。
	summary, _ := json.MarshalIndent(map[string]any{
		"realAccount": realAccount,
		"result":      "all scenarios passed",
	}, "", "  ")
	_ = os.WriteFile(`F:/sub2api-lite/.local/project-resources/dev/logs/e2e-manual-20260919-result.json`, summary, 0o600)

	// 保留进程完整日志（t.TempDir 清理会删除原件；panic 栈排查需要）。
	if gw.process != nil && gw.process.logPath != "" {
		if data, err := os.ReadFile(gw.process.logPath); err == nil {
			_ = os.WriteFile(`F:/sub2api-lite/.local/project-resources/dev/logs/e2e-gateway.log`, data, 0o600)
		}
	}
	if jobs != nil && jobs.logPath != "" {
		if data, err := os.ReadFile(jobs.logPath); err == nil {
			_ = os.WriteFile(`F:/sub2api-lite/.local/project-resources/dev/logs/e2e-jobs.log`, data, 0o600)
		}
	}
}

// TestE2EManualRealResponsesBridge BUG-0178 真机验收：真实 chat 上游账户 +
// 显式 responses -> chat_completions 模型映射，网关 /v1/responses 端到端桥
// 转换（真实上游兼容性 + 真实 SSE 回转），并以无映射账户做负对照。
func TestE2EManualRealResponsesBridge(t *testing.T) {
	if !e2eManualEnabled() {
		t.Skip("手工 E2E：设置 JUHE_AI_E2E_MANUAL=1 运行")
	}
	realBaseURL := e2eRequireEnv("E2E_REAL_BASE_URL")
	realKey := e2eRequireEnv("E2E_REAL_KEY")
	appDSN := e2eEnsureTempDatabase(t)
	t.Cleanup(func() { e2eDropTempDatabase(t) })

	gw := startGateway(t, gatewayEnvOptions{ChainEnabled: true, PGDSN: appDSN})
	// LIFO：本 cleanup 在 harness TempDir 清理前运行，抢出进程完整日志。
	t.Cleanup(func() {
		if gw.process != nil && gw.process.logPath != "" {
			if data, err := os.ReadFile(gw.process.logPath); err == nil {
				_ = os.WriteFile(`F:/sub2api-lite/.local/project-resources/dev/logs/e2e-bridge-gateway.log`, data, 0o600)
			}
		}
	})
	admin := &acceptanceClient{t: t, http: gw.admin, baseURL: gw.baseURL}
	fixture := &fullchainFixture{t: t, gw: gw, admin: admin}
	fixture.raiseSystemAPIRateLimits()
	f := fixture

	bridgeGroup := f.createGroupWithProvider("e2e-桥接组", "openai")
	bridgeAccount := f.e2eCreateRealAccount("e2e-真号-桥接", bridgeGroup, realBaseURL, realKey, e2eRealModel)
	// modelMappings 不在创建契约内（bug0162：PATCH 扩展字段），创建后 PATCH。
	_, patched := admin.do(http.MethodPatch, "/__aisys__/api/accounts/"+bridgeAccount, map[string]any{
		"expectedConfigRevision": 1,
		"modelMappings": []map[string]any{{
			"sourceModel":            e2eRealModel,
			"sourceEndpointFamily":   "responses",
			"upstreamModel":          e2eRealModel,
			"upstreamEndpointFamily": "chat_completions",
		}},
	}, wantStatus(http.StatusOK))
	t.Logf("bridge account id=%s patched=%v", bridgeAccount, patched != nil)

	bridgeStrategy := f.createStrategy("e2e-bridge", "normal", []map[string]any{{"groupId": bridgeGroup, "priority": 1, "weight": 100}}, nil)
	bridgeKey := f.createAPIKey("e2e-bridge-key", bridgeStrategy)

	// B0 对照：chat 直连必须可用（上游健康基线）。
	t.Run("B0_chat_baseline", func(t *testing.T) {
		response := f.chatT(t, bridgeKey, fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"只回复两个字:成功"}],"max_tokens":512}`, e2eRealModel))
		if response.Status != http.StatusOK {
			t.Fatalf("B0 status=%d body=%.300s", response.Status, response.Body)
		}
		t.Logf("B0 chat reply: %.160s", response.Body)
	})

	// B1 非流式：/v1/responses 经桥转换打到 chat 上游并回转 Responses JSON。
	t.Run("B1_responses_buffered", func(t *testing.T) {
		request, err := http.NewRequest(http.MethodPost, gw.baseURL+"/v1/responses",
			strings.NewReader(fmt.Sprintf(`{"model":%q,"input":"只回复两个字:成功","stream":false,"max_output_tokens":512}`, e2eRealModel)))
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		request.Header.Set("Authorization", "Bearer "+bridgeKey)
		request.Header.Set("Content-Type", "application/json")
		response := f.doRawT(t, request)
		if response.Status != http.StatusOK {
			t.Fatalf("B1 status=%d body=%.400s", response.Status, response.Body)
		}
		if !strings.Contains(response.Body, `"object":"response"`) && !strings.Contains(response.Body, `"object": "response"`) {
			t.Fatalf("B1 缺少 Responses 对象语义: %.400s", response.Body)
		}
		if strings.Contains(response.Body, `"choices"`) {
			t.Fatalf("B1 响应仍携带 chat completions 特征字段 choices: %.400s", response.Body)
		}
		t.Logf("B1 responses reply: %.300s", response.Body)
	})

	// B2 流式：chat SSE 上游经桥回转 Responses SSE 事件流。
	t.Run("B2_responses_stream", func(t *testing.T) {
		request, err := http.NewRequest(http.MethodPost, gw.baseURL+"/v1/responses",
			strings.NewReader(fmt.Sprintf(`{"model":%q,"input":"只回复两个字:成功","stream":true,"max_output_tokens":512}`, e2eRealModel)))
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		request.Header.Set("Authorization", "Bearer "+bridgeKey)
		request.Header.Set("Content-Type", "application/json")
		response := f.doRawT(t, request)
		if response.Status != http.StatusOK {
			t.Fatalf("B2 status=%d body=%.400s", response.Status, response.Body)
		}
		if !strings.Contains(response.ContentType, "text/event-stream") {
			t.Fatalf("B2 Content-Type=%q 非 SSE", response.ContentType)
		}
		if !strings.Contains(response.Body, `"type":"response.`) && !strings.Contains(response.Body, `response.`) {
			t.Fatalf("B2 缺少 Responses SSE 事件: %.400s", response.Body)
		}
		if strings.Contains(response.Body, `"choices"`) {
			t.Fatalf("B2 流式响应仍携带 chat completions 特征字段 choices: %.400s", response.Body)
		}
		t.Logf("B2 stream head: %.300s", response.Body)
	})

	// B3 负对照：同上游账户不带映射时，/v1/responses 不得以 chat 透传方式
	// "意外成功"（端点模式闸应淘汰或显式报错）。
	t.Run("B3_negative_plain_account", func(t *testing.T) {
		plainGroup := f.createGroupWithProvider("e2e-裸号组", "openai")
		f.e2eCreateRealAccount("e2e-真号-裸", plainGroup, realBaseURL, realKey, e2eRealModel)
		strategy := f.createStrategy("e2e-plain", "normal", []map[string]any{{"groupId": plainGroup, "priority": 1, "weight": 100}}, nil)
		apiKey := f.createAPIKey("e2e-plain-key", strategy)
		request, err := http.NewRequest(http.MethodPost, gw.baseURL+"/v1/responses",
			strings.NewReader(fmt.Sprintf(`{"model":%q,"input":"只回复两个字:成功","stream":false}`, e2eRealModel)))
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		request.Header.Set("Authorization", "Bearer "+apiKey)
		request.Header.Set("Content-Type", "application/json")
		response := f.doRawT(t, request)
		if response.Status == http.StatusOK && strings.Contains(response.Body, `"choices"`) {
			t.Fatalf("B3 无映射账户以 chat 透传形态意外成功: %.300s", response.Body)
		}
		t.Logf("B3 无映射行为: status=%d body=%.200s", response.Status, response.Body)
	})
}
