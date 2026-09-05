import type { NextFunction, Request, Response } from 'express'

const preserveUpstreamErrorMessageKey = '__juhePreserveUpstreamErrorMessage'
const chineseCharacterPattern = /[\u3400-\u9fff]/u

export function systemErrorMessageForStatus(statusCode: number): string {
  switch (statusCode) {
    case 400:
      return '请求参数无效'
    case 401:
      return '身份验证失败，请检查访问凭据'
    case 403:
      return '无权执行此操作'
    case 404:
      return '请求的资源不存在'
    case 405:
      return '请求方法不被支持'
    case 409:
      return '请求状态已发生变化，请刷新后重试'
    case 413:
      return '请求内容过大'
    case 422:
      return '请求内容无法处理'
    case 429:
      return '请求过于频繁，请稍后重试'
    case 502:
      return '服务处理上游响应失败，请稍后重试'
    case 503:
      return '服务暂时不可用，请稍后重试'
    case 504:
      return '服务处理超时，请稍后重试'
    default:
      return '请求处理失败，请稍后重试'
  }
}

export function localizeSystemErrorMessage(message: string, statusCode: number): string {
  const normalized = message.trim()
  if (normalized && chineseCharacterPattern.test(normalized)) {
    return message
  }
  return systemErrorMessageForStatus(statusCode)
}

export function markResponseErrorMessageAsUpstream(res: Response): void {
  res.locals[preserveUpstreamErrorMessageKey] = true
}

export function isResponseErrorMessageMarkedAsUpstream(res: Response): boolean {
  return res.locals[preserveUpstreamErrorMessageKey] === true
}

export function systemErrorMessageLocalizationMiddleware(_req: Request, res: Response, next: NextFunction): void {
  const originalJson = res.json.bind(res)
  res.json = ((body: unknown) => originalJson(localizeSystemErrorPayload(
    body,
    res.statusCode,
    isResponseErrorMessageMarkedAsUpstream(res)
  ))) as Response['json']
  next()
}

export function localizeSystemErrorPayload(
  payload: unknown,
  statusCode: number,
  preserveUpstreamErrorMessage = false
): unknown {
  if (statusCode < 400 || preserveUpstreamErrorMessage) {
    return payload
  }
  if (typeof payload === 'string') {
    return localizeSystemErrorMessage(payload, statusCode)
  }
  if (!isRecord(payload)) {
    return payload
  }

  let changed = false
  const output = { ...payload }
  if (typeof output.message === 'string') {
    const localized = localizeSystemErrorMessage(output.message, statusCode)
    if (localized !== output.message) {
      output.message = localized
      changed = true
    }
  }
  if (isRecord(output.error) && typeof output.error.message === 'string') {
    const localized = localizeSystemErrorMessage(output.error.message, statusCode)
    if (localized !== output.error.message) {
      output.error = { ...output.error, message: localized }
      changed = true
    }
  }
  return changed ? output : payload
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
