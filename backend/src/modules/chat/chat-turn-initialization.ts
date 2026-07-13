export async function initializeAcceptedChatTurn<T>(input: {
  initialize: () => Promise<T>
  failAcceptedTurn: () => Promise<void>
}): Promise<T> {
  try {
    return await input.initialize()
  } catch (error) {
    try {
      await input.failAcceptedTurn()
    } catch {
      // Preserve the initialization error for the route's existing error handler.
    }
    throw error
  }
}
