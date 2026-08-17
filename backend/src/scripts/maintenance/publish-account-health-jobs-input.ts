import { runtimeConfig } from '../../config/runtime.js'
import { publishAccountHealthJobsInputFromAccount } from '../../modules/background/account-health-jobs-input.service.js'
import {
  findAccountForAccountHealthJobsInputAsync,
  findAccountHealthJobsInputRevisionsAsync
} from '../../storage/account-health-jobs-input.repository.js'
import { reserveAccountHealthJobsInputVersion, reserveAccountHealthJobsInputVersionAsync } from '../../storage/account-health-jobs-input-version.repository.js'
import { getBusinessDatabase } from '../../storage/database.js'
import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { getPostgresPool } from '../../storage/postgres-client.js'
import { resolveProxyUrlForProfileAsync } from '../../storage/proxy.repository.js'
import { getSettingsAsync } from '../../storage/settings.repository.js'

const accountId = process.argv[2]?.trim()
if (!accountId || process.argv.length !== 3) {
  throw new Error('用法：tsx src/scripts/maintenance/publish-account-health-jobs-input.ts <accountId>')
}
const root = runtimeConfig.accountHealthJobs.inputDirectory
const signingKey = runtimeConfig.accountHealthJobs.inputSigningKey
if (!root || !signingKey) {
  throw new Error('必须设置 JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY 和 JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY')
}
const account = await findAccountForAccountHealthJobsInputAsync(accountId)
if (!account) {
  throw new Error(`账户 ${accountId} 不是可发布的 J1 候选；不会生成不完整 snapshot`)
}
const revisions = await findAccountHealthJobsInputRevisionsAsync(account.id)
if (!revisions || revisions.configRevision !== account.configRevision) {
  throw new Error(`账户 ${account.id} 的 J1 config/dispatch fence 在发布前不一致；请重新读取后重试`)
}
const settings = await getSettingsAsync()
const inputVersion = runtimeConfig.databaseDriver === 'postgres'
  ? await reserveAccountHealthJobsInputVersionAsync(createPostgresDatabaseClient(await getPostgresPool()), account.id)
  : reserveAccountHealthJobsInputVersion(account.id, getBusinessDatabase())
const numberSetting = (name: string, min: number, max: number): number => {
  const value = settings[name]
  if (typeof value !== 'number' || !Number.isInteger(value) || value < min || value > max) {
    throw new Error(`系统设置 ${name} 必须是 ${min}..${max} 的整数`)
  }
  return value
}
const path = publishAccountHealthJobsInputFromAccount({
  account,
  dispatchRevision: revisions.dispatchRevision,
  inputVersion,
  signingKey,
  root,
  settings: {
    intervalHours: numberSetting('accountHealthCheckIntervalHours', 1, 168),
    jitterMinutes: numberSetting('accountHealthCheckJitterMinutes', 0, 1440),
    failureThreshold: numberSetting('accountHealthCheckFailureThreshold', 1, 10)
  },
  expiresAt: new Date(Date.now() + runtimeConfig.accountHealthJobs.inputTtlMs),
  proxyUrl: account.proxyProfileId ? await resolveProxyUrlForProfileAsync(account.proxyProfileId) : undefined,
  sourceConfigRevision: account.accessType === 'authorized' ? account.sourceConfigRevision : undefined
})
process.stdout.write(`${JSON.stringify({ accountId: account.id, inputVersion, path })}\n`)
// This is a one-shot maintenance command. Repository clients may retain idle
// handles after the atomic file publish, so exit after flushing its result.
process.exit(0)
