export async function stopActiveChatGeneration(input: {
  controller?: AbortController
  stop: () => Promise<unknown>
  sendSettled?: Promise<unknown>
}): Promise<void> {
  input.controller?.abort()
  await Promise.allSettled([input.stop(), input.sendSettled ?? Promise.resolve()])
}
