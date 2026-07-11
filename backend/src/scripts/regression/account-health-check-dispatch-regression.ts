import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import { runtimeConfig } from '../../config/runtime.js'
import {
  accountUpdateNeedsImmediateHealthCheck,
  dispatchPendingAccountHealthCheck
} from '../../modules/accounts/account-health-check-dispatch.service.js'

const originalProcessRole = runtimeConfig.processRole
const originalSend = process.send
const messages: unknown[] = []

try {
  runtimeConfig.processRole = 'db-service'
  process.send = ((message: unknown, ...args: unknown[]) => {
    messages.push(message)
    const callback = args.find((item): item is (error: Error | null) => void => typeof item === 'function')
    callback?.(null)
    return true
  }) as typeof process.send

  assert.equal(dispatchPendingAccountHealthCheck({ id: 'acc_disabled', status: 'disabled' }), false)
  assert.deepEqual(messages, [], '停用账户不应投递后台健康检查')

  assert.equal(dispatchPendingAccountHealthCheck({ id: ' acc_pending ', status: 'pending_test' }), true)
  assert.deepEqual(messages, [{
    type: 'background_worker_account_health_check_trigger',
    accountId: 'acc_pending'
  }], '待检查账户应只向后台 worker 投递规范化账户 ID')

  assert.equal(accountUpdateNeedsImmediateHealthCheck({ notes: '仅改备注' }), false)
  assert.equal(accountUpdateNeedsImmediateHealthCheck({ credentials: { api_key: 'sk-updated' } }), true)
  assert.equal(accountUpdateNeedsImmediateHealthCheck({ healthCheckModel: 'gpt-5.5' }), true)

  for (const [name, sourcePath] of [
    ['普通账户新增', '../../modules/accounts/accounts.routes.ts'],
    ['账户导入', '../../modules/accounts/account-import-account-creator.ts'],
    ['OpenAI OAuth 新增', '../../modules/openai-oauth/openai-oauth.routes.ts'],
    ['外部账户推送', '../../modules/external-integrations/external-public-account-push.service.ts']
  ] as const) {
    const source = readFileSync(new URL(sourcePath, import.meta.url), 'utf8')
    assert(source.includes('dispatchPendingAccountHealthCheck('), `${name}必须在保存完成后立即投递后台健康检查`)
  }

  console.log('账户健康检查即时投递回归通过：所有新增入口统一投递，非健康配置编辑不误触发')
} finally {
  runtimeConfig.processRole = originalProcessRole
  process.send = originalSend
}
