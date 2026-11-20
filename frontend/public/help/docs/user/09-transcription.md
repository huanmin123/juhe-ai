# 语音转文字

> 这一篇解决：把音频变成文字的两种方式——短音频用 multipart 同步转写（一次请求直接出文本），长音频用 URL 提交异步任务（提交、轮询、取结果）。

## 开始之前

1. 一把可用的 API Key。认证只有一种方式：请求头 `Authorization: Bearer <API Key>`（Bearer 大小写不敏感），见《API Key 与认证》。
2. 一个支持语音转写的模型名，以 `GET /v1/models` 实际返回为准。
3. 输入材料二选一：一个本地音频文件（方式一用），或一个公网可访问的音频 URL（方式二用）。

## 两种方式怎么选

| | 方式一：同步转写 | 方式二：异步任务 |
| --- | --- | --- |
| 适合 | 短音频（语音消息、简短录音） | 长音频（课程、会议、长录音） |
| 输入 | multipart/form-data 直接上传音频文件 | JSON 里的 `input_url`，**唯一输入形态** |
| 出结果 | 一次请求，同步返回文本 | 提交任务 → 轮询状态 → 取结果 |
| 接口 | `POST /v1/audio/transcriptions`（转写）/ `POST /v1/audio/translations`（翻译） | `POST /v1/audio/jobs` |

## 方式一：短音频同步转写 POST /v1/audio/transcriptions

做什么：用 multipart/form-data 同时上传音频文件和模型名，响应里直接拿文本：

```bash
curl -s "$BASE_URL/v1/audio/transcriptions" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -F model="gpt-4o-transcribe" \
  -F file="@meeting_clip.m4a"
```

（`model` 请替换成你列表里真实存在的转写模型名——不同供应商可用的转写模型不同，以 `GET /v1/models` 为准；`file` 的 `@` 表示读取本地文件。）

结果长这样——同步返回，文本就在 `text` 字段里：

```json
{ "text": "大家好，今天我们讨论三个议题……" }
```

### response_format：一次请求拿到不同形态

可选 `response_format`，默认 `json`：

| 取值 | 响应形态 | 说明 |
| --- | --- | --- |
| `json` | `{"text": "..."}` | 默认；上游回报音频时长时附带 `duration`（秒） |
| `text` | 纯文本 | 直接是转写文本，不是 JSON |
| `verbose_json` | 含 `duration`、`segments` 的 JSON | 需要分句时间轴时用 |
| `srt` | SubRip 字幕文本 | 每个分句一条 |
| `vtt` | WebVTT 字幕文本 | 带 `WEBVTT` 头 |

```bash
curl -s "$BASE_URL/v1/audio/transcriptions" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -F model="gpt-4o-transcribe" \
  -F response_format="verbose_json" \
  -F file="@meeting_clip.m4a"
```

取值不在上表内会返回本地 400。某个模型是否支持全部五种形态取决于所接上游；火山 openspeech 同步转写（如 `volc.seedasr.auc`）五种都支持。

### 上传限制与翻译接口

- 同步转写是单次上传：火山 openspeech 同步转写对单文件强制 **25MB** 上限，超过会本地 400；其他上游按各自限制（OpenAI 官方同类接口同为 25MB）。更长的音频请走下面的异步任务。
- `POST /v1/audio/translations` 是转写的兄弟接口（OpenAI audio API 语义），但**不是所有转写模型都有翻译能力**，只有供应商与模型支持时才能用。火山 openspeech 同步转写当前只支持 `/v1/audio/transcriptions`，对这类模型调用 translations 会本地 400，不会伪造翻译结果；目录里没有可承接翻译的模型时，会按模型不支持或分组无账户返回错误。以 `GET /v1/models` 与管理台目录为准。

### 计费按音频时长

同步转写按**音频秒数**计费，用量记录里能看到这次调用的 `audio_input_seconds` 与金额。计量优先采用上游回报的实际时长，其次解析上传音频；两者都拿不到时该次计量为 0，并在用量记录标记缺失（不按文件大小估算）。对账口径见《用量与统计》。

## 方式二：长音频异步任务 POST /v1/audio/jobs

做什么：把音频的**公网 URL** 用 JSON 提交成任务：

```bash
curl -s "$BASE_URL/v1/audio/jobs" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "paraformer-v2",
    "input_url": "https://example.com/audio/long-lecture.mp3",
    "language": "zh"
  }'
```

（`model` 同样替换成真实存在的转写模型名；异步转写模型以 `GET /v1/models` 为准。）

**`input_url` 是唯一输入形态**：必须是 http(s) 的、公网可直接访问的音频 URL。往这个接口上传 multipart 文件会被 400 拒绝——想直接传文件请走方式一。

返回一个任务对象，与视频任务同构：`id` 以 `audiojob_` 开头、`object` 为 `"audio_job"`，另有 `status`、`progress`、`error` 等字段，并比视频任务多一个 `language`。把 `id` 存下来，下面轮询和取结果都用它。

## 异步任务的轮询、取结果与取消

任务面四个接口，用法与《视频生成：从提交到取片》的任务面一致：

| 动作 | 接口 |
| --- | --- |
| 任务列表 | `GET /v1/audio/jobs` |
| 轮询单个任务 | `GET /v1/audio/jobs/{id}` |
| 取消任务 | `DELETE /v1/audio/jobs/{id}` |
| 取转写结果 | `GET /v1/audio/jobs/{id}/content`（仅完成态） |

状态取值与视频任务一致：`queued` → `in_progress` → `completed` / `failed` / `cancelled` / `expired`。轮询示意（间隔自己定）：

```bash
while : ; do
  sleep 5
  resp=$(curl -s "$BASE_URL/v1/audio/jobs/audiojob_xxx" \
    -H "Authorization: Bearer $JUHE_AI_API_KEY")
  status=$(echo "$resp" | sed -n 's/.*"status":"\([a-z_]*\)".*/\1/p')
  echo "状态: $status"
  case "$status" in
    completed|failed|cancelled|expired) break ;;
  esac
done
```

进入 `completed` 后取结果：

```bash
curl -s "$BASE_URL/v1/audio/jobs/audiojob_xxx/content" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -o transcript.out
```

响应内容即转写结果，保存为文件查看即可。

## 参数表

方式一（multipart/form-data，用 `-F` 上传）：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `file` | 文件 | 是 | 音频文件本体；火山同步转写限 25MB，支持 mp3/wav/ogg/pcm |
| `model` | 字符串 | 是 | 转写模型名 |
| `response_format` | 字符串 | 否 | `json`（默认）/ `text` / `verbose_json` / `srt` / `vtt`，词表外 400 |
| `language` | 字符串 | 否 | 语言提示；是否影响识别取决于上游（火山同步面只把该值回填到 `verbose_json` 的 `language`，不作为识别参数） |

方式二（JSON body）：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `model` | 字符串 | 是 | 转写模型名；缺失报 503 `missing_model`，分组无账户支持报 503 `unsupported_model`（type 均为 `service_unavailable`） |
| `input_url` | 字符串 | 是 | 音频的 http(s) 公网 URL，唯一输入形态；multipart 上传会被 400 拒绝 |
| `language` | 字符串 | 否 | 语言提示 |

## 完整可运行示例

短音频一条命令出文本（方式一即完整示例）。长音频一条龙，保存为 `asr.sh` 执行：

```bash
#!/usr/bin/env bash
# 前置：export BASE_URL=... ; export JUHE_AI_API_KEY=sk-...
set -e

# 1. 提交
resp=$(curl -s "$BASE_URL/v1/audio/jobs" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "paraformer-v2",
    "input_url": "https://example.com/audio/long-lecture.mp3"
  }')
echo "提交结果: $resp"
job_id=$(echo "$resp" | sed -n 's/.*"id":"\(audiojob_[^"]*\)".*/\1/p')
echo "任务 id: $job_id"

# 2. 轮询（间隔仅示意）
status=""
while : ; do
  sleep 5
  resp=$(curl -s "$BASE_URL/v1/audio/jobs/$job_id" \
    -H "Authorization: Bearer $JUHE_AI_API_KEY")
  status=$(echo "$resp" | sed -n 's/.*"status":"\([a-z_]*\)".*/\1/p')
  echo "状态: $status"
  case "$status" in
    completed|failed|cancelled|expired) break ;;
  esac
done

# 3. 取结果
if [ "$status" = "completed" ]; then
  curl -s "$BASE_URL/v1/audio/jobs/$job_id/content" \
    -H "Authorization: Bearer $JUHE_AI_API_KEY" \
    -o transcript.out
  echo "结果已保存 transcript.out"
else
  echo "任务未完成，最后状态: $resp"
fi
```

## 常见问题或常见错误

统一错误信封：`{"error":{"message":"...","type":"...","code":"..."}}`

| 症状 | 原因 | 处理 |
| --- | --- | --- |
| 往 `/v1/audio/jobs` 传文件被 400 拒绝 | 该接口只收 JSON + `input_url`，multipart 不是合法输入 | 改用 `input_url`；或改走方式一的 multipart 接口 |
| 400，提示同步转写音频不得超过 25MB | 上传文件超过火山同步转写上限 | 压缩/切分音频，或改用 `/v1/audio/jobs` 异步任务 |
| 400，提示不支持 `response_format` | 取值不在 `json/text/verbose_json/srt/vtt` 内 | 改用上表五个取值之一 |
| 400，提示只支持 `/v1/audio/transcriptions` | 该模型（如火山 openspeech 同步转写）没有翻译能力，却调用了 `/v1/audio/translations` | 改用 `/v1/audio/transcriptions`，或换支持翻译能力的模型 |
| 503 `missing_model` / `unsupported_model`（type 均为 `service_unavailable`） | 没传 model、模型名写错或分组不支持 | 以 `GET /v1/models` 为准核对 |
| 400 `invalid_request_error` | `input_url` 不是 http(s) URL，或 multipart 字段名缺失 | 检查 URL 协议与 `-F` 字段拼写（`file`、`model`） |
| URL 提交后任务 `failed` | 上游取不到音频：URL 404、内网地址、需要登录才能下载 | 换成公网可直接下载的 URL 重新提交 |
| 转写结果为空或语言不对 | 音频质量问题，或语言识别偏差 | 检查音频清晰度；用 `language` 给出语言提示（是否生效取决于上游） |
| 用量记录里音频秒数为 0 / 标记缺失 | 上游未回报时长，网关也未能解析上传音频 | 改用能回报 `duration` 的模型或 `verbose_json`，或联系管理员核对 |
| 5xx，`type` 为 `server_error` / `service_unavailable` / `upstream_error` | 网关或上游临时故障 | 原样重试，反复失败按《错误码与排障》取证 |

## 接下来

- 反方向：文本变语音（同步、直接出音频文件）：《语音合成 TTS》
- 同样异步任务式的视频生成：《视频生成：从提交到取片》
- 错误信封与按症状排障：《错误码与排障》
