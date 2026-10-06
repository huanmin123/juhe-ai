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
  -F model="gpt-5.4" \
  -F file="@meeting_clip.m4a"
```

（`model` 请替换成你列表里真实存在的转写模型名；`file` 的 `@` 表示读取本地文件。）

结果长这样——同步返回，文本就在 `text` 字段里：

```json
{ "text": "大家好，今天我们讨论三个议题……" }
```

`translations` 是它的兄弟接口，把音频内容翻译成英文文本（OpenAI audio API 语义），请求方式完全一样，只换路径：

```bash
curl -s "$BASE_URL/v1/audio/translations" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -F model="gpt-5.4" \
  -F file="@foreign_clip.mp3"
```

## 方式二：长音频异步任务 POST /v1/audio/jobs

做什么：把音频的**公网 URL** 用 JSON 提交成任务：

```bash
curl -s "$BASE_URL/v1/audio/jobs" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.4",
    "input_url": "https://example.com/audio/long-lecture.mp3",
    "language": "zh"
  }'
```

（`model` 同样替换成真实存在的转写模型名。）

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
| `file` | 文件 | 是 | 音频文件本体 |
| `model` | 字符串 | 是 | 转写模型名 |
| `language` | 字符串 | 否 | 语言提示（按 OpenAI audio API 形态的可选字段） |

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
    "model": "gpt-5.4",
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
| 503 `missing_model` / `unsupported_model`（type 均为 `service_unavailable`） | 没传 model、模型名写错或分组不支持 | 以 `GET /v1/models` 为准核对 |
| 400 `invalid_request_error` | `input_url` 不是 http(s) URL，或 multipart 字段名缺失 | 检查 URL 协议与 `-F` 字段拼写（`file`、`model`） |
| URL 提交后任务 `failed` | 上游取不到音频：URL 404、内网地址、需要登录才能下载 | 换成公网可直接下载的 URL 重新提交 |
| 转写结果为空或语言不对 | 音频质量问题，或语言识别偏差 | 检查音频清晰度；用 `language` 给出语言提示 |
| 5xx，`type` 为 `server_error` / `service_unavailable` / `upstream_error` | 网关或上游临时故障 | 原样重试，反复失败按《错误码与排障》取证 |

## 接下来

- 反方向：文本变语音（同步、直接出音频文件）：《语音合成 TTS》
- 同样异步任务式的视频生成：《视频生成：从提交到取片》
- 错误信封与按症状排障：《错误码与排障》
