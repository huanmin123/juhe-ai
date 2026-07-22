export interface ChatSvgPreviewSize {
  width: number
  height: number
}

export function isCompleteStaticSvg(source: string): boolean {
  const value = source.trim()
  return /^<svg\b[\s\S]*<\/svg>$/iu.test(value)
}

export function resolveChatSvgPreviewSize(source: string): ChatSvgPreviewSize {
  const root = source.match(/^<svg\b([^>]*)>/iu)?.[1] ?? ''
  const viewBox = root.match(/\bviewBox\s*=\s*["']\s*[-+]?\d+(?:\.\d+)?\s+[-+]?\d+(?:\.\d+)?\s+([\d.]+)\s+([\d.]+)\s*["']/iu)
  const width = boundedDimension(root.match(/\bwidth\s*=\s*["']\s*([\d.]+)/iu)?.[1], viewBox?.[1], 640)
  const height = boundedDimension(root.match(/\bheight\s*=\s*["']\s*([\d.]+)/iu)?.[1], viewBox?.[2], 360)
  return { width, height }
}

function boundedDimension(primary: string | undefined, secondary: string | undefined, fallback: number): number {
  const value = Number(primary ?? secondary)
  return Number.isFinite(value) && value > 0 ? Math.min(960, Math.max(120, Math.round(value))) : fallback
}
