import { runtimeConfig } from '../../config/runtime.js'
import {
  acknowledgeAccountHealthJobsInputOutboxEvent,
  acknowledgeAccountHealthJobsInputOutboxEventAsync,
  claimNextAccountHealthJobsInputOutboxEvent,
  claimNextAccountHealthJobsInputOutboxEventAsync,
  failAccountHealthJobsInputOutboxEvent,
  failAccountHealthJobsInputOutboxEventAsync,
  supersedeAccountHealthJobsInputOutboxEvent,
  supersedeAccountHealthJobsInputOutboxEventAsync,
  type AccountHealthJobsInputOutboxEvent
} from '../../storage/account-health-jobs-input-outbox.repository.js'
import {
  currentAccountHealthJobsInputVersion,
  currentAccountHealthJobsInputVersionAsync
} from '../../storage/account-health-jobs-input-version.repository.js'
import {
  findAccountForAccountHealthJobsInput,
  findAccountForAccountHealthJobsInputAsync,
  findAccountHealthJobsInputRevisions,
  findAccountHealthJobsInputRevisionsAsync
} from '../../storage/account-health-jobs-input.repository.js'
import { getBusinessDatabase } from '../../storage/database.js'
import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { getPostgresPool } from '../../storage/postgres-client.js'
import { resolveProxyUrlForProfile, resolveProxyUrlForProfileAsync } from '../../storage/proxy.repository.js'
import { getSettings, getSettingsAsync } from '../../storage/settings.repository.js'
import {
  publishAccountHealthJobsInputFromAccount,
  publishAccountHealthJobsInputTombstone
} from './account-health-jobs-input.service.js'
import {
  publishNextAccountHealthJobsInputOutboxEvent,
  type AccountHealthJobsInputOutboxPublishDisposition
} from './account-health-jobs-input-outbox.service.js'

const publisherLeaseMs = 60_000

// This is deliberately a single-event DB-service executor.  It is not a
// background scheduler and remains unregistered until the complete J1 owner
// handoff.  Its only side effect outside the business transaction is the
// signed, atomically-renamed input file; it never contacts Go, Gateway, Redis,
// or an upstream.
export async function publishNextAccountHealthJobsInputFromBusinessOutbox(): Promise<AccountHealthJobsInputOutboxPublishDisposition> {
  const publication = requiredPublicationConfig()
  if (runtimeConfig.databaseDriver === 'postgres') {
    const client = createPostgresDatabaseClient(await getPostgresPool())
    return await publishNextAccountHealthJobsInputOutboxEvent({
      claim: async (leaseMs) => await claimNextAccountHealthJobsInputOutboxEventAsync(client, leaseMs),
      currentVersion: async (accountId) => await currentAccountHealthJobsInputVersionAsync(client, accountId),
      publishSnapshot: async (event) => await publishCurrentSnapshotAsync(event, publication.root, publication.signingKey),
      publishTombstone: async (event) => publishTombstone(event, publication.root, publication.signingKey),
      acknowledge: async (event) => await acknowledgeAccountHealthJobsInputOutboxEventAsync(client, event.eventId, event.claimToken),
      supersede: async (event) => await supersedeAccountHealthJobsInputOutboxEventAsync(client, event.eventId, event.claimToken),
      fail: async (event, errorCode, retryAt) => await failAccountHealthJobsInputOutboxEventAsync(client, event.eventId, event.claimToken, errorCode, retryAt.toISOString())
    }, { leaseMs: publisherLeaseMs })
  }

  const database = getBusinessDatabase()
  return await publishNextAccountHealthJobsInputOutboxEvent({
    claim: async (leaseMs) => claimNextAccountHealthJobsInputOutboxEvent(leaseMs, database),
    currentVersion: async (accountId) => currentAccountHealthJobsInputVersion(accountId, database),
    publishSnapshot: async (event) => publishCurrentSnapshot(event, publication.root, publication.signingKey),
    publishTombstone: async (event) => publishTombstone(event, publication.root, publication.signingKey),
    acknowledge: async (event) => acknowledgeAccountHealthJobsInputOutboxEvent(event.eventId, event.claimToken, database),
    supersede: async (event) => supersedeAccountHealthJobsInputOutboxEvent(event.eventId, event.claimToken, database),
    fail: async (event, errorCode, retryAt) => failAccountHealthJobsInputOutboxEvent(event.eventId, event.claimToken, errorCode, retryAt.toISOString(), database)
  }, { leaseMs: publisherLeaseMs })
}

function publishCurrentSnapshot(event: AccountHealthJobsInputOutboxEvent, root: string, signingKey: string): void {
  const account = findAccountForAccountHealthJobsInput(event.accountId)
  if (!account) {
    publishTombstone(event, root, signingKey)
    return
  }
  const revisions = findAccountHealthJobsInputRevisions(event.accountId)
  assertEventRevision(event, revisions?.configRevision, revisions?.dispatchRevision)
  const settings = inputSettings(getSettings())
  publishAccountHealthJobsInputFromAccount({
    account,
    dispatchRevision: event.dispatchRevision,
    inputVersion: event.inputVersion,
    root,
    signingKey,
    settings,
    expiresAt: new Date(Date.now() + runtimeConfig.accountHealthJobs.inputTtlMs),
    proxyUrl: account.proxyProfileId ? resolveProxyUrlForProfile(account.proxyProfileId) : undefined,
    sourceConfigRevision: account.accessType === 'authorized' ? account.sourceConfigRevision : undefined
  })
}

async function publishCurrentSnapshotAsync(event: AccountHealthJobsInputOutboxEvent, root: string, signingKey: string): Promise<void> {
  const account = await findAccountForAccountHealthJobsInputAsync(event.accountId)
  if (!account) {
    publishTombstone(event, root, signingKey)
    return
  }
  const revisions = await findAccountHealthJobsInputRevisionsAsync(event.accountId)
  assertEventRevision(event, revisions?.configRevision, revisions?.dispatchRevision)
  const settings = inputSettings(await getSettingsAsync())
  publishAccountHealthJobsInputFromAccount({
    account,
    dispatchRevision: event.dispatchRevision,
    inputVersion: event.inputVersion,
    root,
    signingKey,
    settings,
    expiresAt: new Date(Date.now() + runtimeConfig.accountHealthJobs.inputTtlMs),
    proxyUrl: account.proxyProfileId ? await resolveProxyUrlForProfileAsync(account.proxyProfileId) : undefined,
    sourceConfigRevision: account.accessType === 'authorized' ? account.sourceConfigRevision : undefined
  })
}

function publishTombstone(event: AccountHealthJobsInputOutboxEvent, root: string, signingKey: string): void {
  publishAccountHealthJobsInputTombstone({
    accountId: event.accountId,
    configRevision: event.configRevision,
    dispatchRevision: event.dispatchRevision,
    inputVersion: event.inputVersion,
    root,
    signingKey,
    reason: event.reason
  })
}

function assertEventRevision(event: AccountHealthJobsInputOutboxEvent, configRevision: number | undefined, dispatchRevision: number | undefined): void {
  if (configRevision !== event.configRevision || dispatchRevision !== event.dispatchRevision) {
    throw new Error('J1 input outbox event 与当前业务 revision 不一致')
  }
}

function inputSettings(settings: Record<string, unknown>) {
  return {
    intervalHours: settingInteger(settings, 'accountHealthCheckIntervalHours', 1, 168),
    jitterMinutes: settingInteger(settings, 'accountHealthCheckJitterMinutes', 0, 1440),
    failureThreshold: settingInteger(settings, 'accountHealthCheckFailureThreshold', 1, 10)
  }
}

function settingInteger(settings: Record<string, unknown>, name: string, min: number, max: number): number {
  const value = settings[name]
  if (typeof value !== 'number' || !Number.isInteger(value) || value < min || value > max) {
    throw new Error(`J1 input publisher 系统设置 ${name} 无效`)
  }
  return value
}

function requiredPublicationConfig(): { root: string, signingKey: string } {
  const root = runtimeConfig.accountHealthJobs.inputDirectory?.trim()
  const signingKey = runtimeConfig.accountHealthJobs.inputSigningKey?.trim()
  if (!root || !signingKey) {
    throw new Error('J1 input publisher 必须设置 input directory 和 signing key')
  }
  return { root, signingKey }
}
