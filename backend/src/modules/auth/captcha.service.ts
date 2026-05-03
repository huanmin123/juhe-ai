import { randomInt, randomUUID } from 'node:crypto'

export interface CaptchaChallengeSummary {
  captchaId: string
  image: string
  expiresAt: string
}

interface CaptchaChallengeRecord {
  answer: string
  createdAt: number
  expiresAt: number
}

const captchaTtlMs = 5 * 60 * 1000
const maxCaptchaChallenges = 1000
const captchaChars = '23456789ABCDEFGHJKLMNPQRSTUVWXYZ'
const captchaChallenges = new Map<string, CaptchaChallengeRecord>()

export function createCaptchaChallenge(): CaptchaChallengeSummary {
  cleanupCaptchaChallenges()
  pruneCaptchaChallenges()

  const answer = createCaptchaAnswer()
  const captchaId = randomUUID()
  const now = Date.now()
  const expiresAt = now + captchaTtlMs
  captchaChallenges.set(captchaId, {
    answer,
    createdAt: now,
    expiresAt
  })

  return {
    captchaId,
    image: renderCaptchaImage(answer),
    expiresAt: new Date(expiresAt).toISOString()
  }
}

export function verifyCaptchaChallenge(captchaId: string, captchaCode: string): boolean {
  cleanupCaptchaChallenges()
  const challenge = captchaChallenges.get(captchaId)
  if (!challenge) return false

  captchaChallenges.delete(captchaId)
  if (challenge.expiresAt < Date.now()) return false

  return normalizeCaptchaCode(captchaCode) === challenge.answer
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

function cleanupCaptchaChallenges(): void {
  const now = Date.now()
  for (const [captchaId, challenge] of captchaChallenges.entries()) {
    if (challenge.expiresAt < now) {
      captchaChallenges.delete(captchaId)
    }
  }
}

function pruneCaptchaChallenges(): void {
  if (captchaChallenges.size < maxCaptchaChallenges) return
  const overflow = captchaChallenges.size - maxCaptchaChallenges + 1
  const oldest = [...captchaChallenges.entries()]
    .sort((first, second) => first[1].createdAt - second[1].createdAt)
    .slice(0, overflow)
  for (const [captchaId] of oldest) {
    captchaChallenges.delete(captchaId)
  }
}

function renderCaptchaImage(answer: string): string {
  const width = 144
  const height = 46
  const chars = [...answer]
    .map((char, index) => {
      const x = 18 + index * 23 + randomInt(-2, 3)
      const y = 30 + randomInt(-3, 4)
      const rotation = randomInt(-18, 19)
      return `<text x="${x}" y="${y}" transform="rotate(${rotation} ${x} ${y})">${char}</text>`
    })
    .join('')
  const lines = Array.from({ length: 6 }, () => {
    const x1 = randomInt(0, width)
    const y1 = randomInt(0, height)
    const x2 = randomInt(0, width)
    const y2 = randomInt(0, height)
    const opacity = randomInt(12, 28) / 100
    return `<line x1="${x1}" y1="${y1}" x2="${x2}" y2="${y2}" opacity="${opacity}" />`
  }).join('')
  const dots = Array.from({ length: 28 }, () => {
    const cx = randomInt(4, width - 4)
    const cy = randomInt(4, height - 4)
    const radius = randomInt(1, 3)
    const opacity = randomInt(16, 42) / 100
    return `<circle cx="${cx}" cy="${cy}" r="${radius}" opacity="${opacity}" />`
  }).join('')
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${height}" viewBox="0 0 ${width} ${height}">
  <defs>
    <linearGradient id="bg" x1="0" x2="1" y1="0" y2="1">
      <stop offset="0" stop-color="#eff6ff"/>
      <stop offset="1" stop-color="#dbeafe"/>
    </linearGradient>
  </defs>
  <rect width="${width}" height="${height}" rx="10" fill="url(#bg)"/>
  <g stroke="#2563eb" stroke-width="1.4" stroke-linecap="round">${lines}</g>
  <g fill="#0ea5e9">${dots}</g>
  <g fill="#0f172a" font-family="Consolas, Menlo, monospace" font-size="24" font-weight="800" letter-spacing="2">${chars}</g>
</svg>`
  return `data:image/svg+xml;base64,${Buffer.from(svg).toString('base64')}`
}
