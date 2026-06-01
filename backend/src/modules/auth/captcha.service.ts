import { randomInt, randomUUID } from 'node:crypto'
import { deflateSync } from 'node:zlib'

export interface CaptchaChallengeSummary {
  captchaId: string
  image: string
  expiresAt: string
}

export interface CaptchaIssueGuardResult {
  blocked: boolean
  message?: string
  retryAfterSeconds?: number
}

interface CaptchaChallengeRecord {
  answer: string
  expiresAt: number
}

interface CaptchaIssueRecord {
  timestamps: number[]
}

const captchaTtlMs = 5 * 60 * 1000
const maxCaptchaChallenges = 1000
const captchaCleanupIntervalMs = 30 * 1000
const captchaCleanupBatchSize = 64
const captchaIssueWindowMs = 60 * 1000
const captchaIssueThreshold = 60
const maxCaptchaIssueKeys = 2000
const captchaChars = '23456789ABCDEFGHJKLMNPQRSTUVWXYZ'
const captchaChallenges = new Map<string, CaptchaChallengeRecord>()
const captchaIssueRecords = new Map<string, CaptchaIssueRecord>()
let nextCaptchaCleanupAt = 0
let nextCaptchaIssueCleanupAt = 0

export function createCaptchaChallenge(): CaptchaChallengeSummary {
  const now = Date.now()
  runCaptchaMaintenance(now)
  pruneCaptchaChallenges()

  const answer = createCaptchaAnswer()
  const captchaId = randomUUID()
  const expiresAt = now + captchaTtlMs
  captchaChallenges.set(captchaId, {
    answer,
    expiresAt
  })

  return {
    captchaId,
    image: renderCaptchaImage(answer),
    expiresAt: new Date(expiresAt).toISOString()
  }
}

export function verifyCaptchaChallenge(captchaId: string, captchaCode: string): boolean {
  const now = Date.now()
  runCaptchaMaintenance(now)
  const challenge = captchaChallenges.get(captchaId)
  if (!challenge) return false

  captchaChallenges.delete(captchaId)
  if (challenge.expiresAt < now) return false

  return normalizeCaptchaCode(captchaCode) === challenge.answer
}

export function captchaAnswerForTest(captchaId: string): string | undefined {
  return captchaChallenges.get(captchaId)?.answer
}

export function consumeCaptchaIssueAllowance(clientIp: string): CaptchaIssueGuardResult {
  const now = Date.now()
  runCaptchaIssueMaintenance(now)
  pruneCaptchaIssueRecords(clientIp)

  const record = captchaIssueRecords.get(clientIp) ?? { timestamps: [] }
  record.timestamps = trimRecentCaptchaIssueTimestamps(record.timestamps, now)
  if (record.timestamps.length >= captchaIssueThreshold) {
    captchaIssueRecords.delete(clientIp)
    captchaIssueRecords.set(clientIp, record)
    const retryAfterSeconds = Math.max(1, Math.ceil((record.timestamps[0] + captchaIssueWindowMs - now) / 1000))
    return {
      blocked: true,
      message: '验证码请求过于频繁，请稍后再试',
      retryAfterSeconds
    }
  }

  record.timestamps.push(now)
  captchaIssueRecords.delete(clientIp)
  captchaIssueRecords.set(clientIp, record)
  return { blocked: false }
}

function createCaptchaAnswer(): string {
  let answer = ''
  for (let index = 0; index < 5; index += 1) {
    answer += captchaChars[randomInt(0, captchaChars.length)]
  }
  return answer
}

function normalizeCaptchaCode(value: string): string {
  return value.trim().replace(/\s+/g, '').toUpperCase()
}

function runCaptchaMaintenance(now: number): void {
  if (now < nextCaptchaCleanupAt) return
  nextCaptchaCleanupAt = now + captchaCleanupIntervalMs
  cleanupExpiredCaptchaChallenges(now)
}

function cleanupExpiredCaptchaChallenges(now: number): void {
  let inspected = 0
  for (const [captchaId, challenge] of captchaChallenges) {
    if (inspected >= captchaCleanupBatchSize) break
    if (challenge.expiresAt >= now) break
    captchaChallenges.delete(captchaId)
    inspected += 1
  }
}

function pruneCaptchaChallenges(): void {
  if (captchaChallenges.size < maxCaptchaChallenges) return
  const overflow = captchaChallenges.size - maxCaptchaChallenges + 1
  let removed = 0
  for (const captchaId of captchaChallenges.keys()) {
    captchaChallenges.delete(captchaId)
    removed += 1
    if (removed >= overflow) break
  }
}

function runCaptchaIssueMaintenance(now: number): void {
  if (now < nextCaptchaIssueCleanupAt) return
  nextCaptchaIssueCleanupAt = now + captchaCleanupIntervalMs
  cleanupCaptchaIssueRecords(now)
}

function cleanupCaptchaIssueRecords(now: number): void {
  let inspected = 0
  while (inspected < captchaCleanupBatchSize) {
    const nextEntry = captchaIssueRecords.entries().next()
    if (nextEntry.done) break

    const [clientIp, record] = nextEntry.value
    captchaIssueRecords.delete(clientIp)
    record.timestamps = trimRecentCaptchaIssueTimestamps(record.timestamps, now)
    if (record.timestamps.length > 0) {
      captchaIssueRecords.set(clientIp, record)
    }
    inspected += 1
  }
}

function pruneCaptchaIssueRecords(protectedKey: string): void {
  if (captchaIssueRecords.size < maxCaptchaIssueKeys) return
  const overflow = captchaIssueRecords.size - maxCaptchaIssueKeys + 1
  let removed = 0
  for (const clientIp of captchaIssueRecords.keys()) {
    if (clientIp === protectedKey) continue
    captchaIssueRecords.delete(clientIp)
    removed += 1
    if (removed >= overflow) break
  }
}

function trimRecentCaptchaIssueTimestamps(timestamps: number[], now: number): number[] {
  const earliestAllowedAt = now - captchaIssueWindowMs
  const recentTimestamps: number[] = []
  for (let index = timestamps.length - 1; index >= 0 && recentTimestamps.length < captchaIssueThreshold; index -= 1) {
    const timestamp = timestamps[index]
    if (timestamp < earliestAllowedAt) break
    recentTimestamps.push(timestamp)
  }
  return recentTimestamps.reverse()
}

function renderCaptchaImage(answer: string): string {
  const width = 144
  const height = 46
  const pixels = createPixelBuffer(width, height)

  for (let index = 0; index < 140; index += 1) {
    const shade = randomInt(190, 232)
    setPixel(pixels, width, randomInt(0, width), randomInt(0, height), shade, randomInt(210, 242), 255, randomInt(90, 180))
  }

  for (let index = 0; index < 6; index += 1) {
    drawLine(
      pixels,
      width,
      height,
      randomInt(0, width),
      randomInt(0, height),
      randomInt(0, width),
      randomInt(0, height),
      randomInt(70, 120),
      randomInt(120, 190),
      randomInt(180, 235),
      randomInt(120, 190)
    )
  }

  for (const [index, char] of [...answer].entries()) {
    drawGlyph(
      pixels,
      width,
      height,
      char,
      13 + index * 24 + randomInt(-2, 3),
      8 + randomInt(-2, 3),
      4,
      randomInt(15, 45),
      randomInt(35, 75),
      randomInt(65, 105)
    )
  }

  return `data:image/png;base64,${encodePng(width, height, pixels).toString('base64')}`
}

const glyphs: Record<string, string[]> = {
  2: ['11110', '00001', '00001', '11110', '10000', '10000', '11111'],
  3: ['11110', '00001', '00001', '01110', '00001', '00001', '11110'],
  4: ['10001', '10001', '10001', '11111', '00001', '00001', '00001'],
  5: ['11111', '10000', '10000', '11110', '00001', '00001', '11110'],
  6: ['01111', '10000', '10000', '11110', '10001', '10001', '01110'],
  7: ['11111', '00001', '00010', '00100', '01000', '01000', '01000'],
  8: ['01110', '10001', '10001', '01110', '10001', '10001', '01110'],
  9: ['01110', '10001', '10001', '01111', '00001', '00001', '11110'],
  A: ['01110', '10001', '10001', '11111', '10001', '10001', '10001'],
  B: ['11110', '10001', '10001', '11110', '10001', '10001', '11110'],
  C: ['01111', '10000', '10000', '10000', '10000', '10000', '01111'],
  D: ['11110', '10001', '10001', '10001', '10001', '10001', '11110'],
  E: ['11111', '10000', '10000', '11110', '10000', '10000', '11111'],
  F: ['11111', '10000', '10000', '11110', '10000', '10000', '10000'],
  G: ['01111', '10000', '10000', '10011', '10001', '10001', '01111'],
  H: ['10001', '10001', '10001', '11111', '10001', '10001', '10001'],
  J: ['00111', '00010', '00010', '00010', '10010', '10010', '01100'],
  K: ['10001', '10010', '10100', '11000', '10100', '10010', '10001'],
  L: ['10000', '10000', '10000', '10000', '10000', '10000', '11111'],
  M: ['10001', '11011', '10101', '10101', '10001', '10001', '10001'],
  N: ['10001', '11001', '10101', '10011', '10001', '10001', '10001'],
  P: ['11110', '10001', '10001', '11110', '10000', '10000', '10000'],
  Q: ['01110', '10001', '10001', '10001', '10101', '10010', '01101'],
  R: ['11110', '10001', '10001', '11110', '10100', '10010', '10001'],
  S: ['01111', '10000', '10000', '01110', '00001', '00001', '11110'],
  T: ['11111', '00100', '00100', '00100', '00100', '00100', '00100'],
  U: ['10001', '10001', '10001', '10001', '10001', '10001', '01110'],
  V: ['10001', '10001', '10001', '10001', '10001', '01010', '00100'],
  W: ['10001', '10001', '10001', '10101', '10101', '10101', '01010'],
  X: ['10001', '10001', '01010', '00100', '01010', '10001', '10001'],
  Y: ['10001', '10001', '01010', '00100', '00100', '00100', '00100'],
  Z: ['11111', '00001', '00010', '00100', '01000', '10000', '11111']
}

function createPixelBuffer(width: number, height: number): Buffer {
  const pixels = Buffer.alloc(width * height * 4)
  for (let y = 0; y < height; y += 1) {
    for (let x = 0; x < width; x += 1) {
      const blue = 242 + Math.trunc((x / Math.max(1, width - 1)) * 10)
      const green = 246 + Math.trunc((y / Math.max(1, height - 1)) * 6)
      setPixel(pixels, width, x, y, 239, green, blue, 255)
    }
  }
  return pixels
}

function drawGlyph(
  pixels: Buffer,
  width: number,
  height: number,
  char: string,
  startX: number,
  startY: number,
  scale: number,
  red: number,
  green: number,
  blue: number
): void {
  const glyph = glyphs[char]
  if (!glyph) return
  for (const [rowIndex, row] of glyph.entries()) {
    for (const [columnIndex, value] of [...row].entries()) {
      if (value !== '1') continue
      const blockX = startX + columnIndex * scale + randomInt(-1, 2)
      const blockY = startY + rowIndex * scale + Math.trunc(Math.sin((columnIndex + rowIndex) / 2) * 1.5)
      drawRect(pixels, width, height, blockX, blockY, scale - 1, scale, red, green, blue, 235)
    }
  }
}

function drawRect(
  pixels: Buffer,
  width: number,
  height: number,
  startX: number,
  startY: number,
  rectWidth: number,
  rectHeight: number,
  red: number,
  green: number,
  blue: number,
  alpha: number
): void {
  for (let y = 0; y < rectHeight; y += 1) {
    for (let x = 0; x < rectWidth; x += 1) {
      setPixel(pixels, width, startX + x, startY + y, red, green, blue, alpha, height)
    }
  }
}

function drawLine(
  pixels: Buffer,
  width: number,
  height: number,
  x1: number,
  y1: number,
  x2: number,
  y2: number,
  red: number,
  green: number,
  blue: number,
  alpha: number
): void {
  let currentX = x1
  let currentY = y1
  const dx = Math.abs(x2 - x1)
  const dy = -Math.abs(y2 - y1)
  const stepX = x1 < x2 ? 1 : -1
  const stepY = y1 < y2 ? 1 : -1
  let error = dx + dy

  while (true) {
    drawRect(pixels, width, height, currentX, currentY, 2, 2, red, green, blue, alpha)
    if (currentX === x2 && currentY === y2) break
    const doubledError = 2 * error
    if (doubledError >= dy) {
      error += dy
      currentX += stepX
    }
    if (doubledError <= dx) {
      error += dx
      currentY += stepY
    }
  }
}

function setPixel(
  pixels: Buffer,
  width: number,
  x: number,
  y: number,
  red: number,
  green: number,
  blue: number,
  alpha: number,
  height = Math.trunc(pixels.length / 4 / width)
): void {
  if (x < 0 || y < 0 || x >= width || y >= height) return
  const offset = (y * width + x) * 4
  const sourceAlpha = alpha / 255
  const targetAlpha = 1 - sourceAlpha
  pixels[offset] = Math.round(red * sourceAlpha + pixels[offset] * targetAlpha)
  pixels[offset + 1] = Math.round(green * sourceAlpha + pixels[offset + 1] * targetAlpha)
  pixels[offset + 2] = Math.round(blue * sourceAlpha + pixels[offset + 2] * targetAlpha)
  pixels[offset + 3] = 255
}

function encodePng(width: number, height: number, pixels: Buffer): Buffer {
  const raw = Buffer.alloc(height * (1 + width * 4))
  for (let y = 0; y < height; y += 1) {
    const sourceStart = y * width * 4
    const targetStart = y * (1 + width * 4)
    raw[targetStart] = 0
    pixels.copy(raw, targetStart + 1, sourceStart, sourceStart + width * 4)
  }

  const header = Buffer.alloc(13)
  header.writeUInt32BE(width, 0)
  header.writeUInt32BE(height, 4)
  header[8] = 8
  header[9] = 6
  header[10] = 0
  header[11] = 0
  header[12] = 0

  return Buffer.concat([
    Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]),
    pngChunk('IHDR', header),
    pngChunk('IDAT', deflateSync(raw)),
    pngChunk('IEND', Buffer.alloc(0))
  ])
}

function pngChunk(type: string, data: Buffer): Buffer {
  const typeBuffer = Buffer.from(type, 'ascii')
  const length = Buffer.alloc(4)
  length.writeUInt32BE(data.length, 0)
  const crc = Buffer.alloc(4)
  crc.writeUInt32BE(crc32(Buffer.concat([typeBuffer, data])), 0)
  return Buffer.concat([length, typeBuffer, data, crc])
}

let crc32Table: number[] | undefined

function crc32(input: Buffer): number {
  const table = crc32Table ?? buildCrc32Table()
  let crc = 0xffffffff
  for (const value of input) {
    crc = table[(crc ^ value) & 0xff] ^ (crc >>> 8)
  }
  return (crc ^ 0xffffffff) >>> 0
}

function buildCrc32Table(): number[] {
  const table: number[] = []
  for (let index = 0; index < 256; index += 1) {
    let crc = index
    for (let bit = 0; bit < 8; bit += 1) {
      crc = (crc & 1) === 1 ? 0xedb88320 ^ (crc >>> 1) : crc >>> 1
    }
    table[index] = crc >>> 0
  }
  crc32Table = table
  return table
}
