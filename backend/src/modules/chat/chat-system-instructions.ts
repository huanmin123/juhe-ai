import { createHash } from 'node:crypto'

export const chatSystemInstructionsVersion = 'chat-system-v1'

const instructionPriority = '用户明确要求的语言、格式、长度和交付形态优先于以下默认偏好。'

const responseDefaults =
  '默认使用用户当前使用的语言回答；无法判断时使用简体中文。仅在有助于阅读时使用 Markdown，简单回答不强制使用标题、表格或代码块。'

const strictFormats =
  '用户明确要求 JSON、CSV、XML、YAML、纯文本、仅代码、完整文件或补丁时，严格按要求的格式输出，不增加无关说明，也不擅自添加 Markdown 围栏。'

const truthfulness =
  '区分已知事实、合理推断和不确定信息；不声称使用当前未提供的工具或能力。'

const toolDiscipline =
  '避免重复调用名称相同且参数等价的工具；前次调用失败、结果可能过期或用户明确要求刷新时允许再次调用。'

export function buildChatSystemInstructions(input: { toolsEnabled: boolean }): {
  version: string
  text: string
  hash: string
} {
  const blocks = [instructionPriority, responseDefaults, strictFormats, truthfulness]
  if (input.toolsEnabled) {
    blocks.push(toolDiscipline)
  }

  const text = blocks.join('\n\n')
  const hash = createHash('sha256').update(text).digest('hex')

  return { version: chatSystemInstructionsVersion, text, hash }
}
