export class GatewayDownstreamCommitState {
  transportCommitted = false
  semanticCommitted = false
  successfulProtocolTerminalReceived = false
  downstreamBytesWritten = 0

  markTransportCommitted(bytesWritten = 0): void {
    this.transportCommitted = true
    this.downstreamBytesWritten += normalizedBytes(bytesWritten)
  }

  markSemanticCommitted(bytesWritten = 0): void {
    this.transportCommitted = true
    this.semanticCommitted = true
    this.downstreamBytesWritten += normalizedBytes(bytesWritten)
  }

  markSuccessfulProtocolTerminalReceived(): void {
    this.successfulProtocolTerminalReceived = true
  }

  canRetryUpstream(): boolean {
    return !this.semanticCommitted
  }
}

function normalizedBytes(value: number): number {
  return Number.isFinite(value) ? Math.max(0, Math.trunc(value)) : 0
}
