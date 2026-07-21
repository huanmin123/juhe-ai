import assert from 'node:assert/strict'

import type {
  PageDataConfirmRequest,
  PageDataConfirmResult,
  PageDataDomain,
  PageDataRevisionToken
} from '../../api/domains/pageData'
import {
  createPageDataActivationCoordinator,
  type PageDataActivationDecision,
  type PageDataActivationParticipant,
  type PageDataActivationTimer
} from '../../shared/pageDataActivationCoordinator'
import { myAccountsPageDataActivationManifest } from '../../shared/pageDataActivationManifests'

class FakeClock {
  private nextId = 1
  private readonly tasks = new Map<number, { at: number; callback: () => void }>()
  nowMs = 0

  readonly timer: PageDataActivationTimer = {
    setTimeout: (callback, delayMs) => {
      const id = this.nextId++
      this.tasks.set(id, { at: this.nowMs + delayMs, callback })
      return id
    },
    clearTimeout: (handle) => {
      this.tasks.delete(handle as number)
    }
  }

  async advanceTo(targetMs: number): Promise<void> {
    while (true) {
      const due = [...this.tasks.entries()]
        .filter(([, task]) => task.at <= targetMs)
        .sort((left, right) => left[1].at - right[1].at || left[0] - right[0])[0]
      if (!due) break
      this.tasks.delete(due[0])
      this.nowMs = due[1].at
      due[1].callback()
      await flushMicrotasks()
    }
    this.nowMs = targetMs
    await flushMicrotasks()
  }
}

const token = (domain: PageDataDomain, sequence: number): PageDataRevisionToken => ({
  protocolVersion: 2,
  epoch: 'epoch-1',
  scope: 'scope-1',
  domain,
  sequence,
  resetSequence: 0
})

const participant = (
  resourceKey: string,
  domain: PageDataDomain,
  revision = token(domain, 1)
): PageDataActivationParticipant => ({
  resourceKey,
  domain,
  token: revision,
  generation: 1,
  writeEpoch: 0
})

const confirmationFor = (request: PageDataConfirmRequest): PageDataConfirmResult => ({
  serverTime: '2026-07-22T00:00:00.000Z',
  domains: Object.fromEntries(Object.entries(request.domains).map(([domain, known]) => [
    domain,
    {
      action: known ? 'unchanged' : 'reload',
      token: known ?? token(domain as PageDataDomain, 1)
    }
  ]))
})

async function testCollectionWindow(): Promise<void> {
  const clock = new FakeClock()
  const requests: PageDataConfirmRequest[] = []
  const handle = createPageDataActivationCoordinator({
    manifest: myAccountsPageDataActivationManifest,
    viewScope: 'self',
    confirm: async (request) => {
      requests.push(request)
      return confirmationFor(request)
    },
    timer: clock.timer,
    now: () => clock.nowMs
  })

  handle.trigger('mount')
  const accounts = handle.register(participant('accounts', 'accounts.static'))
  await clock.advanceTo(25)
  const options = handle.register(participant('options', 'accounts.options'))
  await clock.advanceTo(45)
  const providers = handle.register(participant('providers', 'providers.catalog'))

  assert.equal(requests.length, 0, 'the 50ms collection window must remain open at 45ms')
  await clock.advanceTo(50)
  const decisions = await Promise.all([accounts, options, providers])

  assert.equal(requests.length, 1, 'three staggered domains must share one page-level confirm')
  assert.deepEqual(Object.keys(requests[0]?.domains ?? {}).sort(), [
    'accounts.options',
    'accounts.static',
    'providers.catalog'
  ])
  assert.deepEqual(decisions.map(({ state }) => state), ['confirmed', 'confirmed', 'confirmed'])
}

async function testTokenDeduplicationAndConflict(): Promise<void> {
  const dedupeClock = new FakeClock()
  const dedupeRequests: PageDataConfirmRequest[] = []
  const dedupeHandle = createPageDataActivationCoordinator({
    manifest: myAccountsPageDataActivationManifest,
    viewScope: 'self',
    confirm: async (request) => {
      dedupeRequests.push(request)
      return confirmationFor(request)
    },
    timer: dedupeClock.timer,
    now: () => dedupeClock.nowMs
  })
  dedupeHandle.trigger('activate')
  const first = dedupeHandle.register(participant('accounts-list', 'accounts.static'))
  const duplicate = dedupeHandle.register(participant('accounts-summary', 'accounts.static'))
  await dedupeClock.advanceTo(50)

  assert.equal(dedupeRequests.length, 1)
  assert.deepEqual(Object.keys(dedupeRequests[0]?.domains ?? {}), ['accounts.static'])
  assert.deepEqual((await Promise.all([first, duplicate])).map(({ state }) => state), ['confirmed', 'confirmed'])

  const conflictClock = new FakeClock()
  let conflictRequests = 0
  const conflictHandle = createPageDataActivationCoordinator({
    manifest: myAccountsPageDataActivationManifest,
    viewScope: 'self',
    confirm: async (request) => {
      conflictRequests += 1
      return confirmationFor(request)
    },
    timer: conflictClock.timer,
    now: () => conflictClock.nowMs
  })
  conflictHandle.trigger('focus')
  const original = conflictHandle.register(participant('accounts-list', 'accounts.static', token('accounts.static', 1)))
  const conflicting = conflictHandle.register(participant('accounts-summary', 'accounts.static', token('accounts.static', 2)))
  await conflictClock.advanceTo(50)

  assert.deepEqual((await Promise.all([original, conflicting])).map(({ state }) => state), ['token_conflict', 'token_conflict'])
  assert.equal(conflictRequests, 0, 'a conflicted domain must not be silently confirmed with either token')
}

async function testFreezeAndPostBarrier(): Promise<void> {
  const clock = new FakeClock()
  const requests: PageDataConfirmRequest[] = []
  const handle = createPageDataActivationCoordinator({
    manifest: myAccountsPageDataActivationManifest,
    viewScope: 'self',
    confirm: async (request) => {
      requests.push(request)
      return confirmationFor(request)
    },
    timer: clock.timer,
    now: () => clock.nowMs
  })
  handle.trigger('interval')
  const initial = participant('accounts', 'accounts.static')
  const pre = handle.register(initial)
  await clock.advanceTo(50)
  assert.equal((await pre).state, 'confirmed')

  const late = await handle.register(participant('options', 'accounts.options'))
  assert.equal(late.state, 'late')
  assert.equal(requests.length, 1, 'late registration must not open a second ordinary batch')

  const stableAccounts = handle.stabilize({ ...initial, baseline: token('accounts.static', 1) })
  await clock.advanceTo(75)
  const stableOptions = handle.stabilize({
    ...participant('options', 'accounts.options'),
    baseline: token('accounts.options', 1)
  })
  await clock.advanceTo(100)
  const postDecisions = await Promise.all([stableAccounts, stableOptions])

  assert.equal(requests.length, 2, 'stabilization participants must share one post-confirm barrier')
  assert.deepEqual(Object.keys(requests[1]?.domains ?? {}).sort(), ['accounts.options', 'accounts.static'])
  assert.deepEqual(postDecisions.map(({ state }) => state), ['confirmed', 'confirmed'])
}

async function testDeactivateSupersedesPendingResult(): Promise<void> {
  const clock = new FakeClock()
  const gate = deferred<PageDataConfirmResult>()
  let request: PageDataConfirmRequest | undefined
  const handle = createPageDataActivationCoordinator({
    manifest: myAccountsPageDataActivationManifest,
    viewScope: 'self',
    confirm: async (input) => {
      request = input
      return gate.promise
    },
    timer: clock.timer,
    now: () => clock.nowMs
  })
  handle.trigger('mount')
  const pending = handle.register(participant('accounts', 'accounts.static'))
  await clock.advanceTo(50)
  assert.ok(request, 'confirm must be in flight before deactivation')

  handle.deactivate()
  assert.equal((await pending).state, 'superseded', 'deactivation must supersede every unresolved decision')
  gate.resolve(confirmationFor(request))
  await flushMicrotasks()

  handle.dispose()
  assert.equal((await handle.register(participant('accounts', 'accounts.static'))).state, 'superseded')
}

assert.equal(myAccountsPageDataActivationManifest.route, '/my-accounts')
assert.deepEqual(myAccountsPageDataActivationManifest.domains, [
  'accounts.static',
  'accounts.options',
  'providers.catalog'
])

await testCollectionWindow()
await testTokenDeduplicationAndConflict()
await testFreezeAndPostBarrier()
await testDeactivateSupersedesPendingResult()

console.log('Page data activation coordinator regression passed')

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((accept) => { resolve = accept })
  return { promise, resolve }
}

async function flushMicrotasks(): Promise<void> {
  await Promise.resolve()
  await Promise.resolve()
}

void (undefined as PageDataActivationDecision | undefined)
