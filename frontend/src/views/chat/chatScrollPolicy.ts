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

export function shouldBreakChatFollowOnScroll(previousOffset: number, currentOffset: number): boolean {
  return currentOffset < previousOffset - 1
}

export function shouldDetachChatFollowOnScroll(input: {
  previousOffset: number
  currentOffset: number
  now: number
  programmaticScrollUntil: number
}): boolean {
  return input.now > input.programmaticScrollUntil
    && shouldBreakChatFollowOnScroll(input.previousOffset, input.currentOffset)
}

export function shouldFollowChatViewportResize(input: { wasFollowing: boolean; userDetached: boolean }): boolean {
  return input.wasFollowing && !input.userDetached
}

export function resolveChatViewportResizeTransition(input: { followLatest: boolean; userDetached: boolean }): {
  followLatest: boolean
  userDetached: boolean
  shouldScroll: boolean
} {
  const shouldScroll = shouldFollowChatViewportResize({ wasFollowing: input.followLatest, userDetached: input.userDetached })
  return {
    followLatest: shouldScroll ? true : input.followLatest,
    userDetached: input.userDetached,
    shouldScroll
  }
}

export function resolveChatFollowState(input: { distance: number; userDetached: boolean }): { followLatest: boolean; userDetached: boolean } {
  if (input.distance <= chatResumeFollowThreshold) return { followLatest: true, userDetached: false }
  if (input.userDetached) return { followLatest: false, userDetached: true }
  return { followLatest: shouldFollowChatBottom(input.distance), userDetached: false }
}
