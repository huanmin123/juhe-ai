export interface AiHealthRequestToken {
  signal: AbortSignal
  isCurrent: () => boolean
}

export function createAiHealthRequestCoordinator() {
  let listGeneration = 0
  let detailGeneration = 0
  let listController: AbortController | undefined
  let detailController: AbortController | undefined

  function beginList(): AiHealthRequestToken {
    listController?.abort()
    const controller = new AbortController()
    const generation = ++listGeneration
    listController = controller
    return {
      signal: controller.signal,
      isCurrent: () => generation === listGeneration && !controller.signal.aborted
    }
  }

  function beginDetail(): AiHealthRequestToken {
    detailController?.abort()
    const controller = new AbortController()
    const generation = ++detailGeneration
    detailController = controller
    return {
      signal: controller.signal,
      isCurrent: () => generation === detailGeneration && !controller.signal.aborted
    }
  }

  function cancelList(): void {
    listGeneration += 1
    listController?.abort()
    listController = undefined
  }

  function cancelDetail(): void {
    detailGeneration += 1
    detailController?.abort()
    detailController = undefined
  }

  function dispose(): void {
    cancelList()
    cancelDetail()
  }

  return { beginList, beginDetail, cancelList, cancelDetail, dispose }
}

export function isAiHealthCanceledRequest(error: unknown): boolean {
  const candidate = error as { code?: string; name?: string } | undefined
  return candidate?.code === 'ERR_CANCELED'
    || candidate?.name === 'CanceledError'
    || candidate?.name === 'AbortError'
}
