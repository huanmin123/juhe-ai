export class BoundedBufferCollector {
  private readonly chunks: Buffer[] = []
  private collectedBytes = 0
  private didTruncate = false

  constructor(private readonly maxBytes: number) {}

  append(value: Buffer | string | Uint8Array): void {
    const buffer = Buffer.isBuffer(value) ? value : Buffer.from(value)
    if (buffer.byteLength <= 0) {
      return
    }
    const remainingBytes = Math.max(0, this.maxBytes - this.collectedBytes)
    if (remainingBytes > 0) {
      const chunk = buffer.byteLength > remainingBytes ? buffer.subarray(0, remainingBytes) : buffer
      this.chunks.push(chunk)
      this.collectedBytes += chunk.byteLength
    }
    if (buffer.byteLength > remainingBytes) {
      this.didTruncate = true
    }
  }

  get byteLength(): number {
    return this.collectedBytes
  }

  get truncated(): boolean {
    return this.didTruncate
  }

  text(options: { includeTruncationMarker?: boolean } = {}): string {
    const text = Buffer.concat(this.chunks, this.collectedBytes).toString('utf8')
    return options.includeTruncationMarker && this.didTruncate
      ? `${text}${truncationMarker(this.maxBytes)}`
      : text
  }
}

export function truncationMarker(maxBytes: number): string {
  return `\n\n[响应体过大，已截断，仅保留前 ${formatByteSize(maxBytes)}]`
}

function formatByteSize(bytes: number): string {
  if (bytes >= 1024 * 1024) {
    return `${Number((bytes / 1024 / 1024).toFixed(2))} MiB`
  }
  if (bytes >= 1024) {
    return `${Number((bytes / 1024).toFixed(1))} KiB`
  }
  return `${bytes} B`
}
