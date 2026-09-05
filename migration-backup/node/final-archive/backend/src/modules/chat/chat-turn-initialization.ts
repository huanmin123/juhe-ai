export async function initializeAcceptedChatTurn<T>(input: {
  initialize: () => Promise<T>
  failAcceptedTurn: () => Promise<void>
}): Promise<T> {
  try {
    return await input.initialize()
  } catch (error) {
    try {
      await input.failAcceptedTurn()
    } catch (finalizationError) {
      throw new AggregateError(
        [error, finalizationError],
        '聊天轮次初始化失败，且终结失败'
      )
    }
    throw error
  }
}
