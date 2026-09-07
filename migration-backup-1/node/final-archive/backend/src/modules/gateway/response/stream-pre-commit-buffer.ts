export interface StreamPreCommitBufferState {
  buffering: boolean
  bufferedBytes: number
  chunks: Buffer[]
}

export interface StreamPreCommitInspectionState {
  outputReceived: boolean
  terminalReceived: boolean
  failedReceived: boolean
  skipped: boolean
}

export interface StreamPreCommitResponseState {
  headersSent: boolean
  writableEnded: boolean
  destroyed: boolean
}

export const streamPreCommitBufferMaxBytes = 256 * 1024

type SseLineKind = 'empty' | 'comment' | 'field_candidate' | 'data' | 'other'

/**
 * Tracks only standard SSE framing. It deliberately does not interpret event
 * names, JSON payloads, provider codes, status codes, or error messages.
 */
export class StreamPreCommitSseEvidence {
  dataEventObserved = false
  dataPayloadStarted = false
  onlyNonSemanticFramingObserved = true

  private lineKind: SseLineKind = 'empty'
  private fieldCandidate = ''
  private dataValueCanSkipLeadingSpace = false
  private currentDataLineHasValue = false
  private currentEventHasData = false
  private carriageReturnPending = false

  push(chunk: Buffer | Uint8Array): void {
    for (const byte of chunk) {
      if (byte === 0x0a) {
        this.finishLine()
        this.carriageReturnPending = false
        continue
      }
      if (byte === 0x0d) {
        if (this.carriageReturnPending) this.finishLine()
        this.carriageReturnPending = true
        continue
      }
      if (this.carriageReturnPending) {
        this.finishLine()
        this.carriageReturnPending = false
      }
      this.pushLineByte(byte)
    }
  }

  finish(): void {
    if (this.carriageReturnPending || this.lineKind !== 'empty') {
      this.finishLine()
      this.carriageReturnPending = false
    }
    this.finishEvent()
  }

  private pushLineByte(byte: number): void {
    if (this.lineKind === 'empty') {
      if (byte === 0x3a) {
        this.lineKind = 'comment'
        return
      }
      this.onlyNonSemanticFramingObserved = false
      this.lineKind = 'field_candidate'
      this.fieldCandidate = String.fromCharCode(byte)
      return
    }
    if (this.lineKind === 'comment' || this.lineKind === 'other') return
    if (this.lineKind === 'data') {
      if (this.dataValueCanSkipLeadingSpace) {
        this.dataValueCanSkipLeadingSpace = false
        if (byte === 0x20) return
      }
      this.currentDataLineHasValue = true
      this.dataPayloadStarted = true
      return
    }
    if (byte === 0x3a) {
      if (this.fieldCandidate === 'data') {
        this.lineKind = 'data'
        this.dataValueCanSkipLeadingSpace = true
      } else {
        this.lineKind = 'other'
      }
      return
    }
    if (this.fieldCandidate.length >= 4) {
      this.lineKind = 'other'
      return
    }
    this.fieldCandidate += String.fromCharCode(byte)
  }

  private finishLine(): void {
    if (this.lineKind === 'empty') {
      this.finishEvent()
      return
    }
    if (this.lineKind === 'data' && this.currentDataLineHasValue) {
      this.currentEventHasData = true
    }
    this.lineKind = 'empty'
    this.fieldCandidate = ''
    this.dataValueCanSkipLeadingSpace = false
    this.currentDataLineHasValue = false
  }

  private finishEvent(): void {
    if (this.currentEventHasData) {
      this.dataEventObserved = true
      this.onlyNonSemanticFramingObserved = false
    } else if (!this.dataEventObserved) {
      // A completed event containing only comments, empty data fields, or
      // metadata fields has no client-visible SSE data and may be discarded.
      this.onlyNonSemanticFramingObserved = true
    }
    this.currentEventHasData = false
  }
}

export function createStreamPreCommitBufferState(enabled: boolean): StreamPreCommitBufferState {
  return {
    buffering: enabled,
    bufferedBytes: 0,
    chunks: []
  }
}

export function canKeepStreamPreCommitChunk(
  state: StreamPreCommitBufferState,
  input: {
    inspection: StreamPreCommitInspectionState
    chunk: Buffer
    totalResponseBytes: number
    response: StreamPreCommitResponseState
  }
): boolean {
  return state.buffering
    && input.totalResponseBytes === 0
    && !input.inspection.outputReceived
    && !input.inspection.terminalReceived
    && !input.inspection.failedReceived
    && !input.inspection.skipped
    && state.bufferedBytes + input.chunk.length <= streamPreCommitBufferMaxBytes
    && responseCanStillFailBeforeCommit(input.response)
}

export function wouldExceedStreamPreCommitBuffer(
  state: StreamPreCommitBufferState,
  chunk: Buffer
): boolean {
  return state.buffering
    && state.bufferedBytes + chunk.length > streamPreCommitBufferMaxBytes
}

export function appendStreamPreCommitChunk(state: StreamPreCommitBufferState, chunk: Buffer): void {
  state.chunks.push(chunk)
  state.bufferedBytes += chunk.length
}

export function clearStreamPreCommitChunks(state: StreamPreCommitBufferState): void {
  state.bufferedBytes = 0
  state.chunks = []
}

export function takeStreamPreCommitChunks(state: StreamPreCommitBufferState): Buffer[] {
  state.buffering = false
  state.bufferedBytes = 0
  return state.chunks.splice(0)
}

export function shouldFailBeforeStreamDownstreamCommit(
  state: StreamPreCommitBufferState,
  input: {
    totalResponseBytes: number
    response: StreamPreCommitResponseState
  }
): boolean {
  return state.buffering
    && input.totalResponseBytes === 0
    && responseCanStillFailBeforeCommit(input.response)
}

export function uncommittedStreamResponseBody(state: StreamPreCommitBufferState): Buffer | undefined {
  return state.chunks.length > 0 ? Buffer.concat(state.chunks) : undefined
}

function responseCanStillFailBeforeCommit(response: StreamPreCommitResponseState): boolean {
  return !response.writableEnded && !response.destroyed
}
