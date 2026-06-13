export class HeadIndexedQueue<T> {
  private items: T[] = []
  private headIndex = 0

  get length(): number {
    return this.items.length - this.headIndex
  }

  push(item: T): void {
    this.items.push(item)
  }

  unshift(item: T): void {
    if (this.headIndex > 0) {
      this.headIndex -= 1
      this.items[this.headIndex] = item
      return
    }
    this.items.unshift(item)
  }

  shift(): T | undefined {
    if (this.length <= 0) {
      return undefined
    }
    const item = this.items[this.headIndex]
    this.headIndex += 1
    this.compactConsumedItems()
    return item
  }

  at(index: number): T | undefined {
    if (index < 0 || index >= this.length) {
      return undefined
    }
    return this.items[this.headIndex + index]
  }

  set(index: number, item: T): void {
    if (index < 0 || index >= this.length) {
      return
    }
    this.items[this.headIndex + index] = item
  }

  findIndex(predicate: (item: T) => boolean): number {
    for (let index = 0; index < this.length; index += 1) {
      const item = this.at(index)
      if (item !== undefined && predicate(item)) {
        return index
      }
    }
    return -1
  }

  removeAt(index: number): T | undefined {
    if (index < 0 || index >= this.length) {
      return undefined
    }
    const physicalIndex = this.headIndex + index
    if (physicalIndex === this.headIndex) {
      return this.shift()
    }
    const [item] = this.items.splice(physicalIndex, 1)
    return item
  }

  private compactConsumedItems(): void {
    if (this.headIndex === 0) {
      return
    }
    if (this.headIndex >= this.items.length) {
      this.items = []
      this.headIndex = 0
      return
    }
    if (this.headIndex > 64 && this.headIndex * 2 > this.items.length) {
      this.items = this.items.slice(this.headIndex)
      this.headIndex = 0
    }
  }
}
