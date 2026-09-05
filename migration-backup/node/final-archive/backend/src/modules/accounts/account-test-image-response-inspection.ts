export const accountTestImageEnvelopeScanMaxChars = 256 * 1024

export interface AccountTestImageResponseInspection {
  successEvidence: boolean
  errorCode?: string
  errorMessage?: string
  scannedCharacters: number
  imagePayloadCharactersScanned: number
}

type ParseStatus = 'ok' | 'invalid' | 'incomplete'
type ValueRole = 'root' | 'data_array' | 'data_item' | 'error_object' | 'other'

interface StringToken {
  status: ParseStatus
  nextIndex: number
  startIndex: number
  endIndex: number
  decoded?: string
}

export function inspectAccountTestImageResponseEnvelope(
  bodyText: string,
  responseTruncated: boolean
): AccountTestImageResponseInspection {
  const truncationMarkerLength = responseTruncated && bodyText.endsWith('\n[truncated]')
    ? '\n[truncated]'.length
    : 0
  const availableEnd = bodyText.length - truncationMarkerLength
  const endIndex = Math.min(availableEnd, accountTestImageEnvelopeScanMaxChars)
  let index = 0
  let dataArraySeen = false
  let imageResultSeen = false
  let topLevelErrorSeen = false
  let errorCode: string | undefined
  let errorMessage: string | undefined
  let imagePayloadCharactersScanned = 0

  const skipWhitespace = () => {
    while (index < endIndex && isJsonWhitespace(bodyText.charCodeAt(index))) index += 1
  }

  const parseString = (captureLimit = 0, imageKind?: 'b64_json' | 'url'): StringToken => {
    const startIndex = index
    if (bodyText.charCodeAt(index) !== jsonQuote) {
      return { status: 'invalid', nextIndex: index, startIndex, endIndex: index }
    }
    index += 1
    const contentStart = index
    let decoded = ''
    let observedImageCharacters = 0
    let validImageValue = imageKind === undefined
    let urlPrefixValid = imageKind !== 'url'
    let base64NonPaddingCharacters = 0
    let base64PaddingCharacters = 0
    let base64PaddingStarted = false
    const urlPrefix = imageKind === 'url' ? '' : undefined
    let boundedUrlPrefix = urlPrefix

    while (index < endIndex) {
      const code = bodyText.charCodeAt(index)
      if (code === jsonQuote) {
        const contentEnd = index
        index += 1
        if (imageKind === 'b64_json') {
          validImageValue = observedImageCharacters >= 4
            && observedImageCharacters % 4 === 0
            && base64NonPaddingCharacters > 0
            && base64PaddingCharacters <= 2
        } else if (imageKind === 'url') {
          validImageValue = urlPrefixValid && hasCompleteImageUrlPrefix(boundedUrlPrefix ?? '', observedImageCharacters)
        }
        if (imageKind && validImageValue) imageResultSeen = true
        imagePayloadCharactersScanned += observedImageCharacters
        return { status: 'ok', nextIndex: index, startIndex: contentStart, endIndex: contentEnd, decoded }
      }
      if (code < 0x20) {
        return { status: 'invalid', nextIndex: index, startIndex: contentStart, endIndex: index }
      }
      if (code === jsonBackslash) {
        if (imageKind) {
          return { status: 'invalid', nextIndex: index, startIndex: contentStart, endIndex: index }
        }
        const escaped = decodeJsonEscape(bodyText, index, endIndex)
        if (!escaped) {
          return { status: index + 1 >= endIndex ? 'incomplete' : 'invalid', nextIndex: endIndex, startIndex: contentStart, endIndex, decoded }
        }
        if (decoded.length < captureLimit) decoded += escaped.value
        index = escaped.nextIndex
        continue
      }
      if (imageKind === 'b64_json') {
        if (!isBase64Character(code)) {
          return { status: 'invalid', nextIndex: index, startIndex: contentStart, endIndex: index }
        }
        if (code === 0x3d) {
          base64PaddingStarted = true
          base64PaddingCharacters += 1
          if (base64PaddingCharacters > 2) {
            return { status: 'invalid', nextIndex: index, startIndex: contentStart, endIndex: index }
          }
        } else {
          if (base64PaddingStarted) {
            return { status: 'invalid', nextIndex: index, startIndex: contentStart, endIndex: index }
          }
          base64NonPaddingCharacters += 1
        }
        observedImageCharacters += 1
      } else if (imageKind === 'url') {
        if (isJsonWhitespace(code)) {
          return { status: 'invalid', nextIndex: index, startIndex: contentStart, endIndex: index }
        }
        if (boundedUrlPrefix !== undefined && boundedUrlPrefix.length < 16) {
          boundedUrlPrefix += bodyText[index]
          urlPrefixValid = isPotentialImageUrlPrefix(boundedUrlPrefix)
        }
        observedImageCharacters += 1
      } else if (decoded.length < captureLimit) {
        decoded += bodyText[index]
      }
      index += 1
    }

    if (imageKind === 'b64_json') {
      validImageValue = observedImageCharacters >= 4 && base64NonPaddingCharacters > 0
    } else if (imageKind === 'url') {
      validImageValue = urlPrefixValid && hasCompleteImageUrlPrefix(boundedUrlPrefix ?? '', observedImageCharacters)
    }
    if (imageKind && validImageValue && responseTruncated && endIndex === availableEnd) imageResultSeen = true
    imagePayloadCharactersScanned += observedImageCharacters
    return { status: 'incomplete', nextIndex: endIndex, startIndex: contentStart, endIndex, decoded }
  }

  const parseValue = (role: ValueRole, depth: number): ParseStatus => {
    if (depth > 64) return 'invalid'
    skipWhitespace()
    if (index >= endIndex) return 'incomplete'
    const code = bodyText.charCodeAt(index)
    if (code === jsonObjectOpen) return parseObject(role, depth + 1)
    if (code === jsonArrayOpen) return parseArray(role, depth + 1)
    if (code === jsonQuote) return parseString().status
    return parsePrimitive()
  }

  const parseObject = (role: ValueRole, depth: number): ParseStatus => {
    index += 1
    skipWhitespace()
    if (index >= endIndex) return 'incomplete'
    if (bodyText.charCodeAt(index) === jsonObjectClose) {
      index += 1
      return 'ok'
    }
    while (index < endIndex) {
      const key = parseString()
      if (key.status !== 'ok') return key.status
      skipWhitespace()
      if (index >= endIndex) return 'incomplete'
      if (bodyText.charCodeAt(index) !== jsonColon) return 'invalid'
      index += 1
      skipWhitespace()
      if (index >= endIndex) return 'incomplete'

      const keyKind = jsonKeyKind(bodyText, key.startIndex, key.endIndex)
      let status: ParseStatus
      if (role === 'root' && keyKind === 'data') {
        if (bodyText.charCodeAt(index) !== jsonArrayOpen) return 'invalid'
        dataArraySeen = true
        status = parseArray('data_array', depth + 1)
      } else if (role === 'root' && keyKind === 'error') {
        topLevelErrorSeen = !startsWithJsonNull(bodyText, index, endIndex)
        status = parseValue('error_object', depth + 1)
      } else if (role === 'data_item' && (keyKind === 'b64_json' || keyKind === 'url')) {
        status = parseString(0, keyKind).status
      } else if (role === 'error_object' && (keyKind === 'code' || keyKind === 'type' || keyKind === 'message')) {
        if (bodyText.charCodeAt(index) === jsonQuote) {
          const token = parseString(keyKind === 'message' ? 240 : 96)
          status = token.status
          if (status === 'ok') {
            if (keyKind === 'message') errorMessage = token.decoded || undefined
            else if (!errorCode) errorCode = token.decoded || undefined
          }
        } else {
          status = parseValue('other', depth + 1)
        }
      } else {
        status = parseValue('other', depth + 1)
      }
      if (status !== 'ok') return status

      skipWhitespace()
      if (index >= endIndex) return 'incomplete'
      const separator = bodyText.charCodeAt(index)
      if (separator === jsonObjectClose) {
        index += 1
        return 'ok'
      }
      if (separator !== jsonComma) return 'invalid'
      index += 1
      skipWhitespace()
      if (index >= endIndex) return 'incomplete'
    }
    return 'incomplete'
  }

  const parseArray = (role: ValueRole, depth: number): ParseStatus => {
    index += 1
    skipWhitespace()
    if (index >= endIndex) return 'incomplete'
    if (bodyText.charCodeAt(index) === jsonArrayClose) {
      index += 1
      return 'ok'
    }
    while (index < endIndex) {
      const status = parseValue(role === 'data_array' ? 'data_item' : 'other', depth + 1)
      if (status !== 'ok') return status
      skipWhitespace()
      if (index >= endIndex) return 'incomplete'
      const separator = bodyText.charCodeAt(index)
      if (separator === jsonArrayClose) {
        index += 1
        return 'ok'
      }
      if (separator !== jsonComma) return 'invalid'
      index += 1
      skipWhitespace()
      if (index >= endIndex) return 'incomplete'
    }
    return 'incomplete'
  }

  const parsePrimitive = (): ParseStatus => {
    const start = index
    while (index < endIndex && !isJsonValueDelimiter(bodyText.charCodeAt(index))) index += 1
    if (index === endIndex && responseTruncated) return 'incomplete'
    return isValidJsonPrimitive(bodyText, start, index) ? 'ok' : 'invalid'
  }

  skipWhitespace()
  const status = index < endIndex && bodyText.charCodeAt(index) === jsonObjectOpen
    ? parseObject('root', 0)
    : index >= endIndex ? 'incomplete' : 'invalid'
  skipWhitespace()
  const completeDocument = status === 'ok' && index === endIndex
  const acceptedTruncatedPrefix = responseTruncated
    && status === 'incomplete'
    && endIndex === availableEnd

  return {
    successEvidence: dataArraySeen
      && imageResultSeen
      && !topLevelErrorSeen
      && (completeDocument || acceptedTruncatedPrefix),
    errorCode,
    errorMessage,
    scannedCharacters: index,
    imagePayloadCharactersScanned
  }
}

function jsonKeyKind(
  text: string,
  startIndex: number,
  endIndex: number
): 'data' | 'error' | 'b64_json' | 'url' | 'code' | 'type' | 'message' | 'other' {
  for (const key of imageResponseKeys) {
    if (endIndex - startIndex !== key.length) continue
    let matches = true
    for (let offset = 0; offset < key.length; offset += 1) {
      if (text.charCodeAt(startIndex + offset) !== key.charCodeAt(offset)) {
        matches = false
        break
      }
    }
    if (matches) return key
  }
  return 'other'
}

function decodeJsonEscape(text: string, index: number, endIndex: number): { value: string; nextIndex: number } | undefined {
  if (index + 1 >= endIndex) return undefined
  const escaped = text[index + 1]
  const simple = simpleJsonEscapes[escaped]
  if (simple !== undefined) return { value: simple, nextIndex: index + 2 }
  if (escaped !== 'u' || index + 5 >= endIndex) return undefined
  const hex = text.substring(index + 2, index + 6)
  if (!/^[0-9a-fA-F]{4}$/.test(hex)) return undefined
  return { value: String.fromCharCode(Number.parseInt(hex, 16)), nextIndex: index + 6 }
}

function startsWithJsonNull(text: string, index: number, endIndex: number): boolean {
  return index + 4 <= endIndex
    && text.charCodeAt(index) === 0x6e
    && text.charCodeAt(index + 1) === 0x75
    && text.charCodeAt(index + 2) === 0x6c
    && text.charCodeAt(index + 3) === 0x6c
}

function isValidJsonPrimitive(text: string, startIndex: number, endIndex: number): boolean {
  const length = endIndex - startIndex
  if (length === 4 && (matchesAscii(text, startIndex, 'true') || matchesAscii(text, startIndex, 'null'))) return true
  if (length === 5 && matchesAscii(text, startIndex, 'false')) return true
  if (length <= 0) return false
  let index = startIndex
  if (text.charCodeAt(index) === jsonMinus) index += 1
  if (index >= endIndex) return false
  if (text.charCodeAt(index) === jsonZero) {
    index += 1
  } else if (isOneToNine(text.charCodeAt(index))) {
    index += 1
    while (index < endIndex && isDigit(text.charCodeAt(index))) index += 1
  } else {
    return false
  }
  if (text.charCodeAt(index) === jsonDot) {
    index += 1
    const fractionStart = index
    while (index < endIndex && isDigit(text.charCodeAt(index))) index += 1
    if (index === fractionStart) return false
  }
  const exponent = text.charCodeAt(index)
  if (exponent === 0x65 || exponent === 0x45) {
    index += 1
    const sign = text.charCodeAt(index)
    if (sign === jsonPlus || sign === jsonMinus) index += 1
    const exponentStart = index
    while (index < endIndex && isDigit(text.charCodeAt(index))) index += 1
    if (index === exponentStart) return false
  }
  return index === endIndex
}

function matchesAscii(text: string, startIndex: number, expected: string): boolean {
  for (let index = 0; index < expected.length; index += 1) {
    if (text.charCodeAt(startIndex + index) !== expected.charCodeAt(index)) return false
  }
  return true
}

function isPotentialImageUrlPrefix(value: string): boolean {
  return 'https://'.startsWith(value)
    || 'http://'.startsWith(value)
    || 'data:image/'.startsWith(value)
    || value.startsWith('https://')
    || value.startsWith('http://')
    || value.startsWith('data:image/')
}

function hasCompleteImageUrlPrefix(value: string, observedCharacters: number): boolean {
  return (value.startsWith('https://') && observedCharacters > 'https://'.length)
    || (value.startsWith('http://') && observedCharacters > 'http://'.length)
    || (value.startsWith('data:image/') && observedCharacters > 'data:image/'.length)
}

function isBase64Character(code: number): boolean {
  return (code >= 0x41 && code <= 0x5a)
    || (code >= 0x61 && code <= 0x7a)
    || (code >= 0x30 && code <= 0x39)
    || code === 0x2b
    || code === 0x2f
    || code === 0x3d
    || code === 0x2d
    || code === 0x5f
}

function isJsonWhitespace(code: number): boolean {
  return code === 0x20 || code === 0x09 || code === 0x0a || code === 0x0d
}

function isJsonValueDelimiter(code: number): boolean {
  return isJsonWhitespace(code) || code === jsonComma || code === jsonObjectClose || code === jsonArrayClose
}

function isDigit(code: number): boolean {
  return code >= 0x30 && code <= 0x39
}

function isOneToNine(code: number): boolean {
  return code >= 0x31 && code <= 0x39
}

const imageResponseKeys = ['data', 'error', 'b64_json', 'url', 'code', 'type', 'message'] as const
const simpleJsonEscapes: Record<string, string> = {
  '"': '"',
  '\\': '\\',
  '/': '/',
  b: '\b',
  f: '\f',
  n: '\n',
  r: '\r',
  t: '\t'
}
const jsonQuote = 0x22
const jsonBackslash = 0x5c
const jsonObjectOpen = 0x7b
const jsonObjectClose = 0x7d
const jsonArrayOpen = 0x5b
const jsonArrayClose = 0x5d
const jsonColon = 0x3a
const jsonComma = 0x2c
const jsonMinus = 0x2d
const jsonPlus = 0x2b
const jsonDot = 0x2e
const jsonZero = 0x30
