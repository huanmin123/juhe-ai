# 视频生成：从提交到取片

> 这一篇解决：`POST /v1/video/generations` 提交生成任务、轮询状态、下载成片的完整生命周期，以及每一步的结果长什么样。

## 开始之前

1. 一把可用的 API Key。认证只有一种方式：请求头 `Authorization: Bearer <API Key>`（Bearer 大小写不敏感），见《API Key 与认证》。
2. 一个视频模型名，以 `GET /v1/models` 实际返回为准。
3. 心理预期：视频生成是**异步任务**——提交后立刻拿到的不是视频，而是一个任务 id（`task_id`）；成片要等生成完成后另行下载。

## 这套接口怎么运转

三步走，每步一个接口：

| 步骤 | 做什么 | 接口 |
| --- | --- | --- |
| 1 | 提交任务 | `POST /v1/video/generations` |
| 2 | 轮询状态，直到离开排队/生成中 | `GET /v1/video/generations/{task_id}` |
| 3 | 下载成片 | `GET /v1/video/generations/{task_id}/content` |

辅助接口：`GET /v1/video/generations` 列出任务、`DELETE /v1/video/generations/{task_id}` 取消任务。

任务状态机：`queued`（排队）→ `in_progress`（生成中）→ `completed`（完成）/ `failed`（失败）/ `cancelled`（已取消）/ `expired`（已过期）。只有 `completed` 能下载成片。

## 第一步：提交任务 POST /v1/video/generations

做什么：用 JSON 描述你要生成的视频，最少只要 `model` 和 `prompt`（图生视频时可以只给 `image`）：

```bash
curl -s "$BASE_URL/v1/video/generations" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "sora-2",
    "prompt": "一只橘猫在窗台上晒太阳，阳光洒在毛上，写实风格",
    "seconds": 5,
    "size": "1280x720"
  }'
```

（示例里的 `model` 请替换成你模型列表里真实存在的视频模型名。）

结果长这样——创建成功返回一个任务对象：

```json
{
  "task_id": "video_xxx",
  "status": "queued",
  "model": "……",
  "created_at": 1763540000,
  "provider": "……",
  "provider_job_id": "……",
  "params_applied": ["model", "prompt", "seconds", "size"],
  "params_ignored": [],
  "metadata_applied": []
}
```

**立刻把 `task_id` 存下来**，轮询、下载、取消全靠它。

关于"受理"要知道的事：提交返回成功，代表上游已受理并给出了任务 id；如果上游认为参数不合法（400/413/422 类错误），错误会**直接透传**给你，网关不会悄悄换一个账户重试——按错误信息改参数后重新提交即可。

## 提交参数表

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `model` | 字符串 | 是 | 视频模型名；缺失报 503 `missing_model`，分组无账户支持报 503 `unsupported_model`（type 均为 `service_unavailable`） |
| `prompt` | 字符串 | 无图时必填 | 画面描述。主体、动作、镜头、风格写得越具体，效果越可控 |
| `image` | 字符串 | 否 | 图生视频首帧：image_url 或 base64 data URL 字符串；给了图可以省 `prompt` |
| `images` | 字符串数组 | 否 | `image` 的数组形态，当前只接受 1 个 |
| `input_reference` | 字符串 | 否 | 与 `image` 同义；多个同义字段同时给时取值必须一致 |
| `duration` / `seconds` | 数字 | 否 | 视频时长（秒），两个词同义，只能二选一或给相同值 |
| `size` | 字符串 | 否 | 分辨率，"宽x高" 像素串，如 `"1280x720"` |
| `width` / `height` | 整数 | 否 | 与 `size` 二选一（或给出与 `size` 一致的值），必须成对 |
| `fps` | 整数 | 否 | 帧率；仅部分模型支持，其余会在 `params_ignored` 回显 |
| `n` | 整数 | 否 | 生成条数，≥1 |
| `negative_prompt` | 字符串 | 否 | 负面提示：不希望出现的内容 |
| `seed` | 整数 | 否 | 随机种子，固定种子配合相同参数可复现同类结果 |
| `audio` | 布尔 | 否 | 是否生成带音频的视频 |
| `resolution` | 字符串 | 否 | 档位（如 `"720p"`）；与 `size` 不能同时给 |
| `response_format` | 字符串 | 否 | 只支持默认的 `"url"` |
| `metadata` | 对象 | 否 | 厂商专有参数（高级用法，见下） |

参数最终是否生效，以任务对象里的 `params_applied`（网关归一化后**实际生效**的参数）和 `params_ignored`（**被忽略**的参数）为准——上游模型不支持的字段会出现在 `params_ignored` 里，不算报错。

## 任务对象字段表

| 字段 | 说明 |
| --- | --- |
| `task_id` | 任务 id，`video_` 前缀，轮询/下载/取消都用它 |
| `status` | 当前状态：queued / in_progress / completed / failed / cancelled / expired |
| `progress` | 进度（0~100 的参考值，仅在轮询时可能返回） |
| `model` | 你提交时的模型名 |
| `url` | 成片下载地址（仅 `completed` 出现，指向 content 接口） |
| `format` | 成片格式（如 `mp4`） |
| `metadata` | 成片元数据：`duration`（秒）、`fps`、`width`、`height`、`seed`（按可得性返回） |
| `created_at` / `completed_at` | 创建时间 / 完成时间（Unix 秒） |
| `error` | 失败原因，仅 failed 时有用 |
| `provider` / `provider_job_id` | 实际承接的上游供应商与它在侧的任务 id |
| `params_applied` / `params_ignored` | 网关归一化后实际生效 / 被忽略的参数名列表（如 `["model","prompt","size"]`） |
| `metadata_applied` | `metadata` 扩展通道实际并入厂商报文的键名 |

## 第二步：轮询状态 GET /v1/video/generations/{task_id}

做什么：拿任务 id 循环查询，直到 `status` 进入终态（completed/failed/cancelled/expired）。轮询间隔自己定，下面以每 5 秒一次示意：

```bash
while : ; do
  sleep 5
  resp=$(curl -s "$BASE_URL/v1/video/generations/video_xxx" \
    -H "Authorization: Bearer $JUHE_AI_API_KEY")
  echo "$resp"
  status=$(echo "$resp" | sed -n 's/.*"status":"\([a-z_]*\)".*/\1/p')
  case "$status" in
    completed|failed|cancelled|expired) break ;;
  esac
done
```

结果怎么读：`progress` 一点点涨是正常现象；进入 `completed` 就可以下载（任务对象里的 `url` 就是下载地址）；进入 `failed` 就看任务对象里的 `error` 字段了解原因。

## 第三步：下载成片 GET /v1/video/generations/{task_id}/content

只有 `completed` 状态能下载，返回 mp4 二进制。用 `-o` 保存成文件：

```bash
curl -s "$BASE_URL/v1/video/generations/video_xxx/content" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -o cat.mp4
```

保存后用播放器打开确认。如果下载回来的不是视频而是一段 JSON，多半是任务还没到 `completed` 或 id 写错——先 `GET /v1/video/generations/{task_id}` 核对状态。

## 列表与取消

```bash
# 我提交过的任务列表（可选 ?limit=，默认 20、上限 100）
curl -s "$BASE_URL/v1/video/generations" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY"

# 取消一个还在排队/生成中的任务
curl -s -X DELETE "$BASE_URL/v1/video/generations/video_xxx" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY"
```

任务与提交它的 API Key 归属一致，查询、下载、取消都按任务归属进行。

## 完整可运行示例

三步串成一条龙，保存为 `video.sh` 执行（先 `export BASE_URL=...` 和 `export JUHE_AI_API_KEY=sk-...`）：

```bash
#!/usr/bin/env bash
set -e

# 1. 提交
resp=$(curl -s "$BASE_URL/v1/video/generations" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "sora-2",
    "prompt": "夕阳下的海边，浪花轻拍沙滩，慢镜头",
    "seconds": 5,
    "size": "1280x720"
  }')
echo "提交结果: $resp"
task_id=$(echo "$resp" | sed -n 's/.*"task_id":"\(video_[^"]*\)".*/\1/p')
echo "任务 id: $task_id"

# 2. 轮询（间隔仅示意）
status=""
while : ; do
  sleep 5
  resp=$(curl -s "$BASE_URL/v1/video/generations/$task_id" \
    -H "Authorization: Bearer $JUHE_AI_API_KEY")
  status=$(echo "$resp" | sed -n 's/.*"status":"\([a-z_]*\)".*/\1/p')
  echo "状态: $status"
  case "$status" in
    completed|failed|cancelled|expired) break ;;
  esac
done

# 3. 下载
if [ "$status" = "completed" ]; then
  curl -s "$BASE_URL/v1/video/generations/$task_id/content" \
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
| 400 `metadata_conflict` | `metadata` 里出现了规范字段名（如 `prompt`） | 规范字段放顶层，`metadata` 只放厂商专有参数 |
| 状态长期停在 `queued` | 分组账户忙或上游排队 | 继续等待；过久可 `DELETE` 取消后重提 |
| `status: failed` | 上游生成失败 | 看任务对象 `error` 字段，按提示调整 prompt/参数重试 |
| 下载返回的是 JSON 不是视频 | 任务不是 `completed`，或 id 写错 | 先查询确认状态再下载 |
| `status: expired` | 任务过期 | 重新提交一次 |

高级用法：`metadata` 可按厂商透传专有参数（对象或 JSON 字符串），例如 `{"metadata":{"camera_fixed":true}}`；键名不能与规范字段同名，实际并入的键会在 `metadata_applied` 回显。一般无需使用。

## 接下来

- 同样是异步任务形态的语音接口：《语音转文字》
- 文本转语音（同步、一次请求直接出音频）：《语音合成 TTS》
- 错误信封与按症状排障：《错误码与排障》
