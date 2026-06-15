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

const streamPreCommitBufferMaxBytes = 256 * 1024

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

export function appendStreamPreCommitChunk(state: StreamPreCommitBufferState, chunk: Buffer): void {
  state.chunks.push(chunk)
  state.bufferedBytes += chunk.length
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
  return !response.headersSent && !response.writableEnded && !response.destroyed
}
