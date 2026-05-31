import axios from 'axios'

export function extractApiErrorMessage(error: unknown, fallback: string): string {
  if (axios.isAxiosError(error)) {
    return extractResponseErrorMessage(error.response?.data)
      ?? localizeTransportErrorMessage(error.message, fallback)
  }
  return error instanceof Error ? localizeTransportErrorMessage(error.message, fallback) : fallback
}

export function extractResponseErrorMessage(data: unknown): string | undefined {
  if (!isRecord(data)) return undefined
  const message = stringValue(data.message)
  if (message) return message
  const error = data.error
  if (isRecord(error)) {
    return stringValue(error.message)
  }
  return undefined
}

export function localizeTransportErrorMessage(message: string | undefined, fallback: string): string {
  const text = message?.trim()
  if (!text) return fallback
  if (isCommonEnglishNetworkError(text)) return '网络请求失败，请检查网络或稍后重试'
  if (/timeout of \d+ms exceeded/i.test(text) || /request timed out/i.test(text)) return '请求超时，请稍后重试'
  if (/^request failed with status code \d+$/i.test(text)) return fallback
  if (/failed to fetch|fetch failed/i.test(text)) return '网络请求失败，请稍后重试'
  return text
}

function isCommonEnglishNetworkError(text: string): boolean {
  return /^network error$/i.test(text)
    || /^request aborted$/i.test(text)
    || /^load failed$/i.test(text)
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}
