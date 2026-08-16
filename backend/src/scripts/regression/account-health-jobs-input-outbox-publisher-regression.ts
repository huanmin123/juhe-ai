import assert from 'node:assert/strict'

import {
  publishNextAccountHealthJobsInputOutboxEvent,
  type AccountHealthJobsInputOutboxPublisherDependencies
} from '../../modules/background/account-health-jobs-input-outbox.service.js'
import type { AccountHealthJobsInputOutboxEvent } from '../../storage/account-health-jobs-input-outbox.repository.js'

const event: AccountHealthJobsInputOutboxEvent = {
  eventId: 'event-1', accountId: 'account-1', inputVersion: 3, kind: 'snapshot',
  reason: 'credentials_changed', configRevision: 7, dispatchRevision: 9,
  attemptCount: 1, claimToken: 'lease-1', claimedUntil: '2030-08-16T00:01:00.000Z'
}

const actions: string[] = []
const dependencies: AccountHealthJobsInputOutboxPublisherDependencies = {
  async claim() { actions.push('claim'); return event },
  async currentVersion() { actions.push('current'); return 3 },
  async publishSnapshot() { actions.push('snapshot') },
  async publishTombstone() { actions.push('tombstone') },
  async acknowledge() { actions.push('ack'); return true },
  async supersede() { actions.push('supersede'); return true },
  async fail() { actions.push('fail'); return true }
}

assert.equal(await publishNextAccountHealthJobsInputOutboxEvent(dependencies, { leaseMs: 30_000 }), 'published')
assert.deepEqual(actions, ['claim', 'current', 'snapshot', 'ack'])

actions.length = 0
const staleDependencies = { ...dependencies, async currentVersion() { actions.push('current'); return 4 } }
assert.equal(await publishNextAccountHealthJobsInputOutboxEvent(staleDependencies, { leaseMs: 30_000 }), 'superseded')
assert.deepEqual(actions, ['claim', 'current', 'supersede'])

actions.length = 0
let retryAt: Date | undefined
const failingDependencies = {
  ...dependencies,
  async publishSnapshot() { actions.push('snapshot'); throw new Error('file publish failed') },
  async fail(_event: AccountHealthJobsInputOutboxEvent, _errorCode: string, value: Date) { actions.push('fail'); retryAt = value; return true }
}
const start = new Date('2030-08-16T00:00:00.000Z')
assert.equal(await publishNextAccountHealthJobsInputOutboxEvent(failingDependencies, {
  leaseMs: 30_000,
  now: () => start,
  retryBaseMs: 1_000,
  retryMaxMs: 10_000
}), 'retry_scheduled')
assert.equal(retryAt?.toISOString(), '2030-08-16T00:00:01.000Z')
assert.deepEqual(actions, ['claim', 'current', 'snapshot', 'fail'])

console.log('account-health-jobs-input-outbox-publisher-regression passed')
