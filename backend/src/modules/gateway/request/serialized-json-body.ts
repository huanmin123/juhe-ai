type JsonObject = Record<string, unknown>

const parsedBodyBySerializedBuffer = new WeakMap<Buffer, Readonly<JsonObject>>()

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
