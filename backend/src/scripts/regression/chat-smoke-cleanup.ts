export interface ChatSmokeCleanupStep {
  name: string
  run: () => Promise<void>
}

export async function runChatSmokeWithCleanup(input: {
  run: () => Promise<void>
  cleanupSteps: readonly ChatSmokeCleanupStep[]
  onSuccess: () => void
}): Promise<void> {
  let runError: unknown
  try {
    await input.run()
  } catch (error) {
    runError = error
  }

  const cleanupErrors: unknown[] = []
  for (const step of input.cleanupSteps) {
    try {
      await step.run()
    } catch (error) {
      cleanupErrors.push(error)
    }
  }

  if (runError !== undefined && cleanupErrors.length > 0) {
    throw new AggregateError([runError, ...cleanupErrors], 'AI 问答 smoke 执行失败，且清理失败')
  }
  if (runError !== undefined) throw runError
  if (cleanupErrors.length === 1) throw cleanupErrors[0]
  if (cleanupErrors.length > 1) throw new AggregateError(cleanupErrors, 'AI 问答 smoke 清理失败')
  input.onSuccess()
}
