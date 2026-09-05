import type { DbServiceOperation } from '../../db-service/db-service-types.js'
import { requiredRfc3339Instant, rfc3339InstantMilliseconds } from '../../../shared/rfc3339.js'

export type AccountErrorHandlingOperation = Extract<DbServiceOperation, { type: 'apply_account_error_handling' }>
export type AccountSideEffectOperation = AccountErrorHandlingOperation

export interface AccountSideEffectEpoch {
  runtimeKey: string
  sequence: number
  observedAt: string
  success: boolean
  dispatchRevision?: number
}

export interface AccountSideEffectEpochDecision {
  accepted: boolean
  epoch: AccountSideEffectEpoch
}

export class AccountSideEffectEpochRegistry {
  private readonly currentByRuntimeKey = new Map<string, AccountSideEffectEpoch>()
  private readonly retainedEpochs = new Set<AccountSideEffectEpoch>()
  private readonly retainedCountByRuntimeKey = new Map<string, number>()

  constructor(private readonly capacity = 20_000) {
    if (!Number.isSafeInteger(capacity) || capacity < 1) {
      throw new Error('account side effect epoch capacity must be a positive integer')
    }
  }

  observe(
    runtimeKey: string,
    observation: { observedAt: string; success: boolean; dispatchRevision?: number; retain?: boolean }
  ): AccountSideEffectEpochDecision {
    const normalizedRuntimeKey = runtimeKey.trim()
    if (!normalizedRuntimeKey) throw new Error('account side effect runtimeKey is required')
    const observedAt = requiredRfc3339Instant(observation.observedAt, 'account side effect observedAt')
    const observedAtMs = rfc3339InstantMilliseconds(observedAt)
    if (observedAtMs === undefined) throw new Error('account side effect observedAt must be a RFC3339 instant')
    const dispatchRevision = normalizedDispatchRevision(observation.dispatchRevision)
    const current = this.currentByRuntimeKey.get(normalizedRuntimeKey)
    let currentObservedAtMs = Number.NEGATIVE_INFINITY
    if (current) {
      const parsedCurrentObservedAtMs = rfc3339InstantMilliseconds(current.observedAt)
      if (parsedCurrentObservedAtMs === undefined) throw new Error('account side effect current observedAt must be a RFC3339 instant')
      currentObservedAtMs = parsedCurrentObservedAtMs
    }
    const staleByRevision = current?.dispatchRevision !== undefined
      && (dispatchRevision === undefined || dispatchRevision < current.dispatchRevision)
    const newerRevision = dispatchRevision !== undefined
      && (current?.dispatchRevision === undefined || dispatchRevision > current.dispatchRevision)
    const staleByTime = !newerRevision && observedAtMs < currentObservedAtMs
    const staleEqualTimestampFailure = !newerRevision
      && observedAtMs === currentObservedAtMs
      && current?.success === true
      && !observation.success
    if (staleByRevision || staleByTime || staleEqualTimestampFailure) {
      return {
        accepted: false,
        epoch: {
          runtimeKey: normalizedRuntimeKey,
          sequence: current?.sequence ?? 0,
          observedAt,
          success: observation.success,
          ...(dispatchRevision === undefined ? {} : { dispatchRevision })
        }
      }
    }
    const epoch: AccountSideEffectEpoch = {
      runtimeKey: normalizedRuntimeKey,
      sequence: (current?.sequence ?? 0) + 1,
      observedAt,
      success: observation.success,
      ...(dispatchRevision === undefined ? {} : { dispatchRevision })
    }
    this.currentByRuntimeKey.delete(normalizedRuntimeKey)
    this.currentByRuntimeKey.set(normalizedRuntimeKey, epoch)
    if (observation.retain) {
      this.retain(epoch)
    }
    this.trimToCapacity(normalizedRuntimeKey)
    return { accepted: true, epoch }
  }

  isCurrent(epoch: AccountSideEffectEpoch): boolean {
    const current = this.currentByRuntimeKey.get(epoch.runtimeKey)
    return current?.sequence === epoch.sequence
      && current.observedAt === epoch.observedAt
      && current.success === epoch.success
      && current.dispatchRevision === epoch.dispatchRevision
  }

  release(epoch: AccountSideEffectEpoch): void {
    if (!this.retainedEpochs.delete(epoch)) {
      return
    }
    const retainedCount = this.retainedCountByRuntimeKey.get(epoch.runtimeKey) ?? 0
    if (retainedCount <= 1) {
      this.retainedCountByRuntimeKey.delete(epoch.runtimeKey)
    } else {
      this.retainedCountByRuntimeKey.set(epoch.runtimeKey, retainedCount - 1)
    }
    this.trimToCapacity()
  }

  clear(): void {
    this.currentByRuntimeKey.clear()
    this.retainedEpochs.clear()
    this.retainedCountByRuntimeKey.clear()
  }

  private retain(epoch: AccountSideEffectEpoch): void {
    this.retainedEpochs.add(epoch)
    this.retainedCountByRuntimeKey.set(
      epoch.runtimeKey,
      (this.retainedCountByRuntimeKey.get(epoch.runtimeKey) ?? 0) + 1
    )
  }

  private trimToCapacity(preserveRuntimeKey?: string): void {
    while (this.currentByRuntimeKey.size > this.capacity) {
      let evicted = false
      const retainedPrefix: Array<[string, AccountSideEffectEpoch]> = []
      for (const [runtimeKey, epoch] of this.currentByRuntimeKey) {
        if (runtimeKey === preserveRuntimeKey || (this.retainedCountByRuntimeKey.get(runtimeKey) ?? 0) > 0) {
          retainedPrefix.push([runtimeKey, epoch])
          continue
        }
        this.currentByRuntimeKey.delete(runtimeKey)
        evicted = true
        break
      }
      // 活跃项移到 LRU 末尾，持续容量压力不必反复扫描同一段 retained 前缀。
      for (const [runtimeKey, epoch] of retainedPrefix) {
        if (this.currentByRuntimeKey.get(runtimeKey) !== epoch) continue
        this.currentByRuntimeKey.delete(runtimeKey)
        this.currentByRuntimeKey.set(runtimeKey, epoch)
      }
      if (!evicted) {
        break
      }
    }
  }
}

function normalizedDispatchRevision(value: number | undefined): number | undefined {
  return Number.isSafeInteger(value) && (value ?? 0) > 0 ? value : undefined
}

export interface QueuedAccountSideEffect {
  operation: AccountSideEffectOperation
  epoch: AccountSideEffectEpoch
  attempts: number
  enqueuedAtMs: number
  nextAttemptAtMs: number
  expiresAtMs: number
}

export class AccountSideEffectQueue {
  private readonly items: QueuedAccountSideEffect[] = []
  private readonly indexByItem = new Map<QueuedAccountSideEffect, number>()
  private readonly itemsByRuntimeKey = new Map<string, Set<QueuedAccountSideEffect>>()
  private readonly failuresByAge: QueuedAccountSideEffect[] = []
  private readonly failureAgeIndexByItem = new Map<QueuedAccountSideEffect, number>()
  private failureCount = 0

  get length(): number {
    return this.items.length
  }

  get hasFailures(): boolean {
    return this.failureCount > 0
  }

  push(item: QueuedAccountSideEffect): void {
    this.items.push(item)
    this.registerItem(item, this.items.length - 1)
    this.heapifyUp(this.items.length - 1)
  }

  peek(): QueuedAccountSideEffect | undefined {
    return this.items[0]
  }

  pop(): QueuedAccountSideEffect | undefined {
    return this.removeAt(0)
  }

  clear(): void {
    this.items.splice(0)
    this.indexByItem.clear()
    this.itemsByRuntimeKey.clear()
    this.failuresByAge.splice(0)
    this.failureAgeIndexByItem.clear()
    this.failureCount = 0
  }

  findIndex(predicate: (item: QueuedAccountSideEffect) => boolean): number {
    return this.items.findIndex(predicate)
  }

  findIndexByRuntimeKey(runtimeKey: string): number {
    const item = this.itemsByRuntimeKey.get(runtimeKey)?.values().next().value as QueuedAccountSideEffect | undefined
    return item ? (this.indexByItem.get(item) ?? -1) : -1
  }

  hasRuntimeKey(runtimeKey: string): boolean {
    return (this.itemsByRuntimeKey.get(runtimeKey)?.size ?? 0) > 0
  }

  replaceAt(index: number, item: QueuedAccountSideEffect): QueuedAccountSideEffect | undefined {
    if (index < 0 || index >= this.items.length) {
      return undefined
    }
    const replaced = this.items[index]
    this.unregisterItem(replaced)
    this.items[index] = item
    this.registerItem(item, index)
    this.rebalanceAt(index)
    return replaced
  }

  removeWhere(predicate: (item: QueuedAccountSideEffect) => boolean): number {
    return this.removeWhereItems(predicate).length
  }

  removeWhereItems(predicate: (item: QueuedAccountSideEffect) => boolean): QueuedAccountSideEffect[] {
    let writeIndex = 0
    const removed: QueuedAccountSideEffect[] = []
    for (let index = 0; index < this.items.length; index += 1) {
      const item = this.items[index]
      if (predicate(item)) {
        removed.push(item)
        continue
      }
      this.items[writeIndex] = item
      writeIndex += 1
    }
    if (removed.length === 0) {
      return removed
    }
    this.items.length = writeIndex
    this.rebuildIndexes()
    this.heapify()
    return removed
  }

  removeRuntimeKey(runtimeKey: string): QueuedAccountSideEffect[] {
    const matchingItems = [...(this.itemsByRuntimeKey.get(runtimeKey) ?? [])]
    const removed: QueuedAccountSideEffect[] = []
    for (const item of matchingItems) {
      const index = this.indexByItem.get(item)
      if (index === undefined) continue
      const removedItem = this.removeAt(index)
      if (removedItem) removed.push(removedItem)
    }
    return removed
  }

  removeOldestWhere(predicate: (item: QueuedAccountSideEffect) => boolean): QueuedAccountSideEffect | undefined {
    let oldestIndex = -1
    for (let index = 0; index < this.items.length; index += 1) {
      const item = this.items[index]
      if (!predicate(item)) continue
      if (oldestIndex < 0 || item.enqueuedAtMs < this.items[oldestIndex].enqueuedAtMs) {
        oldestIndex = index
      }
    }
    if (oldestIndex < 0) return undefined
    return this.removeAt(oldestIndex)
  }

  removeOldestFailure(): QueuedAccountSideEffect | undefined {
    const oldestFailure = this.failuresByAge[0]
    if (!oldestFailure) return undefined
    const index = this.indexByItem.get(oldestFailure)
    return index === undefined ? undefined : this.removeAt(index)
  }

  private heapify(): void {
    for (let index = Math.floor(this.items.length / 2) - 1; index >= 0; index -= 1) {
      this.heapifyDown(index)
    }
  }

  private heapifyUp(startIndex: number): number {
    let index = startIndex
    while (index > 0) {
      const parentIndex = Math.floor((index - 1) / 2)
      if (compareSideEffectQueueItems(this.items[parentIndex], this.items[index]) <= 0) {
        return index
      }
      this.swap(index, parentIndex)
      index = parentIndex
    }
    return index
  }

  private heapifyDown(startIndex: number): void {
    let index = startIndex
    while (true) {
      const leftIndex = index * 2 + 1
      const rightIndex = leftIndex + 1
      let smallestIndex = index
      if (
        leftIndex < this.items.length
        && compareSideEffectQueueItems(this.items[leftIndex], this.items[smallestIndex]) < 0
      ) {
        smallestIndex = leftIndex
      }
      if (
        rightIndex < this.items.length
        && compareSideEffectQueueItems(this.items[rightIndex], this.items[smallestIndex]) < 0
      ) {
        smallestIndex = rightIndex
      }
      if (smallestIndex === index) {
        return
      }
      this.swap(index, smallestIndex)
      index = smallestIndex
    }
  }

  private swap(leftIndex: number, rightIndex: number): void {
    const left = this.items[leftIndex]
    const right = this.items[rightIndex]
    this.items[leftIndex] = right
    this.items[rightIndex] = left
    this.indexByItem.set(right, leftIndex)
    this.indexByItem.set(left, rightIndex)
  }

  private rebalanceAt(index: number): void {
    const indexAfterHeapifyUp = this.heapifyUp(index)
    this.heapifyDown(indexAfterHeapifyUp)
  }

  private removeAt(index: number): QueuedAccountSideEffect | undefined {
    if (index < 0 || index >= this.items.length) return undefined
    const removed = this.items[index]
    const last = this.items.pop()
    this.unregisterItem(removed)
    if (last && index < this.items.length) {
      this.items[index] = last
      this.indexByItem.set(last, index)
      this.rebalanceAt(index)
    }
    return removed
  }

  private registerItem(item: QueuedAccountSideEffect, index: number): void {
    this.indexByItem.set(item, index)
    const runtimeItems = this.itemsByRuntimeKey.get(item.epoch.runtimeKey) ?? new Set<QueuedAccountSideEffect>()
    runtimeItems.add(item)
    this.itemsByRuntimeKey.set(item.epoch.runtimeKey, runtimeItems)
    if (!item.operation.input.success) {
      this.failureCount += 1
      this.pushFailureByAge(item)
    }
  }

  private unregisterItem(item: QueuedAccountSideEffect): void {
    this.indexByItem.delete(item)
    const runtimeItems = this.itemsByRuntimeKey.get(item.epoch.runtimeKey)
    runtimeItems?.delete(item)
    if (runtimeItems?.size === 0) {
      this.itemsByRuntimeKey.delete(item.epoch.runtimeKey)
    }
    if (!item.operation.input.success) {
      this.failureCount -= 1
      this.removeFailureByAge(item)
    }
  }

  private rebuildIndexes(): void {
    this.indexByItem.clear()
    this.itemsByRuntimeKey.clear()
    this.failuresByAge.splice(0)
    this.failureAgeIndexByItem.clear()
    this.failureCount = 0
    for (let index = 0; index < this.items.length; index += 1) {
      this.registerItem(this.items[index], index)
    }
  }

  private pushFailureByAge(item: QueuedAccountSideEffect): void {
    this.failuresByAge.push(item)
    let index = this.failuresByAge.length - 1
    this.failureAgeIndexByItem.set(item, index)
    while (index > 0) {
      const parentIndex = Math.floor((index - 1) / 2)
      if (compareSideEffectFailureAge(this.failuresByAge[parentIndex], this.failuresByAge[index]) <= 0) {
        break
      }
      this.swapFailureAge(index, parentIndex)
      index = parentIndex
    }
  }

  private removeFailureByAge(item: QueuedAccountSideEffect): void {
    const index = this.failureAgeIndexByItem.get(item)
    if (index === undefined) return
    const last = this.failuresByAge.pop()
    this.failureAgeIndexByItem.delete(item)
    if (!last || index >= this.failuresByAge.length) return
    this.failuresByAge[index] = last
    this.failureAgeIndexByItem.set(last, index)
    this.rebalanceFailureAgeAt(index)
  }

  private rebalanceFailureAgeAt(startIndex: number): void {
    let index = startIndex
    while (index > 0) {
      const parentIndex = Math.floor((index - 1) / 2)
      if (compareSideEffectFailureAge(this.failuresByAge[parentIndex], this.failuresByAge[index]) <= 0) {
        break
      }
      this.swapFailureAge(index, parentIndex)
      index = parentIndex
    }
    while (true) {
      const leftIndex = index * 2 + 1
      const rightIndex = leftIndex + 1
      let smallestIndex = index
      if (
        leftIndex < this.failuresByAge.length
        && compareSideEffectFailureAge(this.failuresByAge[leftIndex], this.failuresByAge[smallestIndex]) < 0
      ) {
        smallestIndex = leftIndex
      }
      if (
        rightIndex < this.failuresByAge.length
        && compareSideEffectFailureAge(this.failuresByAge[rightIndex], this.failuresByAge[smallestIndex]) < 0
      ) {
        smallestIndex = rightIndex
      }
      if (smallestIndex === index) break
      this.swapFailureAge(index, smallestIndex)
      index = smallestIndex
    }
  }

  private swapFailureAge(leftIndex: number, rightIndex: number): void {
    const left = this.failuresByAge[leftIndex]
    const right = this.failuresByAge[rightIndex]
    this.failuresByAge[leftIndex] = right
    this.failuresByAge[rightIndex] = left
    this.failureAgeIndexByItem.set(right, leftIndex)
    this.failureAgeIndexByItem.set(left, rightIndex)
  }
}

function compareSideEffectQueueItems(left: QueuedAccountSideEffect, right: QueuedAccountSideEffect): number {
  return left.nextAttemptAtMs - right.nextAttemptAtMs || left.enqueuedAtMs - right.enqueuedAtMs
}

function compareSideEffectFailureAge(left: QueuedAccountSideEffect, right: QueuedAccountSideEffect): number {
  return left.enqueuedAtMs - right.enqueuedAtMs || left.nextAttemptAtMs - right.nextAttemptAtMs
}
