# 语音合成 TTS

> 这一篇解决：POST /v1/audio/speech 怎么把一段文本变成一个音频文件——参数怎么填、响应怎么保存、常见错误怎么对号。

## 开始之前

1. 一把可用的 API Key。认证只有一种方式：请求头 `Authorization: Bearer <API Key>`（Bearer 大小写不敏感），见《API Key 与认证》。
2. 一个支持语音合成的模型名，以 `GET /v1/models` 实际返回为准。
3. 认知预期：TTS 是**同步接口**——一次请求、稍等片刻，响应体直接就是音频数据本身，不是 JSON，也不是任务 id。

先分清 juhe-ai 里几个媒体接口的形态，避免用错读取方式：

| 接口 | 形态 | 出结果方式 |
| --- | --- | --- |
| `POST /v1/audio/speech`（本篇） | 同步 | 响应体直接是音频二进制，存文件即得 |
| `POST /v1/audio/transcriptions`（短音频转写） | 同步 | 响应体直接是文本 JSON |
| `POST /v1/audio/jobs`（长音频转写）、`POST /v1/videos`（视频） | 异步 | 先拿任务 id，轮询完成后另行取结果 |

本篇的 `model` 同样必须是你的 Key 绑定分组支持的模型：缺失报 503 `missing_model`，分组无账户支持报 503 `unsupported_model`（type 均为 `service_unavailable`）（message 形如「当前分组无账户支持请求模型：X」）。

## 第一步：发起合成请求

做什么：告诉接口用哪个模型、什么音色、读哪段文字。必填只有三个字段：`model`、`input`、`voice`：

```bash
curl -s "$BASE_URL/v1/audio/speech" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.4",
    "voice": "alloy",
    "input": "你好，欢迎来到 juhe-ai。"
  }'
```

（示例里的 `model`、`voice` 都请替换成真实可用的值：模型名以 `GET /v1/models` 为准；音色名**以模型支持的音色为准**，参考所用模型/供应商的音色清单。）

## 第二步：把响应保存成音频文件

响应不是 JSON，而是**音频二进制流**，`Content-Type` 是对应的音频类型。所以不要用解析 JSON 的方式处理它，直接把响应体写成文件：

```bash
curl -s "$BASE_URL/v1/audio/speech" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.4",
    "voice": "alloy",
    "input": "你好，欢迎来到 juhe-ai。",
    "response_format": "mp3"
  }' -o hello.mp3
```

结果：当前目录出现 `hello.mp3`，用播放器打开确认。

排错小技巧：如果你误把响应当 JSON 解析而报错，先看响应内容——若是一段 `{"error":{...}}`，说明请求其实失败了，按错误信封里的信息修正请求；若是一堆乱码般的二进制，说明合成成功，是你处理方式不对。

三个快速校验，确认拿到的是真音频：

1. **看文件大小**：只有几 KB 且播放器打不开，多半是把错误 JSON 存了下来，用文本编辑器打开看一眼是否为错误信封。
2. **看响应头**：把 `-s` 换成 `curl -s -D - -o hello.mp3 ...`，响应头里 `Content-Type` 为对应音频类型即为正常。
3. **对后缀**：文件后缀要与 `response_format` 一致，部分播放器靠后缀识别格式。

## 参数表

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `model` | 字符串 | 是 | TTS 模型名；缺失报 503 `missing_model`，分组无账户支持报 503 `unsupported_model`（type 均为 `service_unavailable`） |
| `input` | 字符串 | 是 | 要合成的文本 |
| `voice` | 字符串 | 是 | 音色名，以模型支持的音色为准 |
| `speed` | 数字 | 否 | 语速倍率 |
| `response_format` | 字符串 | 否 | 输出格式：mp3 / opus / aac / flac / wav / pcm |
| `instructions` | 字符串 | 否 | 合成风格指令，如「用平静的语气朗读」 |
| `language` | 字符串 | 否 | 语言提示 |

两个取值从哪来：

- `model`：`GET /v1/models` 返回清单里真实存在的 TTS 模型名，不要凭印象猜。
- `voice`：模型支持的音色名。同样一段文本换不同 `voice` 各合成一次对比试听，是最快的选音色方法；选好后再用 `instructions` 微调风格。

## 第三步：批量合成多段文本

接口一次处理一段 `input`。要合成多段，循环逐条请求、逐条保存即可：

```bash
#!/usr/bin/env bash
# 前置：export BASE_URL=... ; export JUHE_AI_API_KEY=sk-...
i=0
while IFS= read -r line; do
  i=$((i+1))
  curl -s "$BASE_URL/v1/audio/speech" \
    -H "Authorization: Bearer $JUHE_AI_API_KEY" \
    -H "Content-Type: application/json" \
    -d "{
      \"model\": \"gpt-5.4\",
      \"voice\": \"alloy\",
      \"input\": \"$line\",
      \"response_format\": \"mp3\"
    }" -o "clip_$i.mp3"
done < lines.txt
```

`lines.txt` 里每行一段文本，跑完得到 `clip_1.mp3`、`clip_2.mp3`……

## 完整可运行示例

换格式、调语速、加风格指令的组合示例（保存为 `tts.sh` 执行）：

```bash
#!/usr/bin/env bash
curl -s "$BASE_URL/v1/audio/speech" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.4",
    "voice": "alloy",
    "input": "今天是十月六日，下午三点有一场会议。",
    "speed": 1.1,
    "response_format": "wav",
    "instructions": "用平稳清晰的播报语气朗读"
  }' -o notice.wav
```

预期结果：得到 `notice.wav`，语速略快于默认，语气平稳。文件后缀要和 `response_format` 一致，否则有的播放器会拒绝识别。

`response_format` 怎么挑：要直接播放、分享，选 `mp3` 或 `wav` 最省事；`opus` / `aac` / `flac` / `pcm` 更适合有特定解码需求的程序化场景，其中 `pcm` 是无文件头的原始音频样本（OpenAI 语义），一般不适合直接双击播放。

## 计费怎么算

TTS 按字符计费——`input` 越长费用越高，与生成音频的时长无关地取决于你送进去多少字。具体以模型目录展示的单价与用量记录为准，可在管理台对账，见《用量与统计》。

## 常见问题或常见错误

统一错误信封：`{"error":{"message":"...","type":"...","code":"..."}}`

| 症状 | 原因 | 处理 |
| --- | --- | --- |
| 503 `missing_model` / `unsupported_model`（type 均为 `service_unavailable`），message 形如「当前分组无账户支持请求模型：X」 | 没传 `model`、模型名写错或分组不支持 | 以 `GET /v1/models` 为准核对 |
| 400 `invalid_request_error` | 缺 `input`/`voice`，或 `response_format` 取值不在支持列表 | 对照参数表补全、修正取值 |
| 把响应当 JSON 解析报错 | 响应是音频二进制，不是 JSON | 按 `-o` 存文件；若内容是错误信封再按错误处理 |
| 存出来的文件播放不了 | 后缀与 `response_format` 不一致（如格式是 opus 却存成 .mp3） | 文件后缀与 `response_format` 保持一致 |
| 音色相关报错 | `voice` 不在该模型支持的音色里 | 换用模型支持的音色名 |
| 想控制语气、停顿、播报风格 | 风格由 `instructions` 与文本本身的标点共同影响 | 在 `instructions` 里写明风格，文本里用好标点 |
| 5xx，`type` 为 `server_error` / `service_unavailable` / `upstream_error` | 网关或上游临时故障 | 原样重试，反复失败按《错误码与排障》取证 |

## 接下来

- 反方向：音频变文字（同步与异步两种方式）：《语音转文字》
- 生成视频内容（异步任务式）：《视频生成：从提交到取片》
- 看每次合成的花费与用量记录：《用量与统计》
