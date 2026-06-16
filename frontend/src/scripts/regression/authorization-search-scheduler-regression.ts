import { ref } from 'vue'

import { createAuthorizationSearchScheduler } from '@/views/authorizations/authorizationSearchScheduler'

type TimerCallback = () => void

const callbacks = new Map<number, TimerCallback>()
let nextTimerId = 1

const fakeWindow = {
  setTimeout: ((handler: TimerHandler, _timeout?: number, ...args: unknown[]) => {
    const timerId = nextTimerId++
    callbacks.set(timerId, () => {
      if (typeof handler === 'function') {
        handler(...args)
      }
    })
    return timerId
  }) as Window['setTimeout'],
  clearTimeout: ((timerId?: number) => {
    if (typeof timerId === 'number') {
      callbacks.delete(timerId)
    }
  }) as Window['clearTimeout']
}

const originalWindow = globalThis.window
Object.defineProperty(globalThis, 'window', {
  configurable: true,
  value: fakeWindow
})

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) {
    throw new Error(message)
  }
}

function runTimer(timerId: number): void {
  const callback = callbacks.get(timerId)
  callbacks.delete(timerId)
  callback?.()
}

function callbackCount(): number {
  return callbacks.size
}

function keywordValue(keyword: { value: string }): string {
  return keyword.value
}

try {
  const keyword = ref('')
  const loadedKeywords: string[] = []
  const scheduler = createAuthorizationSearchScheduler({
    delayMs: 250,
    keyword,
    load: (value) => {
      loadedKeywords.push(value)
    }
  })

  scheduler.schedule('alpha')
  assert(keywordValue(keyword) === 'alpha', 'schedule 应立即更新搜索关键字')
  assert(callbackCount() === 1, '第一次 schedule 应创建一个定时器')
  const firstTimerId = [...callbacks.keys()][0]

  scheduler.schedule('beta')
  assert(keywordValue(keyword) === 'beta', '连续 schedule 应保留最后一个搜索关键字')
  assert(!callbacks.has(firstTimerId), '连续 schedule 应清理前一个定时器')
  assert(callbackCount() === 1, '连续 schedule 后只应保留一个定时器')

  runTimer([...callbacks.keys()][0])
  assert(loadedKeywords.length === 1 && loadedKeywords[0] === 'beta', '定时器触发时应加载最后一个搜索关键字')
  assert(callbackCount() === 0, '定时器触发后应释放自身')

  scheduler.schedule('gamma')
  scheduler.clear()
  assert(callbackCount() === 0, 'clear 应取消挂起的搜索定时器')
  assert(loadedKeywords.length === 1, 'clear 后不应触发加载')

  console.log('授权搜索防抖调度器回归通过：连续搜索只保留最后一次，clear 可取消挂起加载')
} finally {
  if (typeof originalWindow === 'undefined') {
    delete (globalThis as { window?: Window }).window
  } else {
    Object.defineProperty(globalThis, 'window', {
      configurable: true,
      value: originalWindow
    })
  }
}
