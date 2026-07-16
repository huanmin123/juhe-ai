export type ChatSubmissionRecoveryDecision =
  | { action: 'retry' }
  | { action: 'wait' }
  | { action: 'stop' }
  | { action: 'recover_completed' }
  | { action: 'fail'; reason: string }

export function decideChatSubmissionRecovery(input: {
  state: 'not_found' | 'preparing' | 'accepted'
  assistantStatus?: string
  attempt: number
  timedOut: boolean
}): ChatSubmissionRecoveryDecision {
  if (input.state === 'not_found') return input.attempt < 2 ? { action: 'retry' } : { action: 'fail', reason: 'retry_exhausted' }
  if (input.state === 'preparing') return input.timedOut ? { action: 'fail', reason: 'preparation_timeout' } : { action: 'wait' }
  if (input.assistantStatus === 'completed') return { action: 'recover_completed' }
  if (input.assistantStatus === 'streaming') return input.timedOut ? { action: 'stop' } : { action: 'wait' }
  return { action: 'fail', reason: `assistant_${input.assistantStatus ?? 'unknown'}` }
}
