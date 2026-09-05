import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import {
  chatLongSessionRunIdentityFileName,
  resolveChatLongSessionRunSecret
} from './chat-long-session-run-identity.js'

const root = mkdtempSync(join(tmpdir(), 'juhe-ai-chat-run-identity-'))
const otherRoot = mkdtempSync(join(tmpdir(), 'juhe-ai-chat-run-identity-other-'))

try {
  const first = resolveChatLongSessionRunSecret(root, { resuming: false, keyMaterial: 'credential-one' })
  const identityPath = join(root, chatLongSessionRunIdentityFileName)
  assert(existsSync(identityPath), '首次运行必须落盘非秘密随机盐')
  const identity = readFileSync(identityPath, 'utf8').trim()
  assert.match(identity, /^v2:[a-f0-9]{64}$/)
  assert(!first.includes(identity), '派生后的 runtime secret 不得直接包含随机盐')
  assert.throws(
    () => resolveChatLongSessionRunSecret(root, { resuming: true }),
    /key_material_required/,
    '只有临时目录中的随机盐不得恢复数据库密钥'
  )

  assert.equal(
    resolveChatLongSessionRunSecret(root, { resuming: true, keyMaterial: 'credential-one' }),
    first,
    '同一运行目录必须从已落盘 v2 identity 与相同凭据恢复 runtime secret'
  )
  assert.equal(readFileSync(identityPath, 'utf8').trim(), identity, '恢复运行不能改写 v2 identity')

  assert.notEqual(
    resolveChatLongSessionRunSecret(root, { resuming: true, keyMaterial: 'credential-two' }),
    first,
    '不同凭据不得从同一随机盐派生相同 runtime secret'
  )

  const isolated = resolveChatLongSessionRunSecret(otherRoot, { resuming: false, keyMaterial: 'credential-one' })
  assert.notEqual(isolated, first, '不同 run 必须使用隔离的 runtime secret')

  const legacyRoot = mkdtempSync(join(tmpdir(), 'juhe-ai-chat-run-identity-legacy-'))
  try {
    assert.throws(
      () => resolveChatLongSessionRunSecret(resolve(legacyRoot), { resuming: true, keyMaterial: 'credential-one' }),
      /identity_missing/,
      '缺失随机盐的目录不得按可猜路径降级派生密钥'
    )
    const explicitLegacyRoot = resolve(legacyRoot)
    assert.equal(
      resolveChatLongSessionRunSecret(explicitLegacyRoot, { resuming: true, keyMaterial: 'credential-one', allowLegacyPathDerivation: true }),
      `chat-long-${createHash('sha256').update(explicitLegacyRoot).digest('hex')}`,
      '只允许显式开关恢复已经存在的 v1 路径派生验收目录'
    )
  } finally {
    rmSync(legacyRoot, { recursive: true, force: true })
  }

  const legacyIdentityRoot = mkdtempSync(join(tmpdir(), 'juhe-ai-chat-run-identity-v1-'))
  try {
    const legacyIdentity = 'a'.repeat(64)
    writeFileSync(join(legacyIdentityRoot, chatLongSessionRunIdentityFileName), `${legacyIdentity}\n`, 'utf8')
    assert.throws(
      () => resolveChatLongSessionRunSecret(legacyIdentityRoot, { resuming: true, keyMaterial: 'credential-one' }),
      /legacy_identity_not_allowed/,
      '旧版 v1 identity 即使存在，也不得在没有显式 legacy 开关时恢复'
    )
    assert.equal(
      resolveChatLongSessionRunSecret(legacyIdentityRoot, {
        resuming: true,
        keyMaterial: 'credential-one',
        allowLegacyPathDerivation: true
      }),
      `chat-long-${createHash('sha256').update(`chat-long-session-run:${legacyIdentity}`).digest('hex')}`,
      '显式 legacy 开关必须仍可恢复已保留的 v1 identity 验收目录'
    )
  } finally {
    rmSync(legacyIdentityRoot, { recursive: true, force: true })
  }
} finally {
  rmSync(root, { recursive: true, force: true })
  rmSync(otherRoot, { recursive: true, force: true })
}

console.log('chat long session run identity regression passed')
