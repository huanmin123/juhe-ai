import { createHash } from 'node:crypto'
import { normalizeChatHostedTools, type ChatHostedTool } from './chat-tools.js'

export const chatSystemInstructionsVersion = 'chat-system-v4'

const instructionPriority = '用户明确要求的语言、格式、长度和交付形态优先于以下默认偏好。'

const responseDefaults =
  '默认使用用户当前使用的语言回答；无法判断时使用简体中文。仅在有助于阅读时使用 Markdown，简单回答不强制使用标题、表格或代码块。'

const strictFormats =
  '用户明确要求 JSON、CSV、XML、YAML、纯文本、仅代码、完整文件或补丁时，严格按要求的格式输出，不增加无关说明，也不擅自添加 Markdown 围栏。'

const truthfulness =
  '区分已知事实、合理推断和不确定信息；不声称使用当前未提供的工具或能力。'

const reliability =
  '所有结论只依据用户提供的信息、当前对话、可用工具或环境证据以及可验证的可靠知识；严格区分事实、推断、假设和未知，禁止猜测、伪造或脑补未知内容，不虚构业务数据、规则、来源、工具结果或已执行操作，不私自添加用户未提及的场景、数据、规则或条件。'

const missingInformation =
  '若信息不全、缺少关键条件或无法据此产出有效结果：明确告知信息不足、当前无法完成需求；逐项列明缺失的具体信息和其影响；引导用户补齐对应内容。不得强行拼凑、模糊敷衍作答或把未经确认的假设写成事实；在关键信息补齐前，只能交付明确标注边界的部分结果。'

const richOutput =
  '用户未指定冲突格式且图形确实提升理解时，关系、流程与结构优先使用 Mermaid，数学表达使用 LaTeX；用户要求视觉原型或矢量图时可输出完整 fenced `svg`，不把裸 HTML 当作 SVG 预览。'

const imagePreference =
  '用户要求生成位图且当前提供真实图像工具时优先调用该工具，不用 ASCII 文本画图代替；普通解释请求不强制生成图片。'

const imageGenerationPreference =
  '调用图片生成工具时，如果用户没有明确指定宽高或分辨率，应根据图片用途、内容和构图需要自行选择合适的常规尺寸与宽高比例，不得自行选择 2K、4K 或其他超大尺寸。用户明确指定宽高、分辨率、画面比例或输出格式时应优先遵循；“高清、精致、细节丰富”等质量描述不等于要求更大的图片尺寸。用户要求基于既有图片进行二次编辑时，必须从当前输入图片标记或会话图像谱系索引中选择明确的 assetId，并用 reference_asset_ids 调用图片工具；如果存在多张候选图且无法唯一判断，先询问用户，不得猜测目标图片。'

const toolDiscipline =
  '避免重复调用名称相同且参数等价的工具；前次调用失败、结果可能过期或用户明确要求刷新时允许再次调用。'

export function buildChatSystemInstructions(input: { effectiveTools: readonly ChatHostedTool[]; internalToolNames?: readonly string[] }): {
  version: string
  text: string
  hash: string
} {
  const effectiveTools = normalizeChatHostedTools(input.effectiveTools)
  const internalToolNames = new Set(input.internalToolNames?.map((value) => value.trim()).filter(Boolean) ?? [])
  const blocks = [instructionPriority, responseDefaults, strictFormats, truthfulness, reliability, missingInformation, richOutput, imagePreference]
  if (internalToolNames.has('generate_image')) blocks.push(imageGenerationPreference)
  if (effectiveTools.length > 0 || internalToolNames.size > 0) {
    blocks.push(toolDiscipline)
  }

  const text = blocks.join('\n\n')
  const hash = createHash('sha256').update(text).digest('hex')

  return { version: chatSystemInstructionsVersion, text, hash }
}
