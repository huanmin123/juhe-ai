export const chatFollowBottomThreshold = 72
export const chatJumpButtonThreshold = chatFollowBottomThreshold

export function chatDistanceFromBottom(input: { scrollHeight: number; scrollTop: number; clientHeight: number }): number {
  return Math.max(0, input.scrollHeight - input.scrollTop - input.clientHeight)
}

export function shouldFollowChatBottom(distance: number): boolean {
  return distance <= chatFollowBottomThreshold
}

export function shouldShowChatJumpButton(distance: number): boolean {
  return distance > chatJumpButtonThreshold
}

export function shouldBreakChatFollowOnWheel(deltaY: number): boolean {
  return deltaY < 0
}
