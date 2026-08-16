import { runtimeConfig } from '../../config/runtime.js'
import { publishAccountHealthJobsInputFromAccount } from '../../modules/background/account-health-jobs-input.service.js'
import { findAccountForHealthCheckAsync } from '../../storage/account-health-check.repository.js'
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
const account = await findAccountForHealthCheckAsync(accountId, { ignoreSchedule: true })
if (!account) {
  throw new Error(`账户 ${accountId} 不是可发布的 active/pending_test J1 候选；不会生成不完整 snapshot`)
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
  dispatchRevision: account.dispatchRevision ?? 0,
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
console.log(JSON.stringify({ accountId: account.id, inputVersion, path }))
