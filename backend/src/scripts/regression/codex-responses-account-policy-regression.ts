import assert from 'node:assert/strict'

import {
  resolveCodexResponsesAccountPolicy,
  resolveCodexResponsesGuardMode
} from '../../modules/gateway/codex-responses/account-policy.js'
import { normalizeAccountCredentialsForWrite } from '../../storage/account-credentials-normalization.js'

assert.deepEqual(resolveCodexResponsesAccountPolicy(undefined), {
  safeRepairEnabled: true,
  strictInterceptEnabled: false
})
assert.equal(resolveCodexResponsesGuardMode({ globalMode: 'off', credentials: {} }), 'off')
assert.equal(resolveCodexResponsesGuardMode({ globalMode: 'shadow', credentials: {} }), 'safe_repair')
assert.equal(resolveCodexResponsesGuardMode({
  globalMode: 'shadow',
  credentials: {
    codex_responses_safe_repair_enabled: false,
    codex_responses_strict_intercept_enabled: false
  }
}), 'shadow')
assert.equal(resolveCodexResponsesGuardMode({
  globalMode: 'shadow',
  credentials: {
    codex_responses_safe_repair_enabled: true,
    codex_responses_strict_intercept_enabled: true
  }
}), 'strict_intercept')

const credentials = normalizeAccountCredentialsForWrite('api_key', {
  api_key: 'sk-policy-test',
  base_url: 'https://example.com/v1',
  codex_responses_safe_repair_enabled: false,
  codex_responses_strict_intercept_enabled: true
})
assert.equal(credentials.codex_responses_safe_repair_enabled, false)
assert.equal(credentials.codex_responses_strict_intercept_enabled, true)
assert.throws(() => normalizeAccountCredentialsForWrite('api_key', {
  api_key: 'sk-policy-test',
  base_url: 'https://example.com/v1',
  codex_responses_safe_repair_enabled: 'true'
}), /必须是布尔值/)

console.log('codex responses account policy regression passed')
