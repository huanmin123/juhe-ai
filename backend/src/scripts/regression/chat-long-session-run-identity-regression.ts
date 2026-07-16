import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { existsSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import {
  chatLongSessionRunIdentityFileName,
  resolveChatLongSessionRunSecret
} from './chat-long-session-run-identity.js'

const root = mkdtempSync(join(tmpdir(), 'juhe-ai-chat-run-identity-'))
const otherRoot = mkdtempSync(join(tmpdir(), 'juhe-ai-chat-run-identity-other-'))

try {
  const first = resolveChatLongSessionRunSecret(root, { resuming: false })
  const identityPath = join(root, chatLongSessionRunIdentityFileName)
  assert(existsSync(identityPath), '首次运行必须落盘 opaque run identity')
  const identity = readFileSync(identityPath, 'utf8').trim()
  assert.match(identity, /^[a-f0-9]{64}$/)
  assert(!first.includes(identity), '派生后的 runtime secret 不得直接包含 opaque identity')

  const longPath = resolve(root)
  const equivalentPath = process.platform === 'win32'
    ? longPath.replace(/^C:\\Users\\Administrator(?=\\)/i, 'C:\\Users\\ADMINI~1')
    : longPath
  assert.equal(
    resolveChatLongSessionRunSecret(equivalentPath, { resuming: true }),
    first,
    '同一运行目录的 Windows 短路径与长路径必须派生相同 runtime secret'
  )

  const isolated = resolveChatLongSessionRunSecret(otherRoot, { resuming: false })
  assert.notEqual(isolated, first, '不同 run 必须使用隔离的 runtime secret')

  const legacyRoot = mkdtempSync(join(tmpdir(), 'juhe-ai-chat-run-identity-legacy-'))
  try {
    const explicitLegacyRoot = resolve(legacyRoot)
    const legacy = resolveChatLongSessionRunSecret(explicitLegacyRoot, { resuming: true })
    assert.equal(legacy, `chat-long-${createHash('sha256').update(explicitLegacyRoot).digest('hex')}`)
    assert(!existsSync(join(legacyRoot, chatLongSessionRunIdentityFileName)), 'legacy resume 不得写入新 identity 或静默切换密钥')
  } finally {
    rmSync(legacyRoot, { recursive: true, force: true })
  }
} finally {
  rmSync(root, { recursive: true, force: true })
  rmSync(otherRoot, { recursive: true, force: true })
}

console.log('chat long session run identity regression passed')
