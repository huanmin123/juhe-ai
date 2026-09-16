import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { message } from '@/lib/antd'
import { copyTextToClipboard, writeTextToClipboard } from './clipboard'

vi.mock('@/lib/antd', () => ({
  message: {
    success: vi.fn(),
    error: vi.fn(),
    warning: vi.fn()
  }
}))

const originalExecCommand = document.execCommand

beforeEach(() => {
  vi.spyOn(console, 'error').mockImplementation(() => {})
})

afterEach(() => {
  vi.restoreAllMocks()
  document.execCommand = originalExecCommand
  Reflect.deleteProperty(window, 'isSecureContext')
})

describe('writeTextToClipboard', () => {
  it('clipboard.writeText 可用时直接写入', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    await expect(writeTextToClipboard('hello')).resolves.toBeUndefined()
    expect(writeText).toHaveBeenCalledWith('hello')
  })

  it('clipboard 不可用时抛出错误', async () => {
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: undefined }, configurable: true })
    await expect(writeTextToClipboard('hello')).rejects.toThrow('clipboard unavailable')
  })

  it('writeText 失败且无 execCommand 时抛出原始错误', async () => {
    const failure = new Error('denied')
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: vi.fn().mockRejectedValue(failure) }, configurable: true })
    await expect(writeTextToClipboard('hello')).rejects.toBe(failure)
  })

  it('writeText 失败时回退 execCommand 且成功后不抛错', async () => {
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: vi.fn().mockRejectedValue(new Error('denied')) }, configurable: true })
    document.execCommand = vi.fn(() => true)
    await expect(writeTextToClipboard('fallback text')).resolves.toBeUndefined()
    expect(document.execCommand).toHaveBeenCalledWith('copy')
  })
})

describe('copyTextToClipboard', () => {
  it('空字符串直接返回 false 且不提示', async () => {
    await expect(copyTextToClipboard('')).resolves.toBe(false)
    expect(message.success).not.toHaveBeenCalled()
    expect(message.error).not.toHaveBeenCalled()
  })

  it('复制成功提示成功文案', async () => {
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: vi.fn().mockResolvedValue(undefined) }, configurable: true })
    await expect(copyTextToClipboard('hello')).resolves.toBe(true)
    expect(message.success).toHaveBeenCalledWith('已复制')
    await expect(copyTextToClipboard('hello', '自定义文案')).resolves.toBe(true)
    expect(message.success).toHaveBeenCalledWith('自定义文案')
  })

  it('复制失败时按失败原因提示（页面有焦点 + 普通错误）', async () => {
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: vi.fn().mockRejectedValue(new Error('denied')) }, configurable: true })
    await expect(copyTextToClipboard('hello')).resolves.toBe(false)
    expect(message.error).toHaveBeenCalledWith('复制失败，请手动选择内容复制')
  })

  it('页面未获得焦点时提示回到页面重试', async () => {
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: vi.fn().mockRejectedValue(new Error('denied')) }, configurable: true })
    vi.spyOn(document, 'hasFocus').mockReturnValue(false)
    await expect(copyTextToClipboard('hello')).resolves.toBe(false)
    expect(message.error).toHaveBeenCalledWith('当前页面未获得焦点，复制失败，请回到页面后重试')
  })

  it('NotAllowedError 提示浏览器未允许复制', async () => {
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText: vi.fn().mockRejectedValue(new DOMException('denied', 'NotAllowedError')) },
      configurable: true
    })
    vi.spyOn(document, 'hasFocus').mockReturnValue(true)
    await expect(copyTextToClipboard('hello')).resolves.toBe(false)
    expect(message.error).toHaveBeenCalledWith('浏览器未允许本次复制，请直接点击复制按钮或手动选择内容复制')
  })

  it('clipboard 不可用且非安全上下文时提示 HTTPS 限制', async () => {
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: undefined }, configurable: true })
    Object.defineProperty(window, 'isSecureContext', { value: false, configurable: true })
    await expect(copyTextToClipboard('hello')).resolves.toBe(false)
    expect(message.error).toHaveBeenCalledWith('当前页面不是 HTTPS 或本机地址，浏览器已限制自动复制，请手动选择内容复制')
  })

  it('clipboard 不可用且安全上下文未知时提示环境不支持', async () => {
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: undefined }, configurable: true })
    await expect(copyTextToClipboard('hello')).resolves.toBe(false)
    expect(message.error).toHaveBeenCalledWith('当前环境不支持自动复制，请手动选择内容复制')
  })

  it('execCommand 回退成功时整体成功', async () => {
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: vi.fn().mockRejectedValue(new Error('denied')) }, configurable: true })
    document.execCommand = vi.fn(() => true)
    await expect(copyTextToClipboard('hello')).resolves.toBe(true)
    expect(message.success).toHaveBeenCalledWith('已复制')
  })
})
