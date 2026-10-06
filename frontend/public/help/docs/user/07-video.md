# 视频生成：从提交到取片

> 这一篇解决：POST /v1/videos 提交生成任务、轮询状态、下载成片的完整生命周期，以及每一步的结果长什么样。

## 开始之前

1. 一把可用的 API Key。认证只有一种方式：请求头 `Authorization: Bearer <API Key>`（Bearer 大小写不敏感），见《API Key 与认证》。
2. 一个视频模型名，以 `GET /v1/models` 实际返回为准。
3. 心理预期：视频生成是**异步任务**——提交后立刻拿到的不是视频，而是一个任务 id；成片要等生成完成后另行下载。

## 这套接口怎么运转

三步走，每步一个接口：

| 步骤 | 做什么 | 接口 |
| --- | --- | --- |
| 1 | 提交任务 | `POST /v1/videos` |
| 2 | 轮询状态，直到离开排队/生成中 | `GET /v1/videos/{id}` |
| 3 | 下载成片 | `GET /v1/videos/{id}/content` |

辅助接口：`GET /v1/videos` 列出任务、`DELETE /v1/videos/{id}` 取消任务。

任务状态机：`queued`（排队）→ `in_progress`（生成中）→ `completed`（完成）/ `failed`（失败）/ `cancelled`（已取消）/ `expired`（已过期）。只有 `completed` 能下载成片。

## 第一步：提交任务 POST /v1/videos

做什么：用 JSON 描述你要生成的视频，最少只要 `model` 和 `prompt`：

```bash
curl -s "$BASE_URL/v1/videos" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.4",
    "prompt": "一只橘猫在窗台上晒太阳，阳光洒在毛上，写实风格",
    "seconds": 5,
    "size": "1280x720"
  }'
```

（示例里的 `model` 请替换成你模型列表里真实存在的视频模型名。）

结果长这样——创建成功返回一个任务对象：

```json
{
  "id": "video_xxx",
  "object": "video",
  "status": "queued",
  "progress": 0,
  "model": "……",
  "prompt": "一只橘猫在窗台上晒太阳……",
  "seconds_length": 5,
  "size": "1280x720",
  "error": null,
  "provider": "……",
  "provider_job_id": "……",
  "params_applied": ["model", "prompt", "size"],
  "params_ignored": [],
  "provider_options_applied": []
}
```

**立刻把 `id` 存下来**，轮询和下载全靠它。

关于"受理"要知道的事：提交返回成功，代表上游已受理并给出了任务 id；如果上游认为参数不合法（400/413/422 类错误），错误会**直接透传**给你，网关不会悄悄换一个账户重试——按错误信息改参数后重新提交即可。

## 提交参数表

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `model` | 字符串 | 是 | 视频模型名；缺失报 503 `missing_model`，分组无账户支持报 503 `unsupported_model`（type 均为 `service_unavailable`） |
| `prompt` | 字符串 | 是 | 画面描述。主体、动作、镜头、风格写得越具体，效果越可控 |
| `seconds` | 数字 | 否 | 视频时长（秒） |
| `size` | 字符串 | 否 | 分辨率，"宽x高" 像素串，如 `"1280x720"` |
| `n` | 整数 | 否 | 生成条数，≥1 |
| `input_reference` | 字符串 | 否 | 图生视频的参考图：image_url 或 base64 data URL 字符串 |
| `negative_prompt` | 字符串 | 否 | 负面提示：不希望出现的内容 |
| `seed` | 整数 | 否 | 随机种子，固定种子配合相同参数可复现同类结果 |
| `audio` | 布尔 | 否 | 是否生成带音频的视频 |

参数最终是否生效，以任务对象里的 `params_applied`（网关归一化后**实际生效**的参数）和 `params_ignored`（**被忽略**的参数）为准——上游模型不支持的字段会出现在 `params_ignored` 里，不算报错。

## 任务对象字段表

| 字段 | 说明 |
| --- | --- |
| `id` | 任务 id，`video_` 前缀，轮询/下载/取消都用它 |
| `object` | 固定为 `"video"` |
| `status` | 当前状态：queued / in_progress / completed / failed / cancelled / expired |
| `progress` | 进度（0~100 的参考值） |
| `model` / `prompt` | 你提交时的模型名与描述 |
| `seconds_length` | 实际时长（秒） |
| `size` | 分辨率 |
| `error` | 失败原因，仅 failed 时有用 |
| `provider` / `provider_job_id` | 实际承接的上游供应商与它在侧的任务 id |
| `params_applied` / `params_ignored` | 网关归一化后实际生效 / 被忽略的参数名列表（如 `["model","prompt","size"]`） |
| `provider_options_applied` | 供应商专有扩展通道实际生效的项（高级用法，一般任务为空） |

## 第二步：轮询状态 GET /v1/videos/{id}

做什么：拿任务 id 循环查询，直到 `status` 进入终态（completed/failed/cancelled/expired）。轮询间隔自己定，下面以每 5 秒一次示意：

```bash
while : ; do
  sleep 5
  resp=$(curl -s "$BASE_URL/v1/videos/video_xxx" \
    -H "Authorization: Bearer $JUHE_AI_API_KEY")
  echo "$resp"
  status=$(echo "$resp" | sed -n 's/.*"status":"\([a-z_]*\)".*/\1/p')
  case "$status" in
    completed|failed|cancelled|expired) break ;;
  esac
done
```

结果怎么读：`progress` 一点点涨是正常现象；进入 `completed` 就可以下载；进入 `failed` 就看任务对象里的 `error` 字段了解原因。

## 第三步：下载成片 GET /v1/videos/{id}/content

只有 `completed` 状态能下载，返回 mp4 二进制。用 `-o` 保存成文件：

```bash
curl -s "$BASE_URL/v1/videos/video_xxx/content" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -o cat.mp4
```

保存后用播放器打开确认。如果下载回来的不是视频而是一段 JSON，多半是任务还没到 `completed` 或 id 写错——先 `GET /v1/videos/{id}` 核对状态。

## 列表与取消

```bash
# 我提交过的任务列表
curl -s "$BASE_URL/v1/videos" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY"

# 取消一个还在排队/生成中的任务
curl -s -X DELETE "$BASE_URL/v1/videos/video_xxx" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY"
```

任务与提交它的 API Key 归属一致，查询、下载、取消都按任务归属进行。

## 完整可运行示例

三步串成一条龙，保存为 `video.sh` 执行（先 `export BASE_URL=...` 和 `export JUHE_AI_API_KEY=sk-...`）：

```bash
#!/usr/bin/env bash
set -e

# 1. 提交
resp=$(curl -s "$BASE_URL/v1/videos" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.4",
    "prompt": "夕阳下的海边，浪花轻拍沙滩，慢镜头",
    "seconds": 5,
    "size": "1280x720"
  }')
echo "提交结果: $resp"
video_id=$(echo "$resp" | sed -n 's/.*"id":"\(video_[^"]*\)".*/\1/p')
echo "任务 id: $video_id"

# 2. 轮询（间隔仅示意）
status=""
while : ; do
  sleep 5
  resp=$(curl -s "$BASE_URL/v1/videos/$video_id" \
    -H "Authorization: Bearer $JUHE_AI_API_KEY")
  status=$(echo "$resp" | sed -n 's/.*"status":"\([a-z_]*\)".*/\1/p')
  echo "状态: $status"
  case "$status" in
    completed|failed|cancelled|expired) break ;;
  esac
done

# 3. 下载
if [ "$status" = "completed" ]; then
  curl -s "$BASE_URL/v1/videos/$video_id/content" \
    -H "Authorization: Bearer $JUHE_AI_API_KEY" \
    -o seaside.mp4
  echo "已保存 seaside.mp4"
else
  echo "任务未完成，最后状态: $resp"
fi
```

## 常见问题或常见错误

统一错误信封：`{"error":{"message":"...","type":"...","code":"..."}}`

| 症状 | 原因 | 处理 |
| --- | --- | --- |
| 503 `missing_model`（type `service_unavailable`） | 没传 `model` | 补上视频模型名 |
| 503 `unsupported_model`（type `service_unavailable`），message 形如「当前分组无账户支持请求模型：X」 | 模型名写错或分组不支持 | 以 `GET /v1/models` 为准核对 |
| 400/413/422 参数错误（上游透传） | prompt 超长、seconds/size 超出上游限制等 | 按错误信息调整参数后重新提交；网关不会换号重试 |
| 状态长期停在 `queued` | 分组账户忙或上游排队 | 继续等待；过久可 `DELETE` 取消后重提 |
| `status: failed` | 上游生成失败 | 看任务对象 `error` 字段，按提示调整 prompt/参数重试 |
| 下载返回的是 JSON 不是视频 | 任务不是 `completed`，或 id 写错 | 先查询确认状态再下载 |
| `status: expired` | 任务过期 | 重新提交一次 |

高级用法 provider_options 可按供应商透传专有参数，一般无需使用。

## 接下来

- 同样是异步任务形态的语音接口：《语音转文字》
- 文本转语音（同步、一次请求直接出音频）：《语音合成 TTS》
- 错误信封与按症状排障：《错误码与排障》
