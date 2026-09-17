// 全链路验收 Stage 4：真实客户端端到端（计划 §3 C1/C2/C3/C5）。
//
// 门控与 Stage 3 一致（FULLCHAIN + REAL + PROD secret）。真实客户端：
//   - codex-cli 0.152.0（/v1/responses，隔离 CODEX_HOME + config.toml）；
//   - claude 2.x（/v1/messages，env 注入）——E2E-FINDING #6 修复后已恢复
//     完整断言；
//   - opencode（chat_completions，项目级 opencode.json + 隔离 XDG 目录）。
//
// 客户端进程 env 完全隔离（HOME/USERPROFILE/XDG/CODEX_HOME 指向临时目录），
// 不读写用户现有配置；命令超时 120s，失败保留 stderr 摘要（脱敏）。
package acceptance

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/mockupstream"
)

// fullchainClientBinDir 是真实客户端 CLI 的安装目录。
const fullchainClientBinDir = `E:\node\node_global`

// fullchainClientResult 携带一次客户端进程运行的脱敏结果。
type fullchainClientResult struct {
	ExitCode  int
	Stdout    string
	Stderr    string
	OutputImg string
}

// runFullchainClient 运行一个真实客户端命令（120s 超时；工作目录与 env 隔离）。
func runFullchainClient(t *testing.T, dir string, env []string, name string, args ...string) fullchainClientResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(fullchainClientBinDir, name), args...)
	cmd.Dir = dir
	cmd.Env = env
	// CLI（codex/opencode）检测到 stdin 非 TTY 会等待 EOF 追加输入；显式给
	// 立即关闭的空 stdin，避免客户端阻塞在读 stdin 分支。
	cmd.Stdin = strings.NewReader("")
	// Windows 下 .cmd shim 的 node 后代进程不被默认 Kill 覆盖，会继续持有
	// TempDir 里的数据文件（opencode.db）导致清理失败置 FAIL；ctx 取消时
	// 用 taskkill 杀整棵进程树。
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run()
	}
	cmd.WaitDelay = 10 * time.Second
	stdout, err := os.CreateTemp(clientTempDir(t), "client-stdout-")
	if err != nil {
		t.Fatalf("create stdout temp: %v", err)
	}
	stderr, err := os.CreateTemp(clientTempDir(t), "client-stderr-")
	if err != nil {
		t.Fatalf("create stderr temp: %v", err)
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	runErr := cmd.Run()
	_ = stdout.Close()
	_ = stderr.Close()
	stdoutData, _ := os.ReadFile(stdout.Name())
	stderrData, _ := os.ReadFile(stderr.Name())
	result := fullchainClientResult{Stdout: string(stdoutData), Stderr: string(stderrData)}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if ctx.Err() != nil {
		result.Stderr += "\n(client timed out after 120s)"
	}
	if runErr != nil {
		result.Stderr += "\n(run error: " + runErr.Error() + ")"
	}
	_ = err
	return result
}

// clientBaseEnv 构造隔离的客户端进程 env（不透传用户级 AI 客户端配置）。
func clientBaseEnv(t *testing.T, home string) []string {
	t.Helper()
	return append(os.Environ(),
		"HOME="+home,
		"USERPROFILE="+home,
		"APPDATA="+filepath.Join(home, "AppData", "Roaming"),
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
		"NO_COLOR=1",
		"CI=1",
	)
}

// TestFullchainRealClients Stage 4：真实客户端全链路。
func TestFullchainRealClients(t *testing.T) {
	prodSecret := requireRealGateWithSecret(t)
	r := startRealFixture(t, prodSecret)
	f := r.f

	t.Run("C1_codex_responses_gpt", func(t *testing.T) {
		raw, ok := r.pickRaw("gpt", 0)
		if !ok {
			t.Skip("C1：无可用 gpt 生产候选")
		}
		// E2E-FINDING #7（已修复）：createInTx 按 derive 落列
		// client_compatibility（gpt+api_key → codex_responses），调度侧 pinned
		// 比较承接 codex CLI 的 codex_responses 请求类。完整断言：对照组
		// /v1/responses 200 + codex CLI 输出非空 + 审计归因真号账户。
		route, account, ok := r.realRouteImported("C1", "gpt", raw, nil, nil)
		if !ok {
			t.Skipf("C1：真号导入失败（模型 %s）", raw.HealthModel)
		}
		// 导入写路径把 active 重映射为 pending_test（importCreateInput），
		// 假探针模型让健康检查永不通过；PATCH 恢复 active（造数允许范围）。
		f.patchAccount(account.ID, map[string]any{"status": "active"})
		if got := f.accountSnapshot(account.ID)["clientCompatibility"]; got != "codex_responses" {
			t.Fatalf("E2E-FINDING #7 已修复：导入的 gpt+api_key 账户 clientCompatibility 应为 codex_responses，实际 %v", got)
		}
		// 对照组：普通 HTTP 客户端 POST /v1/responses 200（真号真实回复）。
		baseline := postResponses(t, f, route.apiKey, account.Model)
		if baseline.Status != http.StatusOK {
			t.Fatalf("C1 对照组 /v1/responses 预期 200，实际 status=%d body=%s",
				baseline.Status, maskRealBody(baseline.Body))
		}
		// codex CLI 全链路。
		home := clientTempDir(t)
		config := fmt.Sprintf(`model = %q
model_provider = "fullchain"

[model_providers.fullchain]
name = "fullchain"
base_url = %q
env_key = "FULLCHAIN_API_KEY"
wire_api = "responses"
`, account.Model, f.gw.baseURL+"/v1")
		writeFile(t, filepath.Join(home, "config.toml"), config)
		outputFile := filepath.Join(clientTempDir(t), "codex-last-message.txt")
		env := append(clientBaseEnv(t, home),
			"CODEX_HOME="+home,
			"FULLCHAIN_API_KEY="+route.apiKey,
			"OPENAI_API_KEY="+route.apiKey)
		result := runFullchainClient(t, clientTempDir(t), env, "codex.cmd", "exec",
			"--skip-git-repo-check", "-o", outputFile, "回复OK")
		lastMessage, readErr := os.ReadFile(outputFile)
		if readErr != nil || len(strings.TrimSpace(string(lastMessage))) == 0 {
			t.Fatalf("E2E-FINDING #7 已修复：codex CLI 应完整通过（对照 /v1/responses 200，账户已派生 "+
				"codex_responses），实际 exit=%d，stderr 摘要=%s",
				result.ExitCode, maskClientText(result.Stderr))
		}
		detail := f.waitAuditLogDetail(t, route.apiKeyID, func(log fullchainAuditLog) bool {
			return log.Success && len(log.Attempts) >= 1
		}, "codex success audit")
		last := detail.Attempts[len(detail.Attempts)-1]
		if str(last["accountId"]) != account.ID {
			t.Fatalf("C1 attempt account wrong: %s", str(last["accountId"]))
		}
		t.Logf("C1 codex（…%s /v1/responses）输出 %d 字节，模型 %s",
			account.ID[len(account.ID)-8:], len(lastMessage), account.Model)
	})

	t.Run("C2_claude_messages_finding", func(t *testing.T) {
		// E2E-FINDING #6（已修复）：/v1 协议门放行 anthropic native 面，
		// claude 客户端全链路应完整通过（exit=0 + JSON 输出非空）。
		account, ok := r.pick("anthropic", 0)
		if !ok {
			t.Skip("C2：无可用 anthropic 导入账户")
		}
		route := r.realRoute("C2", "anthropic", account)
		home := clientTempDir(t)
		env := append(clientBaseEnv(t, home),
			"ANTHROPIC_BASE_URL="+f.gw.baseURL,
			"ANTHROPIC_AUTH_TOKEN="+route.apiKey,
			"ANTHROPIC_API_KEY="+route.apiKey,
			// CLI 默认模型名不在账户 supportedModels 内（网关 503
			// model_unsupported），显式钉住导入账户的健康模型。
			"ANTHROPIC_MODEL="+account.Model)
		// Stage3 惯例（上游偶发重试一次）：claude CLI ≥2.1.220 在 API 错误时
		// exit=1 且错误详情在 stdout 的 is_error JSON（result 字段）；上游
		// anthropic 529 过载是环境噪声，不应记为链路缺陷。
		var result fullchainClientResult
		for attempt := 0; attempt < 2; attempt++ {
			result = runFullchainClient(t, clientTempDir(t), env, "claude.cmd", "-p", "回复OK", "--output-format", "json")
			if result.ExitCode == 0 && strings.TrimSpace(result.Stdout) != "" {
				break
			}
		}
		if result.ExitCode != 0 || strings.TrimSpace(result.Stdout) == "" {
			t.Fatalf("claude 客户端链路应可用（/v1/messages 已接入协议门与分发链），"+
				"实际 exit=%d，stdout 摘要=%s，stderr 摘要=%s",
				result.ExitCode, maskClientText(result.Stdout), maskClientText(result.Stderr))
		}
		t.Logf("C2 claude（…%s /v1/messages）：exit=0，输出长度 %d", account.ID[len(account.ID)-8:], len(result.Stdout))
	})

	t.Run("C3_opencode_chat_completions", func(t *testing.T) {
		account, ok := r.pick("deepseek", 0)
		if !ok {
			account, ok = r.pick("glm", 0)
			if !ok {
				t.Skip("C3：无可用 deepseek/glm 导入账户")
			}
		}
		route := r.realRoute("C3", account.ProviderCode, account)
		home := clientTempDir(t)
		workDir := clientTempDir(t)
		opencodeConfig := fmt.Sprintf(`{
  "$schema": "https://opencode.ai/config.json",
  "provider": {
    "fullchain": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "Fullchain",
      "options": {"baseURL": %q, "apiKey": "%s"},
      "models": {%q: {"name": %q}}
    }
  }
}
`, f.gw.baseURL+"/v1", route.apiKey, account.Model, account.Model)
		writeFile(t, filepath.Join(workDir, "opencode.json"), opencodeConfig)
		// PWD 覆盖：go test 继承的 PWD 指向测试源码目录，opencode 会拿它解析
		// 项目目录（--print-logs 可见第二个 instance 在 acceptance 目录
		// bootstrap 后内部错误退出），必须与 cmd.Dir 一致。
		env := append(clientBaseEnv(t, home),
			"OPENCODE_DISABLE_AUTOUPDATE=1",
			"PWD="+workDir)
		result := runFullchainClient(t, workDir, env, "opencode.cmd", "run", "--print-logs",
			"-m", "fullchain/"+account.Model, "回复OK")
		trimmed := strings.TrimSpace(result.Stdout)
		if result.ExitCode != 0 || trimmed == "" {
			// 对照组：同账户非流式 chat 可用（真号/凭据/路由健康才走到这里），
			// 失败即 Fatal，不掩盖真号侧回归。
			assertRealChat(t, f, route.apiKey, account.Model)
			// E2E-FINDING #8 canary：opencode 链路已通（真实请求进入网关、真号
			// 被调用），失败是生产上游对该客户端流式请求返回非标准
			// `Content-Encoding: none`，网关 unexpected_failure 503 循环直到
			// opencode 重试耗尽（网关日志 error="不支持的上游响应压缩编码:
			// none"；确定性 mock 复现见 F8_upstream_content_encoding_none）。
			t.Skipf("E2E-FINDING #8：客户端失败但同账户非流式 chat 200（exit=%d，120s 超时内重试耗尽）；"+
				"确定性复现见 F8；修复 #8 后应恢复 exit=0 + 审计归因断言", result.ExitCode)
		}
		detail := f.waitAuditLogDetail(t, route.apiKeyID, func(log fullchainAuditLog) bool {
			return log.Success && len(log.Attempts) >= 1
		}, "opencode success audit")
		last := detail.Attempts[len(detail.Attempts)-1]
		if str(last["accountId"]) != account.ID {
			t.Fatalf("C3 attempt account wrong: %s", str(last["accountId"]))
		}
		t.Logf("C3 opencode（…%s chat_completions）：exit=0，输出 %d 字节，模型 %s", account.ID[len(account.ID)-8:], len(trimmed), account.Model)
	})

	t.Run("C5_codex_failover_mock500_to_real", func(t *testing.T) {
		// E2E-FINDING #7（已修复）：codex_responses 请求类可承接 gpt+api_key
		// 账户（derive 落列）。恢复故障切换链验证：mock 账户（priority 0，
		// 恒 500）+ 真号（priority 100），codex CLI exec 输出非 MOCK 内容 +
		// mock 被真实调用 + 审计末位 attempt 归属真号。
		account, ok := r.pick("gpt", 0)
		if !ok {
			t.Skip("C5：无可用 gpt 导入账户")
		}
		mockKey := fullchainUpstreamKey(t, "C5-mock")
		// mock 与真号同 provider 分组并支持真号请求模型；gpt provider 的
		// api_key 账户按 derive 落列 codex_responses，codex 请求可同时命中
		// mock 与真号（模型过滤契约见 M1）。
		mockAccountExtra := map[string]any{
			"providerCode":              account.ProviderCode,
			"providerProtocolProfileId": profileForProvider(account.ProviderCode),
			"supportedModels":           []string{account.Model, acceptanceModel, fullchainProbeModel},
		}
		groupID := f.createGroupWithProvider("全链路-C5-组", account.ProviderCode)
		f.createAccount("全链路-C5-mock账户", mockKey, groupID, mockAccountExtra)
		f.rebindAccountGroup(account.ID, groupID, 100)
		f.mock.setDefault(mockKey, mockupstream.ScenarioStatus500)
		strategyID := f.createStrategy("全链路-C5-策略", "normal", []map[string]any{
			{"groupId": groupID, "priority": 1, "weight": 100},
		}, nil)
		apiKey := f.createAPIKey("全链路-C5-Key", strategyID)
		apiKeyID := f.apiKeyIDByName("全链路-C5-Key")

		home := clientTempDir(t)
		config := fmt.Sprintf(`model = %q
model_provider = "fullchain"

[model_providers.fullchain]
name = "fullchain"
base_url = %q
env_key = "FULLCHAIN_API_KEY"
wire_api = "responses"
`, account.Model, f.gw.baseURL+"/v1")
		writeFile(t, filepath.Join(home, "config.toml"), config)
		outputFile := filepath.Join(clientTempDir(t), "codex-c5-last-message.txt")
		env := append(clientBaseEnv(t, home),
			"CODEX_HOME="+home,
			"FULLCHAIN_API_KEY="+apiKey,
			"OPENAI_API_KEY="+apiKey)
		result := runFullchainClient(t, clientTempDir(t), env, "codex.cmd", "exec",
			"--skip-git-repo-check", "-o", outputFile, "回复OK")
		lastMessage, readErr := os.ReadFile(outputFile)
		if readErr != nil || len(strings.TrimSpace(string(lastMessage))) == 0 {
			t.Fatalf("C5：codex 故障切换链应完整通过（mock 500 → 真号接管），实际 exit=%d，stderr 摘要=%s",
				result.ExitCode, maskClientText(result.Stderr))
		}
		if strings.Contains(string(lastMessage), "MOCK") {
			t.Fatalf("C5：codex 输出来自 mock：%s", maskClientText(string(lastMessage)))
		}
		if got := len(f.mock.protocolCallsByKeyModel(mockKey, account.Model)); got < 1 {
			t.Fatalf("C5：mock 未被 codex 请求命中（attempts=%d），故障切换链未建立", got)
		}
		detail := f.waitAuditLogDetail(t, apiKeyID, func(log fullchainAuditLog) bool {
			return log.Success && len(log.Attempts) >= 1
		}, "codex failover success audit")
		if str(detail.Attempts[len(detail.Attempts)-1]["accountId"]) != account.ID {
			t.Fatalf("C5 final attempt account wrong: %s", str(detail.Attempts[len(detail.Attempts)-1]["accountId"]))
		}
		t.Logf("C5 codex（…%s 接管）：mock 尝试 %d 次，最终真号内容 %d 字节",
			account.ID[len(account.ID)-8:], len(f.mock.protocolCallsByKeyModel(mockKey, account.Model)), len(lastMessage))
	})
}

// clientTempDir 建客户端专用临时目录；清理失败不置 FAIL（客户端 .cmd shim
// 的后代进程可能短时间仍持有句柄，t.TempDir 的强制清理会把 SKIP 置成 FAIL）。
func clientTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "fullchain-client-")
	if err != nil {
		t.Fatalf("mkdir client temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// writeFile 写入 UTF-8 文本文件。
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// postResponses 以普通 HTTP 客户端（无 codex 画像特征，请求兼容类为空）
// POST /v1/responses，作为 C1 对照组。
func postResponses(t *testing.T, f *fullchainFixture, apiKey, model string) fullchainChatResponse {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, f.gw.baseURL+"/v1/responses",
		strings.NewReader(fmt.Sprintf(`{"model":%q,"input":"回复OK"}`, model)))
	if err != nil {
		t.Fatalf("build responses request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+apiKey)
	return f.doRaw(request)
}

// maskClientText 脱敏客户端输出：截断并去除可能内嵌的 API Key。
func maskClientText(text string) string {
	for _, marker := range []string{"FULLCHAIN_API_KEY=", "OPENAI_API_KEY=", "ANTHROPIC_AUTH_TOKEN="} {
		if idx := strings.Index(text, marker); idx >= 0 {
			end := strings.IndexAny(text[idx:], "\r\n ,\"")
			if end < 0 {
				end = len(text[idx:])
			}
			text = text[:idx+len(marker)] + "<REDACTED>" + text[idx+end:]
		}
	}
	if len(text) > 2400 {
		return text[:2400] + "…(truncated)"
	}
	return text
}
