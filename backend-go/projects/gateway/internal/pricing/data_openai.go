package pricing

// OpenAI pricing snapshot, ported from backend/src/modules/model-pricing/
// openai-model-pricing.{data,gpt4,gpt5,image,reasoning}.data.ts. Token prices
// are USD per token literals identical to the Node snapshot rows.
//
// 2026-10-05 全厂商补全批（依据 docs/plans/计划-20261005T110000000Z-模型
// 目录全厂商补全与场景化展示.md §3.1/§3.2/§2，来源 = 计划 §6.1：
// platform.openai.com/docs/pricing.md + deprecations.md + docs/models）：
//   - 新增 11 行：gpt-realtime-2.1 / gpt-realtime-2 / gpt-realtime-2.1-mini
//     / gpt-realtime-1.5 / gpt-audio-1.5 / gpt-live-1 / gpt-transcribe /
//     gpt-live-transcribe / gpt-realtime-whisper / gpt-realtime-translate /
//     gpt-5.6-cyber（价格全部 pricing.md 明文；新增行 ReleaseDate 无官方
//     明文的一律 nil）。
//   - 修正 22 行：gpt-image-2.5-sunburst/flare 图价 $8/$30（原 $10/$40，
//     2 行）；sora-2/sora-2-pro（2026-09-24）、gpt-5.3-codex/gpt-5.1/
//     gpt-5.4-nano（2027-04-01）、whisper-1/gpt-4o-transcribe/
//     gpt-4o-mini-transcribe（2027-02-26）、tts-1/tts-1-hd/gpt-4o-mini-tts
//     （2027-01-06）、gpt-realtime（2027-01-20）补 ShutdownDate
//     （deprecations.md 明文，只落官方明文日期，裁决 §2.3，12 行）；
//     gpt-5.5/5.5-pro/5.4/5.4-pro 落长上下文档（>272k：in 2x / out 1.5x，
//     含各自 -YYYY-MM-DD 快照行，同一模型同价卡，8 行）；gpt-6-astra
//     Ultrafast 档注释登记（价格结构无 ultrafast 通道，不结构化，
//     裁决 §2.6 同型）。
//   - chat-latest、gpt-rosalind-research 官方在售但不收录（计划 §3.3）。
//
// 2026-10-05 遗留取证批（pricing.md 官方表为唯一真值逐行对照）：
// realtime/audio 族补齐——新增 3 行（gpt-realtime-mini / gpt-audio /
// gpt-audio-mini，补全批漏收、§3.3 未列）；存量 realtime 系 5 行补 Image
// 输入价（$5.00，mini 系 $0.80；image cached 与 text/audio cached 异值，
// 模态无关 cached 通道按 text/audio 主模态计、差异注释登记）；
// gpt-realtime-1.5 补 cached $0.40（补全批漏落，text/audio cached 同值
// 与 2.1 同理）；旧 gpt-realtime 行按新口径补 text $4/$16 + cached
// $0.40 + image $5（「多源分歧不落」旧口径废止，快照门禁断言同步）。
// gpt-5.5-cyber 维持不收录（证据修正：pricing.md .md 源 Cyber 表有可见
// 行 $12.50/$1.25/-/$75 而渲染页无，models 页 404——两源矛盾，调用
// 可用性未证明不落）。
//
// openAIModelPricingData = openAIGPT4 + openAIGPT5 + openAIImage +
// openAIReasoning + openAIAudio + openAIVideo (order preserved).
var openAIModelPricingData = mustLoadProviderCatalogModels("gpt")

// gpt5ToolsGpt5Dot6 mirrors the shared hosted tool list of the GPT-5.6 family
// rows; the *ByProtocol matrices split it across the row protocols (responses
// carries the hosted set, chat_completions only function_calling).
var gpt5ToolsGpt5Dot6 = []string{"function_calling", "web_search", "file_search", "image_generation", "code_interpreter", "hosted_shell", "apply_patch", "skills", "computer_use", "mcp", "tool_search"}

var gpt5ToolsGpt5Dot6ByProtocol = toolsByProtocol([]string{"chat_completions", "responses"}, gpt5ToolsGpt5Dot6)

// gpt5ToolsGpt61Sol mirrors the GPT-6.1 Sol hosted tool list (same set as
// the GPT-5.6 family), but the 2026-09-30 model page restricts tool calling
// to the Responses API: Chat Completions is supported without tool calling,
// so the matrix carries no chat_completions key (consumers treat a missing
// protocol key as an empty tool set).
var gpt5ToolsGpt61SolByProtocol = toolsByProtocol([]string{"responses"}, gpt5ToolsGpt5Dot6)

// gpt5ToolsGpt55 mirrors the GPT-5.5 family hosted tool list.
var gpt5ToolsGpt55 = []string{"function_calling", "web_search", "file_search", "tool_search", "image_generation", "code_interpreter", "hosted_shell", "apply_patch", "skills", "computer_use", "mcp"}

var gpt5ToolsGpt55ByProtocol = toolsByProtocol([]string{"chat_completions", "responses"}, gpt5ToolsGpt55)

// gpt5ToolsGpt54Mini mirrors the GPT-5.4/mini family tool list.
var gpt5ToolsGpt54Mini = gpt5ToolsGpt55

var gpt5ToolsGpt54MiniByProtocol = gpt5ToolsGpt55ByProtocol

// gpt5ToolsGpt54Nano mirrors the GPT-5.4-nano tool list.
var gpt5ToolsGpt54Nano = []string{"function_calling", "web_search", "file_search", "image_generation", "code_interpreter", "hosted_shell", "apply_patch", "skills", "mcp"}

var gpt5ToolsGpt54NanoByProtocol = toolsByProtocol([]string{"chat_completions", "responses"}, gpt5ToolsGpt54Nano)

// gpt5ToolsProCodex543 mirrors the GPT-5 pro / codex tool lists that share
// the same content (responses-only rows).
var gpt5ToolsProCodex543 = []string{"function_calling", "web_search", "file_search", "tool_search", "image_generation", "apply_patch", "computer_use", "mcp"}

var gpt5ToolsProCodex543ByProtocol = toolsByProtocol([]string{"responses"}, gpt5ToolsProCodex543)
