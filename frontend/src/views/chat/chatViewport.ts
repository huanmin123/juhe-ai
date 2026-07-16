export function resolveChatViewportHeight(input: { visualViewportHeight?: number; innerHeight?: number }): number | undefined {
  const visualHeight = input.visualViewportHeight
  if (Number.isFinite(visualHeight) && (visualHeight ?? 0) > 0) return Math.round(visualHeight!)
  const innerHeight = input.innerHeight
  if (Number.isFinite(innerHeight) && (innerHeight ?? 0) > 0) return Math.round(innerHeight!)
  return undefined
}

export interface ChatViewportBounds {
  left: number
  top: number
  right: number
  bottom: number
  width: number
  height: number
}

export function resolveChatVisualViewportBounds(input: {
  offsetLeft?: number
  offsetTop?: number
  width?: number
  height?: number
  innerWidth: number
  innerHeight: number
}): ChatViewportBounds {
  const width = Number.isFinite(input.width) && (input.width ?? 0) > 0 ? input.width! : input.innerWidth
  const height = Number.isFinite(input.height) && (input.height ?? 0) > 0 ? input.height! : input.innerHeight
  const left = Number.isFinite(input.offsetLeft) ? input.offsetLeft! : 0
  const top = Number.isFinite(input.offsetTop) ? input.offsetTop! : 0
  return { left, top, right: left + width, bottom: top + height, width, height }
}

export function clampChatFloatingMenuPosition(input: {
  preferredX: number
  preferredY: number
  menuWidth: number
  menuHeight: number
  viewport: ChatViewportBounds
  padding: number
}): { x: number; y: number } {
  const minX = input.viewport.left + input.padding
  const minY = input.viewport.top + input.padding
  const maxX = Math.max(minX, input.viewport.right - input.menuWidth - input.padding)
  const maxY = Math.max(minY, input.viewport.bottom - input.menuHeight - input.padding)
  return {
    x: Math.max(minX, Math.min(input.preferredX, maxX)),
    y: Math.max(minY, Math.min(input.preferredY, maxY))
  }
}
