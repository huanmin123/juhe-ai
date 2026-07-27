type JsonObject = Record<string, unknown>

const parsedBodyBySerializedBuffer = new WeakMap<Buffer, Readonly<JsonObject>>()

export function serializeGatewayJsonObject(body: Readonly<JsonObject>): Buffer {
  const serialized = Buffer.from(JSON.stringify(body), 'utf8')
  parsedBodyBySerializedBuffer.set(serialized, body)
  return serialized
}

export function gatewaySerializedJsonObject(body: Buffer | string): Readonly<JsonObject> | undefined {
  return Buffer.isBuffer(body) ? parsedBodyBySerializedBuffer.get(body) : undefined
}
