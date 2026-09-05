type JsonObject = Record<string, unknown>

const parsedBodyBySerializedBuffer = new WeakMap<Buffer, Readonly<JsonObject>>()
const codexHistorySanitizedBuffers = new WeakSet<Buffer>()

export function serializeGatewayJsonObject(body: Readonly<JsonObject>): Buffer {
  const serialized = Buffer.from(JSON.stringify(body), 'utf8')
  bindGatewaySerializedJsonObject(serialized, body)
  return serialized
}

export function bindGatewaySerializedJsonObject(
  serialized: Buffer,
  body: Readonly<JsonObject>
): void {
  parsedBodyBySerializedBuffer.set(serialized, body)
}

export function gatewaySerializedJsonObject(body: Buffer | string): Readonly<JsonObject> | undefined {
  return Buffer.isBuffer(body) ? parsedBodyBySerializedBuffer.get(body) : undefined
}

export function markGatewayCodexHistorySanitized(serialized: Buffer): Buffer {
  codexHistorySanitizedBuffers.add(serialized)
  return serialized
}

export function isGatewayCodexHistorySanitized(body: Buffer | string): boolean {
  return Buffer.isBuffer(body) && codexHistorySanitizedBuffers.has(body)
}
