export function isCurrentChatConversationLoad(input: {
  conversationId: string
  selectedConversationId?: string
  epoch: number
  currentEpoch: number
  disposed: boolean
}): boolean {
  return !input.disposed
    && input.epoch === input.currentEpoch
    && input.conversationId === input.selectedConversationId
}

export type ChatConversationSideLoadResult<T> =
  | { ok: true; value: T }
  | { ok: false; error: unknown }

export function startChatConversationLoad<TMessage, TModel>(input: {
  loadMessages: () => Promise<TMessage[]>
  loadModels: () => Promise<TModel[]>
}): {
  messages: Promise<TMessage[]>
  models: Promise<ChatConversationSideLoadResult<TModel[]>>
} {
  const messages = Promise.resolve().then(input.loadMessages)
  const models = Promise.resolve()
    .then(input.loadModels)
    .then<ChatConversationSideLoadResult<TModel[]>, ChatConversationSideLoadResult<TModel[]>>(
      (value) => ({ ok: true, value }),
      (error: unknown) => ({ ok: false, error })
    )
  return { messages, models }
}
