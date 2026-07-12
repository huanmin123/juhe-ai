export type ChatComposerSnapshot = Record<string, unknown>

export interface ChatComposerSubmission {
  snapshot: ChatComposerSnapshot
  shouldRestore(status: 'completed' | 'failed' | 'canceled'): boolean
}

export function createChatComposerSubmission(document: ChatComposerSnapshot): ChatComposerSubmission {
  const snapshot = cloneDocument(document)
  return {
    snapshot,
    shouldRestore: (status) => status === 'failed'
  }
}

function cloneDocument(document: ChatComposerSnapshot): ChatComposerSnapshot {
  return JSON.parse(JSON.stringify(document)) as ChatComposerSnapshot
}
