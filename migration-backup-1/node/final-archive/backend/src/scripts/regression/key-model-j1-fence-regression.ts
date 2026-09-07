import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { accountHealthJobsRequestPath, publishAccountHealthJobsRequest } from '../../modules/background/account-health-jobs-input.protocol.js'

const root = mkdtempSync(join(tmpdir(), 'key-model-j1-fence-'))
const signingKey = Buffer.alloc(32, 7).toString('base64url')
const capabilityHash = 'a'.repeat(64)
const requestId = 'j1-key-model-fence-regression'
publishAccountHealthJobsRequest({
  root,
  requestId,
  signingKey,
  payload: {
    request_id: requestId,
    account_id: 'source-1',
    reason: 'request_failure',
    input_version: 4,
    config_revision: 3,
    dispatch_revision: 7,
    deadline: new Date(Date.now() + 60_000).toISOString(),
    mutate_account: true,
    key_model_fence: {
      capability_hash: capabilityHash,
      key_fingerprint: 'key-a',
      dispatch_revision: 7,
      owner_id: 'attempt-1'
    }
  }
})
const envelope = JSON.parse(readFileSync(accountHealthJobsRequestPath(root, requestId), 'utf8')) as { payload: string }
const payload = JSON.parse(Buffer.from(envelope.payload, 'base64url').toString('utf8')) as Record<string, any>
assert.deepEqual(payload.key_model_fence, {
  capability_hash: capabilityHash,
  key_fingerprint: 'key-a',
  dispatch_revision: 7,
  owner_id: 'attempt-1'
})
assert.equal(payload.mutate_account, true, 'key-model fence request 仍由 J1 主探测决定账户状态')
console.log('key-model-j1-fence regression passed')
