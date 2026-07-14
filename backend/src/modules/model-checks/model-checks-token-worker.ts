interface ModelCheckTokenWorkerOperation {
  type: 'prepare_token_probe_prompt'
  prefix: string
  targetTokens: number
}

interface ModelCheckTokenWorkerRequest {
  requestId: string
  operation: ModelCheckTokenWorkerOperation
}

const {
  buildModelCheckTokenProbePrompt
} = await import(resolveTokenIntegrityModuleUrl()) as typeof import('./model-checks-token-integrity.js')

process.on('message', (message: ModelCheckTokenWorkerRequest) => {
  const requestId = message?.requestId
  if (!requestId) return
  try {
    if (message.operation?.type !== 'prepare_token_probe_prompt') {
      throw new Error('模型检测 Token worker 操作无效')
    }
    process.send?.({
      requestId,
      ok: true,
      result: {
        ...buildModelCheckTokenProbePrompt(message.operation.prefix, message.operation.targetTokens),
        workerPid: process.pid
      }
    })
  } catch (error) {
    process.send?.({
      requestId,
      ok: false,
      errorMessage: error instanceof Error ? error.message : String(error)
    })
  }
})

process.once('disconnect', () => process.exit(0))

function resolveTokenIntegrityModuleUrl(): string {
  const extension = import.meta.url.endsWith('.ts') ? '.ts' : '.js'
  return new URL(`./model-checks-token-integrity${extension}`, import.meta.url).href
}
