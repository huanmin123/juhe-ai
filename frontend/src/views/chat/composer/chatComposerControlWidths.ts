export type ChatComposerControlKind = 'model' | 'reasoning' | 'service'

export interface ChatComposerControlWidths {
  triggerWidth: number
  popupWidth: number
}

const controlMinWidths: Record<ChatComposerControlKind, number> = {
  model: 112,
  reasoning: 92,
  service: 104
}
const controlMaxWidth = 200
const selectChromeWidth = 36
const graphemeSegmenter = new Intl.Segmenter(undefined, { granularity: 'grapheme' })

function isWideGrapheme(grapheme: string): boolean {
  if (/\p{Extended_Pictographic}|\p{Regional_Indicator}/u.test(grapheme)) return true
  const visualBase = grapheme.replace(/[\p{Mark}\u200d\ufe0e\ufe0f]/gu, '')
  return /[^\u0000-\u00ff]/u.test(visualBase)
}

function labelWidth(label: string): number {
  return Array.from(graphemeSegmenter.segment(label), ({ segment }) => segment)
    .reduce((width, grapheme) => width + (isWideGrapheme(grapheme) ? 14 : 7), 0) + selectChromeWidth
}

function boundedWidth(label: string, minWidth: number): number {
  return Math.min(controlMaxWidth, Math.max(minWidth, labelWidth(label)))
}

export function chatComposerControlWidths(
  kind: ChatComposerControlKind,
  selectedLabel: string | undefined,
  optionLabels: readonly string[]
): ChatComposerControlWidths {
  const minWidth = controlMinWidths[kind]
  const longestOption = optionLabels.reduce((longest, label) => labelWidth(label) > labelWidth(longest) ? label : longest, '')
  return {
    triggerWidth: boundedWidth(selectedLabel ?? '', minWidth),
    popupWidth: boundedWidth(longestOption, minWidth)
  }
}
