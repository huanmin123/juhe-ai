const jsonStringCaptureMaxBytes = 256

/**
 * Bounded incremental scanner for the root `status` field of a Responses JSON
 * document. The response may be too large to retain in memory and a provider
 * is not required to put `status` before a large `output` field, so this must
 * run on every transferred chunk instead of inspecting a retained prefix.
 */
export class ResponsesRootStatusTracker {
  private depth = 0
  private rootStarted = false
  private completed = false
  private failed = false
  private rootState: 'key_or_end' | 'colon' | 'value' | 'after_value' = 'key_or_end'
  private rootKeyIsStatus = false
  private inString = false
  private stringEscaped = false
  private stringContext: 'key' | 'status_value' | undefined
  private stringRaw = ''
  private stringCaptureTruncated = false

  push(chunk: Uint8Array): void {
    if (this.completed || this.failed) return
    for (const byte of chunk) {
      this.consumeByte(byte)
      if (this.completed || this.failed) return
    }
  }

  hasFailedStatus(): boolean {
    return this.failed
  }

  private consumeByte(byte: number): void {
    if (this.inString) {
      this.consumeStringByte(byte)
      return
    }
    if (!this.rootStarted) {
      if (isJsonWhitespace(byte)) return
      if (byte !== 0x7b) {
        this.completed = true
        return
      }
      this.rootStarted = true
      this.depth = 1
      this.rootState = 'key_or_end'
      return
    }
    if (byte === 0x22) {
      this.startString()
      return
    }
    if (isJsonWhitespace(byte)) return
    if (this.depth === 1) {
      if (byte === 0x3a && this.rootState === 'colon') {
        this.rootState = 'value'
        return
      }
      if (byte === 0x2c) {
        this.rootState = 'key_or_end'
        this.rootKeyIsStatus = false
        return
      }
      if (this.rootState === 'value') {
        this.rootState = 'after_value'
        this.rootKeyIsStatus = false
      }
    }
    if (byte === 0x7b || byte === 0x5b) {
      this.depth += 1
      return
    }
    if (byte === 0x7d || byte === 0x5d) {
      this.depth = Math.max(0, this.depth - 1)
      if (this.depth === 0) this.completed = true
    }
  }

  private startString(): void {
    this.inString = true
    this.stringEscaped = false
    this.stringRaw = ''
    this.stringCaptureTruncated = false
    this.stringContext = this.depth === 1
      ? this.rootState === 'key_or_end'
        ? 'key'
        : this.rootState === 'value' && this.rootKeyIsStatus
          ? 'status_value'
          : undefined
      : undefined
  }

  private consumeStringByte(byte: number): void {
    if (this.stringEscaped) {
      this.appendStringByte(byte)
      this.stringEscaped = false
      return
    }
    if (byte === 0x5c) {
      this.appendStringByte(byte)
      this.stringEscaped = true
      return
    }
    if (byte !== 0x22) {
      this.appendStringByte(byte)
      return
    }
    this.inString = false
    const value = this.stringCaptureTruncated ? undefined : decodeJsonString(this.stringRaw)
    if (this.stringContext === 'key') {
      this.rootKeyIsStatus = value === 'status'
      this.rootState = 'colon'
    } else if (this.stringContext === 'status_value') {
      this.rootKeyIsStatus = false
      this.rootState = 'after_value'
      this.failed = value === 'failed'
    } else if (this.depth === 1 && this.rootState === 'value') {
      this.rootKeyIsStatus = false
      this.rootState = 'after_value'
    }
    this.stringContext = undefined
    this.stringRaw = ''
  }

  private appendStringByte(byte: number): void {
    if (!this.stringContext || this.stringCaptureTruncated) return
    if (this.stringRaw.length >= jsonStringCaptureMaxBytes) {
      this.stringCaptureTruncated = true
      return
    }
    this.stringRaw += String.fromCharCode(byte)
  }
}

export function responsesFailureStatusFromCapturedJson(responseBodyText: string | undefined): boolean {
  if (!responseBodyText) return false
  const tracker = new ResponsesRootStatusTracker()
  tracker.push(Buffer.from(responseBodyText, 'utf8'))
  return tracker.hasFailedStatus()
}

function decodeJsonString(raw: string): string | undefined {
  try {
    const parsed = JSON.parse(`"${raw}"`) as unknown
    return typeof parsed === 'string' ? parsed : undefined
  } catch {
    return undefined
  }
}

function isJsonWhitespace(byte: number): boolean {
  return byte === 0x09 || byte === 0x0a || byte === 0x0d || byte === 0x20
}
