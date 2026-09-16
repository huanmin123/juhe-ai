import type { ObjectDirective } from 'vue'

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { submitLockDirective } from './submitLock'

// Directive 是对象/函数指令的联合类型，这里按对象指令收窄以访问各生命周期钩子。
const directive = submitLockDirective as ObjectDirective<HTMLElement, unknown>

interface FakeBinding<V> {
  value: V
}

function mountDirective<V>(element: HTMLElement, value: V): { element: HTMLElement; binding: FakeBinding<V> } {
  const binding = { value }
  directive.mounted?.(element, binding as never, {} as never, {} as never)
  return { element, binding }
}

function dispatchClick(element: HTMLElement): MouseEvent {
  const event = new MouseEvent('click', { cancelable: true, bubbles: true })
  element.dispatchEvent(event)
  return event
}

let nowMs = 1_000_000

beforeEach(() => {
  nowMs = 1_000_000
  vi.spyOn(Date, 'now').mockImplementation(() => nowMs)
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('submitLock 指令', () => {
  it('pending 状态下按钮被禁用（disabled 按钮不再派发 click，与浏览器规范一致）', () => {
    const button = document.createElement('button')
    mountDirective(button, { pending: true })
    expect(button.disabled).toBe(true)
  })

  it('非 pending 首次点击放行', () => {
    const button = document.createElement('button')
    mountDirective(button, { pending: false })
    expect(button.disabled).toBe(false)
    const event = dispatchClick(button)
    expect(event.defaultPrevented).toBe(false)
  })

  it('冷却窗口内的重复点击被阻止', () => {
    const button = document.createElement('button')
    mountDirective(button, { pending: false })
    dispatchClick(button)
    nowMs += 100
    const second = dispatchClick(button)
    expect(second.defaultPrevented).toBe(true)
    nowMs += 600
    const third = dispatchClick(button)
    expect(third.defaultPrevented).toBe(false)
  })

  it('自定义 cooldownMs 生效', () => {
    const button = document.createElement('button')
    mountDirective(button, { pending: false, cooldownMs: 1000 })
    dispatchClick(button)
    nowMs += 600
    expect(dispatchClick(button).defaultPrevented).toBe(true)
    nowMs += 500
    expect(dispatchClick(button).defaultPrevented).toBe(false)
  })

  it('不同 key 之间互不共享冷却', () => {
    const button = document.createElement('button')
    mountDirective(button, { pending: false, key: 'save' })
    dispatchClick(button)
    nowMs += 100
    directive.unmounted?.(button, {} as never, {} as never, {} as never)
    mountDirective(button, { pending: false, key: 'delete' })
    expect(dispatchClick(button).defaultPrevented).toBe(false)
  })

  it('boolean 绑定等价于 pending 开关', () => {
    const button = document.createElement('button')
    const { binding } = mountDirective(button, true)
    expect(button.disabled).toBe(true)
    binding.value = false
    directive.updated?.(button, binding as never, {} as never, {} as never)
    expect(button.disabled).toBe(false)
    expect(dispatchClick(button).defaultPrevented).toBe(false)
  })

  it('updated 钩子随 pending 切换按钮禁用状态', () => {
    const button = document.createElement('button')
    const { binding } = mountDirective(button, { pending: false })
    expect(button.disabled).toBe(false)
    binding.value = { pending: true }
    directive.updated?.(button, binding as never, {} as never, {} as never)
    expect(button.disabled).toBe(true)
  })

  it('非原生按钮元素不设置 disabled 但仍阻止点击', () => {
    const div = document.createElement('div')
    mountDirective(div, { pending: true })
    expect((div as unknown as { disabled?: boolean }).disabled).toBeUndefined()
    expect(dispatchClick(div).defaultPrevented).toBe(true)
  })

  it('unmounted 清理监听与状态，后续点击不再被锁', () => {
    const button = document.createElement('button')
    mountDirective(button, { pending: true })
    directive.unmounted?.(button, {} as never, {} as never, {} as never)
    expect(dispatchClick(button).defaultPrevented).toBe(false)
    expect((button as unknown as Record<string, unknown>).__submitLockHandler__).toBeUndefined()
    expect((button as unknown as Record<string, unknown>).__submitLockLastAt__).toBeUndefined()
  })

  it('未传 key 时使用 default 冷却键', () => {
    const button = document.createElement('button')
    mountDirective(button, {})
    dispatchClick(button)
    nowMs += 100
    expect(dispatchClick(button).defaultPrevented).toBe(true)
  })
})
