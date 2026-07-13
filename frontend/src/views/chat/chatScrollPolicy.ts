export const chatFollowBottomThreshold = 72
export const chatJumpButtonThreshold = chatFollowBottomThreshold
export const chatResumeFollowThreshold = 4

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

export function resolveChatFollowState(input: { distance: number; userDetached: boolean }): { followLatest: boolean; userDetached: boolean } {
  if (input.distance <= chatResumeFollowThreshold) return { followLatest: true, userDetached: false }
  if (input.userDetached) return { followLatest: false, userDetached: true }
  return { followLatest: shouldFollowChatBottom(input.distance), userDetached: false }
}
