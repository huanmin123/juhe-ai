import assert from 'node:assert/strict'
import { createHmac } from 'node:crypto'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import {
  accountHealthInputSignatureAlgorithm,
  accountHealthInputSignatureKeyId,
  accountHealthJobsInputPath,
  publishAccountHealthJobsInput,
  readPublishedAccountHealthJobsInput
} from '../../modules/background/account-health-jobs-input.protocol.js'

const testRoot = resolve(process.env.JUHE_AI_TEST_TEMP_ROOT?.trim() || tmpdir())
const root = mkdtempSync(join(testRoot, 'juhe-ai-account-health-input-'))
try {
  const signingKey = Buffer.alloc(32, 7).toString('base64url')
  const accountId = 'account-1'
  const target = publishAccountHealthJobsInput({
    root,
    accountId,
    signingKey,
    payload: { account_id: accountId, input_version: 1, config_revision: 1 }
  })
  assert.equal(target, accountHealthJobsInputPath(root, accountId))
  const signed = readPublishedAccountHealthJobsInput(target)
  assert.equal(signed.algorithm, accountHealthInputSignatureAlgorithm)
  assert.equal(signed.key_id, accountHealthInputSignatureKeyId)
  const payload = Buffer.from(signed.payload, 'base64url')
  const expected = createHmac('sha256', Buffer.from(signingKey, 'base64url'))
    .update(`${accountHealthInputSignatureAlgorithm}\n${accountHealthInputSignatureKeyId}\n`, 'utf8')
    .update(payload)
    .digest('base64url')
  assert.equal(signed.signature, expected)

  publishAccountHealthJobsInput({
    root,
    accountId,
    signingKey,
    payload: { account_id: accountId, input_version: 2, config_revision: 2 }
  })
  const replaced = JSON.parse(Buffer.from(readPublishedAccountHealthJobsInput(target).payload, 'base64url').toString('utf8')) as { input_version?: number }
  assert.equal(replaced.input_version, 2)
} finally {
  rmSync(root, { recursive: true, force: true })
}

console.log('account-health-jobs-input-protocol-regression passed')
