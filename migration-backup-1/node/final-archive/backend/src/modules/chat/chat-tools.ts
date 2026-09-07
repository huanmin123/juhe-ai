export const chatHostedTools = ['web_search', 'image_generation'] as const

export type ChatHostedTool = typeof chatHostedTools[number]

const chatHostedToolSet = new Set<unknown>(chatHostedTools)

export function normalizeChatHostedTools(input: readonly unknown[] | undefined): ChatHostedTool[] {
  const selected = new Set(input?.filter((value): value is ChatHostedTool => chatHostedToolSet.has(value)) ?? [])
  return chatHostedTools.filter((tool) => selected.has(tool))
}

export function mapChatHostedToolsToResponses(input: readonly unknown[] | undefined): Array<{ type: ChatHostedTool }> {
  return normalizeChatHostedTools(input).map((type) => ({ type }))
}

export function shouldOfferChatImageGenerationTool(content: string): boolean {
  const normalized = content.trim().replace(/\s+/gu, ' ')
  if (!normalized) return false
  const structuralDiagram = /(?:流程图|时序图|序列图|状态图|架构图|系统架构|调用链|关系图|拓扑图|组件图|部署图|类图|实体关系图|泳道图|思维导图|脑图|数据图|折线图|柱状图|饼图|图表|示意图|函数图|坐标图|mermaid|latex|svg)/iu
  const englishStructuralDiagram = /\b(?:flowchart|diagram|chart|graph|call\s+graph|sequence\s+diagram|state\s+diagram|architecture\s+diagram|system\s+architecture|component\s+diagram|deployment\s+diagram|class\s+diagram|entity\s+relationship\s+diagram|mind\s+map|mermaid|latex|svg)\b/iu
  if (structuralDiagram.test(normalized) || englishStructuralDiagram.test(normalized)) return false
  if (/(?:生图|出图)/u.test(normalized)) return true
  const chineseVerb = '(?:生成|创建|绘制|制作|设计|画)'
  const chineseNoun = '(?:图片|图像|插画|海报|图标|logo|照片|画面)'
  if (new RegExp(`${chineseVerb}[^。！？\\n]{0,24}${chineseNoun}`, 'iu').test(normalized)) return true
  const englishVerb = '(?:generate|create|draw|make|render|design|produce)'
  const englishNoun = '(?:image|picture|illustration|poster|icon|logo|photo|artwork)'
  if (new RegExp(`\\b${englishVerb}\\b[^.!?\\n]{0,48}\\b${englishNoun}\\b`, 'iu').test(normalized)) return true

  const directChineseDrawing = /^(?:(?:请|麻烦)(?:帮我|给我|为我)?|(?:帮我|给我|为我))?(?:画|绘制|创作)(?:一下|一个|一只|一张|一幅|出)?[^。！？\n]{1,64}$/iu
  if (directChineseDrawing.test(normalized)) return true

  const directEnglishDrawing = /^(?:please\s+)?(?:draw|paint|illustrate)\b[^.!?\n]{1,96}$/iu
  return directEnglishDrawing.test(normalized)
}
