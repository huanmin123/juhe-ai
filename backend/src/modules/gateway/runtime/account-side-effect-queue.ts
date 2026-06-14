import type { DbServiceOperation } from '../../db-service/db-service-types.js'

export type AccountErrorHandlingOperation = Extract<DbServiceOperation, { type: 'apply_account_error_handling' }>
export type StreamFailureOperation = Extract<DbServiceOperation, { type: 'record_account_stream_failure' }>
export type AccountSideEffectOperation = AccountErrorHandlingOperation | StreamFailureOperation

export interface QueuedAccountSideEffect {
  operation: AccountSideEffectOperation
  attempts: number
  enqueuedAtMs: number
  nextAttemptAtMs: number
  expiresAtMs: number
}

export class AccountSideEffectQueue {
  private readonly items: QueuedAccountSideEffect[] = []

  get length(): number {
    return this.items.length
  }

  push(item: QueuedAccountSideEffect): void {
    this.items.push(item)
    this.heapifyUp(this.items.length - 1)
  }

  peek(): QueuedAccountSideEffect | undefined {
    return this.items[0]
  }

  pop(): QueuedAccountSideEffect | undefined {
    if (this.items.length === 0) return undefined
    const first = this.items[0]
    const last = this.items.pop()
    if (last && this.items.length > 0) {
      this.items[0] = last
      this.heapifyDown(0)
    }
    return first
  }

  clear(): void {
    this.items.splice(0)
  }

  findIndex(predicate: (item: QueuedAccountSideEffect) => boolean): number {
    return this.items.findIndex(predicate)
  }

  replaceAt(index: number, item: QueuedAccountSideEffect): void {
    if (index < 0 || index >= this.items.length) {
      return
    }
    this.items[index] = item
    this.heapify()
  }

  removeWhere(predicate: (item: QueuedAccountSideEffect) => boolean): number {
    let writeIndex = 0
    let removed = 0
    for (let index = 0; index < this.items.length; index += 1) {
      const item = this.items[index]
      if (predicate(item)) {
        removed += 1
        continue
      }
      this.items[writeIndex] = item
      writeIndex += 1
    }
    if (removed === 0) {
      return 0
    }
    this.items.length = writeIndex
    this.heapify()
    return removed
  }

  private heapify(): void {
    for (let index = Math.floor(this.items.length / 2) - 1; index >= 0; index -= 1) {
      this.heapifyDown(index)
    }
  }

  private heapifyUp(startIndex: number): void {
    let index = startIndex
    while (index > 0) {
      const parentIndex = Math.floor((index - 1) / 2)
      if (compareSideEffectQueueItems(this.items[parentIndex], this.items[index]) <= 0) {
        return
      }
      this.swap(index, parentIndex)
      index = parentIndex
    }
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
    this.items[leftIndex] = this.items[rightIndex]
    this.items[rightIndex] = left
  }
}

function compareSideEffectQueueItems(left: QueuedAccountSideEffect, right: QueuedAccountSideEffect): number {
  return left.nextAttemptAtMs - right.nextAttemptAtMs || left.enqueuedAtMs - right.enqueuedAtMs
}
