# BUG-0370 火山 openspeech 声明同步 STT 但实际转发到不存在的端点

## 基本信息

- 编号：BUG-0370
- 状态：已修复并随 v2026.11.20 上线（2026-11-20）
- 严重程度：P1（发布前阻断）
- 发现时间：2026-11-20
- 发现方式：真实上游协议矩阵（`/v1/audio/transcriptions` multipart）
- 模块：后端 / 网关 / 同步语音转写 / 火山 openspeech 连接器
- 关联计划：无（真实上游协议矩阵批次已闭环）
- 关联 bug：BUG-0368（multipart model 提取）
- 责任人：ZCode

## 问题概述

- 现象：火山 openspeech 音频账户声明 `audio_transcription_json`，但 `POST /v1/audio/transcriptions` 返回上游 404 HTML（`TLB`），请求体已正确到达上游域名。
- 期望：对外保持 OpenAI 同步转写形态，成功返回 `{"text": ...}` 并按音频秒数计费。
- 实际：通用 OpenAI 透传链把路径拼成 `https://openspeech.bytedance.com/audio/transcriptions`，该端点不存在；`gatewaymedia` 只有异步长转写 adapter（`volcengine_asr.go`），没有同步实现。
- 影响范围：所有声明同步转写的火山 openspeech 音频账户；异步 `/v1/audio/jobs` 不受影响。

## 复现步骤

1. 使用 dev 火山 openspeech 音频账户（`supported_endpoint_modes` 含 `audio_transcription_json`）。
2. `POST /v1/audio/transcriptions`，multipart 携带 `model=volc.seedasr.auc` 与 `file`。
3. 网关返回 404 HTML，正文 `<center>TLB</center>`，请求未进入任何同步 adapter。

## 根因分析

- 连接器词表声明了同步模式，但链上只有 `volcengineSpeechRequest`（TTS `/audio/speech`）与 `audioJobCreateRequest`（异步 `/v1/audio/jobs`）两个火山分支；`/audio/transcriptions` 落入通用 OpenAI 透传。
- 火山语音域的真实同步端点不在 `/audio/transcriptions`，而是 `POST /api/v3/auc/bigmodel/recognize/flash`（头驱动状态、支持 `audio.url` 与 `audio.data` base64）。
- 通用透传无法完成 multipart → base64 报文改写与厂商 JSON → OpenAI 响应归一，因此必须以独立 adapter 承接。

## 修复方案

- 新增 `gatewaymedia/transcriptionir.go`：同步转写 adapter 端口与注册表（首个条目 `volcengine`）。
- 新增 `gatewaymedia/volcengine_asr_sync.go`：
  - `BuildRequest`：multipart `file` → `audio.data` base64，`audio.format` 支持 mp3/wav/ogg/pcm，`request.model_name=bigmodel`，携带 `X-Api-Resource-Id`/`X-Api-Request-Id`/`X-Api-Sequence`。
  - `TransformResponse`：校验 `X-Api-Status-Code=20000000`，把 `audio_info.duration` 毫秒与 `result.text/utterances` 归一为 `json`/`verbose_json`/`text`/`srt`/`vtt`；json/verbose_json 携带 `duration` 秒供统一媒体计费。
- `chain_driver.go`：
  - URL 构造把火山 + `/audio/transcriptions` 指向 `chainVolcengineASRUpstreamURL(VolcASRSyncPath)`。
  - 请求构造新增 `volcengineTranscriptionRequest`，仅命中声明 `audio_transcription_json` 的 openspeech 账户；缺 model/file、格式不支持、超 25MB、`/audio/translations` 均本地 400。
- `gatewayresponse`：新增同步转写响应转换计划，完整缓冲后转换并下发；转换后的正文作为 usage 解析输入。
- `gatewaybody`：`ExtractMultipartFilePartInfo` 额外返回 filename，供无 Content-Type 时推断格式；新增 `ExtractMultipartAudioLanguage`。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 上游探测 | 确认同步端点存在 | `POST /api/v3/auc/bigmodel/recognize/flash` 空体 | 非 404 | 401 `Invalid X-Api-Key`（端点存在） | 通过 |
| 上游实测 | base64 音频同步识别 | 有效 speech_token + `audio.data` | 返回文本 | `{"audio_info":{"duration":3996},"result":{"text":"你好，这是网关真实语音测试。"}}`，`X-Api-Status-Code: 20000000` | 通过 |
| 功能验证 | 网关同步转写 json | `POST /v1/audio/transcriptions`（multipart） | 200 + text | `{"duration":3.996,"text":"你好，这是网关真实语音测试。"}` | 通过 |
| 功能验证 | text/verbose_json/srt | 同上切换 `response_format` | 对应形态 | text 纯文本、verbose_json 含 segments、srt 含 `00:00:00,450 --> 00:00:03,570` | 通过 |
| 计费验证 | 上游时长落库 | 查询 `usage_records` | `audio_input_seconds=3.996`、`cost_usd=0.0001268571`、`usage_missing=0` | 一致（账号 `acc_9dc21042c614d59e`） | 通过 |
| 边界验证 | 翻译面拒绝 | `POST /v1/audio/translations` | 本地 400 | `invalid_gateway_request` 文案明确不支持 | 通过 |
| 单元测试 | adapter 报文/格式/响应 | `go test ./internal/gatewaymedia -run 'VolcASRSync|VolcengineTranscription'` | 全通过 | `ok` | 通过 |
| 回归验证 | mock 上游 + 链级同步转写 | `go test ./shared/platform/mockupstream -run 'VolcengineASRSync'` + `JUHE_AI_TEST_EXTERNAL=1 go test ./projects/gateway/cmd/juhe-ai-gateway -run 'TestChainMediaVolcengineASRSyncTranscription'` | 全通过 | `ok`（mock `media_volcengine_asr_sync_ok` 与链级 multipart→base64→usage 用例） | 通过 |

## 下次遇到

- 先查什么：先区分 `audio/speech`、`audio/transcriptions`、`audio/jobs` 三条链；火山同步转写只走 `recognize/flash`，不是 `/audio/transcriptions`。
- 重点看什么：`X-Api-Status-Code` 是业务状态，HTTP 200 不代表成功；`audio_info.duration` 是毫秒，转成秒后再落库。
- 如何避免误判：看到 404 HTML（TLB）优先怀疑路径不存在，而不是凭据或请求体。

## 完成总结

- 完成时间：2026-11-20
- 结论：火山 openspeech 同步 STT 从“声明但 404”变为真实可用；json/text/verbose_json/srt/vtt 与秒级计费均经真实上游验证，并补 `media_volcengine_asr_sync_ok` mock 场景与链级回归（multipart→base64→duration 计量/成本）。
- 后续建议：若后续要支持 `/v1/audio/translations`，需先确认火山极速版是否有翻译字段或另找上游；当前显式 400，不伪造翻译结果。
