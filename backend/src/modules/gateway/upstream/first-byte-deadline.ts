export type FirstByteDeadlineAction = 'continue' | 'abort'

export interface FirstByteDeadlineDecisionInput {
  elapsedMs: number
  timeoutMs: number
  transport: 'stream' | 'non_stream'
}

export type FirstByteDeadlineHandler = (
  input: FirstByteDeadlineDecisionInput
) => FirstByteDeadlineAction | Promise<FirstByteDeadlineAction>
