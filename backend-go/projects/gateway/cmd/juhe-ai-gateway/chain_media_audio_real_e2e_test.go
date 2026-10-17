// 火山长转写真实上游端到端验证（契约 §9.2 承接批的实弹回归载体）。默认
// 跳过，不进常规回归（常规覆盖见 TestChainMediaVolcengineASRLifecycle 的
// mock 全链）；仅当同时设置以下环境变量时运行真实出站：
//
//	JUHE_AI_E2E_VOLC_ASR_TOKEN       新版语音控制台 API Key（X-Api-Key 凭据，
//	                                 账户 speech_token；凭据只经环境变量进入，
//	                                 不落源码/日志/测试产物）
//	JUHE_AI_E2E_VOLC_ASR_AUDIO_URL   境内可达音频直链（gitee raw 实证可被
//	                                 火山服务器拉取；境外源会卡授权层后
//	                                 45000006 失败，见接入文档取证节）
//
// 不设 seam（JUHE_AI_GATEWAY_VOLCENGINE_SPEECH_BASE_URL），出站直连官方
// openspeech.bytedance.com；业务库为本进程隔离 sqlite fixture，无外部数据
// 依赖。验证面：submit 受理（queued + 网关生成任务 ID）→ 轮询真实处理状态
// 至 completed → content 代理转写 JSON（result.text 非空）→ media_jobs 终态
// 按目录秒价计费 → spool usage 计量行。
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestChainMediaVolcengineASRRealUpstreamE2E(t *testing.T) {
	token := strings.TrimSpace(os.Getenv("JUHE_AI_E2E_VOLC_ASR_TOKEN"))
	audioURL := strings.TrimSpace(os.Getenv("JUHE_AI_E2E_VOLC_ASR_AUDIO_URL"))
	if token == "" || audioURL == "" {
		t.Skip("真实上游 E2E 门控：需 JUHE_AI_E2E_VOLC_ASR_TOKEN + JUHE_AI_E2E_VOLC_ASR_AUDIO_URL")
	}

	fixture := newChainFixture(t)
	apiKey := seedMediaVolcengineASRAccount(t, fixture, "acc_volc_asr_real", "sk-volc-asr-real", token)
	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain := composeMediaVideoChain(t, fixture, spoolDir)
	server := httptest.NewServer(chain)
	defer server.Close()

	job := mediaAudioJobCreate(t, server.URL, apiKey,
		fmt.Sprintf(`{"model":"volc.seedasr.auc","input_url":%q}`, audioURL), nil)
	jobID, _ := job["id"].(string)
	if jobID == "" || !strings.HasPrefix(jobID, "audiojob_") {
		t.Fatalf("真实创建响应缺 job id: %#v", job)
	}
	if job["status"] != "queued" {
		t.Fatalf("真实 submit 受理状态 = %v, want queued: %#v", job["status"], job)
	}
	providerJobID, _ := job["provider_job_id"].(string)
	if len(providerJobID) != 32 {
		t.Fatalf("真实 provider_job_id = %q, want 16 字节 hex", providerJobID)
	}

	// 真实上游处理时长不定（境内直链秒级，失败臂曾 4 分钟）：3 秒间隔轮询，
	// 5 分钟 deadline 兜底。
	deadline := time.Now().Add(5 * time.Minute)
	var final map[string]any
	var finalPayload []byte
	for {
		response, payload := mediaVideoClientDo(t, server.URL, http.MethodGet, "/v1/audio/jobs/"+jobID, apiKey, "", nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("真实轮询 status=%d body=%s", response.StatusCode, payload)
		}
		current := mediaVideoDecodeJob(t, payload)
		status, _ := current["status"].(string)
		if status == "completed" || status == "failed" || status == "cancelled" {
			final, finalPayload = current, payload
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("真实轮询超时未达终态（最后 %s）: %s", status, payload)
		}
		time.Sleep(3 * time.Second)
	}
	if final["status"] != "completed" {
		t.Fatalf("真实转写未完成 status=%v: %s", final["status"], finalPayload)
	}

	// content：同构 query 代理的转写 JSON，result.text 必须非空（真实转写）。
	response, payload := mediaVideoClientDo(t, server.URL, http.MethodGet, "/v1/audio/jobs/"+jobID+"/content", apiKey, "", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("真实 content status=%d body=%s", response.StatusCode, payload)
	}
	var face struct {
		Result struct {
			Text string `json:"text"`
		} `json:"result"`
	}
	if err := json.Unmarshal(payload, &face); err != nil {
		t.Fatalf("真实转写 JSON 解析失败: %v: %s", err, payload)
	}
	if strings.TrimSpace(face.Result.Text) == "" {
		t.Fatalf("真实转写文本为空: %s", payload)
	}
	t.Logf("真实转写文本: %q", face.Result.Text)

	// 终态：media_jobs completed + 目录秒价计费（cost>0）+ spool 计量行。
	var status string
	var costUsd float64
	if err := fixture.db.QueryRow(`SELECT status, cost_usd FROM media_jobs WHERE id = ?`, jobID).Scan(&status, &costUsd); err != nil {
		t.Fatalf("查询 media_jobs: %v", err)
	}
	if status != "completed" {
		t.Fatalf("media_jobs status = %s, want completed", status)
	}
	if costUsd <= 0 {
		t.Fatalf("cost_usd = %v, want 按目录秒价 $3.1746031746031745e-05/秒 计费", costUsd)
	}
	waitGlmSpoolRecord(t, spoolDir, func(text string) bool {
		return strings.Contains(text, `"endpoint":"/v1/audio/jobs"`) && strings.Contains(text, `"audioInputSeconds":`)
	}, "真实转写 usage 计量记录")
}
