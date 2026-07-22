export interface AccountDiagnosticMessageParts {
  message: string
  traceId?: string
  requestId?: string
}

export function splitAccountDiagnosticMessage(message?: string): AccountDiagnosticMessageParts {
  let remaining = message?.trim() ?? ''
  if (!remaining) return { message: '' }

  const traceIdMatch = remaining.match(/(?:^|[；;\s,，])(?:traceId|trace id)(?:[：:]|\s+)([^\s；;，,)]+)/i)
  const traceId = traceIdMatch?.[1]?.trim()
  if (traceIdMatch) {
    remaining = removeDiagnosticSegment(remaining, traceIdMatch.index ?? 0, traceIdMatch[0].length)
  }

  const parenthesizedRequestIdMatch = remaining.match(/\((?:upstream\s+)?(?:request\s*id|requestId)[：:]\s*([^)]+?)\)/i)
  let requestId = parenthesizedRequestIdMatch?.[1]?.trim()
  if (parenthesizedRequestIdMatch) {
    remaining = removeDiagnosticSegment(remaining, parenthesizedRequestIdMatch.index ?? 0, parenthesizedRequestIdMatch[0].length)
  }

  if (!requestId) {
    const requestIdMatch = remaining.match(/(?:^|[；;\s,，])(?:upstream\s+)?(?:request\s*id|requestId)[：:]\s*([^\s；;，,)]+)/i)
    requestId = requestIdMatch?.[1]?.trim()
    if (requestIdMatch) {
      remaining = removeDiagnosticSegment(remaining, requestIdMatch.index ?? 0, requestIdMatch[0].length)
    }
  }

  return {
    message: cleanupDiagnosticMessage(remaining),
    traceId,
    requestId
  }
}

export function accountDiagnosticTooltipLines(
  message: string | undefined,
  options: { reasonLabel: string; idLabelPrefix?: string; statusCode?: number; concise?: boolean }
): string[] {
  const text = options.concise ? conciseAccountLastErrorText(message) : message?.trim()
  if (!text) return []
  const parts = splitAccountDiagnosticMessage(text)
  let reason = parts.message
  if (options.statusCode) {
    reason = reason.replace(new RegExp(`^HTTP ${options.statusCode}[；;\\s]*`), '').trim()
  }
  const idLabelPrefix = options.idLabelPrefix ? `${options.idLabelPrefix} ` : ''
  const lines: string[] = []
  if (parts.traceId) {
    lines.push(`${idLabelPrefix}traceId：${parts.traceId}`)
  }
  if (parts.requestId) {
    lines.push(`${idLabelPrefix}request id：${parts.requestId}`)
  }
  if (reason) {
    lines.push(`${options.reasonLabel}：${reason}`)
  }
  return lines
}

export function conciseAccountLastErrorText(message?: string): string {
  const value = message?.trim()
  if (!value) return ''
  const lastErrorMarker = '最后错误：'
  const markerIndex = value.lastIndexOf(lastErrorMarker)
  return markerIndex >= 0
    ? value.slice(markerIndex + lastErrorMarker.length).trim()
    : value
      .replace(/^账户测试失败，已自动标记为临时不可调用；/, '')
      .replace(/^账户测试失败；/, '')
      .trim()
}

function removeDiagnosticSegment(value: string, index: number, length: number): string {
  return cleanupDiagnosticMessage(`${value.slice(0, index)}${value.slice(index + length)}`)
}

function cleanupDiagnosticMessage(value: string): string {
  return value
    .replace(/\(\s*\)/g, '')
    .replace(/\s*[；;]\s*/g, '；')
    .replace(/；{2,}/g, '；')
    .replace(/^[；;\s,，。]+|[；;\s,，。]+$/g, '')
    .trim()
}
