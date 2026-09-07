import Busboy from 'busboy'

const maxModelBytes = 200
const maxResponseFormatBytes = 64

export async function extractGatewayMultipartImageModel(input: {
  rawBody: Buffer
  contentType: string
  path: string
}): Promise<string | undefined> {
  const path = input.path.split('?')[0]?.toLowerCase() ?? ''
  if (!isImageEndpoint(path) || !input.contentType.toLowerCase().startsWith('multipart/form-data')) return undefined
  return new Promise((resolve) => {
    let model: string | undefined
    let modelFields = 0
    let settled = false
    const finish = (value: string | undefined) => {
      if (settled) return
      settled = true
      resolve(value)
    }
    try {
      const parser = Busboy({
        headers: { 'content-type': input.contentType },
        limits: { fields: 16, fieldSize: maxModelBytes + 1, files: 5, parts: 24 }
      })
      parser.on('file', (_name, stream) => stream.resume())
      parser.on('field', (name, value, info) => {
        if (name !== 'model') return
        modelFields += 1
        const normalized = value.trim()
        if (modelFields === 1 && !info.valueTruncated && Buffer.byteLength(normalized, 'utf8') <= maxModelBytes && isSafeModelId(normalized)) {
          model = normalized
        } else {
          model = undefined
        }
      })
      parser.once('error', () => finish(undefined))
      parser.once('finish', () => finish(modelFields === 1 ? model : undefined))
      parser.end(input.rawBody)
    } catch {
      finish(undefined)
    }
  })
}

export async function extractGatewayMultipartAudioResponseFormat(input: {
  rawBody: Buffer
  contentType: string
  path: string
}): Promise<string | undefined> {
  const path = input.path.split('?')[0]?.toLowerCase() ?? ''
  if (!isAudioTranscriptionEndpoint(path) || !input.contentType.toLowerCase().startsWith('multipart/form-data')) return undefined
  return new Promise((resolve) => {
    let responseFormat: string | undefined
    let responseFormatFields = 0
    let settled = false
    const finish = (value: string | undefined) => {
      if (settled) return
      settled = true
      resolve(value)
    }
    try {
      const parser = Busboy({
        headers: { 'content-type': input.contentType },
        limits: { fields: 16, fieldSize: maxResponseFormatBytes + 1, files: 5, parts: 24 }
      })
      parser.on('file', (_name, stream) => stream.resume())
      parser.on('field', (name, value, info) => {
        if (name !== 'response_format') return
        responseFormatFields += 1
        const normalized = value.trim().toLowerCase()
        if (responseFormatFields === 1 && !info.valueTruncated && Buffer.byteLength(normalized, 'utf8') <= maxResponseFormatBytes) {
          responseFormat = normalized || undefined
        } else {
          responseFormat = undefined
        }
      })
      parser.once('error', () => finish(undefined))
      parser.once('finish', () => finish(responseFormatFields === 1 ? responseFormat : undefined))
      parser.end(input.rawBody)
    } catch {
      finish(undefined)
    }
  })
}

function isImageEndpoint(path: string): boolean {
  return path === '/images' || path.startsWith('/images/') || path === '/v1/images' || path.startsWith('/v1/images/')
}

function isAudioTranscriptionEndpoint(path: string): boolean {
  return path === '/audio/transcriptions'
    || path === '/audio/translations'
    || path === '/v1/audio/transcriptions'
    || path === '/v1/audio/translations'
}

function isSafeModelId(value: string): boolean {
  return Boolean(value && !/[\u0000-\u001f\u007f]/u.test(value))
}
