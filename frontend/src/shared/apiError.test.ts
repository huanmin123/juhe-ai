import { describe, expect, it } from 'vitest'

import { extractApiErrorMessage, extractResponseErrorMessage, localizeTransportErrorMessage } from './apiError'

/** 构造 axios.isAxiosError 可识别的错误形状（不发真实请求）。 */
function axiosLikeError(message: string, data?: unknown): unknown {
  if (data === undefined) {
    return { isAxiosError: true, message }
  }
  return { isAxiosError: true, message, response: { data } }
}

describe('extractApiErrorMessage', () => {
  it('优先提取响应体 message', () => {
    expect(extractApiErrorMessage(axiosLikeError('Request failed with status code 400', { message: ' 参数名无效 ' }), '兜底'))
      .toBe('参数名无效')
  })

  it('响应体无 message 时提取嵌套 error.message', () => {
    expect(extractApiErrorMessage(axiosLikeError('boom', { error: { message: '上游错误' } }), '兜底'))
      .toBe('上游错误')
  })

  it('响应体无法提取时本地化传输错误消息', () => {
    expect(extractApiErrorMessage(axiosLikeError('Network Error'), '兜底'))
      .toBe('网络请求失败，请检查网络或稍后重试')
  })

  it('响应体无法提取且消息为状态码形式时使用兜底文案', () => {
    expect(extractApiErrorMessage(axiosLikeError('Request failed with status code 500'), '兜底')).toBe('兜底')
  })

  it('普通 Error 按传输消息处理', () => {
    expect(extractApiErrorMessage(new Error('自定义业务错误'), '兜底')).toBe('自定义业务错误')
  })

  it('普通 Error 空白消息使用兜底文案', () => {
    expect(extractApiErrorMessage(new Error('   '), '兜底')).toBe('兜底')
  })

  it('非错误值直接使用兜底文案', () => {
    expect(extractApiErrorMessage(undefined, '兜底')).toBe('兜底')
    expect(extractApiErrorMessage('字符串错误', '兜底')).toBe('兜底')
    expect(extractApiErrorMessage({ foo: 'bar' }, '兜底')).toBe('兜底')
  })

  it('非 axios 响应对象不参与提取', () => {
    // data 非对象（字符串/数组/null）时 extractResponseErrorMessage 返回 undefined
    expect(extractApiErrorMessage(axiosLikeError('Network Error', 'plain text'), '兜底'))
      .toBe('网络请求失败，请检查网络或稍后重试')
    expect(extractApiErrorMessage(axiosLikeError('Network Error', [{ message: 'x' }]), '兜底'))
      .toBe('网络请求失败，请检查网络或稍后重试')
  })
})

describe('extractResponseErrorMessage', () => {
  it('提取非空 message 并去除首尾空白', () => {
    expect(extractResponseErrorMessage({ message: '  错误  ' })).toBe('错误')
  })

  it('提取嵌套 error.message', () => {
    expect(extractResponseErrorMessage({ error: { message: '嵌套错误' } })).toBe('嵌套错误')
  })

  it('message 非字符串或空白时返回 undefined', () => {
    expect(extractResponseErrorMessage({ message: '   ' })).toBeUndefined()
    expect(extractResponseErrorMessage({ message: 123 })).toBeUndefined()
  })

  it('error 非对象时返回 undefined', () => {
    expect(extractResponseErrorMessage({ error: 'plain' })).toBeUndefined()
    expect(extractResponseErrorMessage({ error: null })).toBeUndefined()
  })

  it('非对象输入返回 undefined', () => {
    expect(extractResponseErrorMessage(undefined)).toBeUndefined()
    expect(extractResponseErrorMessage(null)).toBeUndefined()
    expect(extractResponseErrorMessage('text')).toBeUndefined()
  })
})

describe('localizeTransportErrorMessage', () => {
  it('常见英文网络错误统一本地化', () => {
    expect(localizeTransportErrorMessage('Network Error', '兜底')).toBe('网络请求失败，请检查网络或稍后重试')
    expect(localizeTransportErrorMessage('request aborted', '兜底')).toBe('网络请求失败，请检查网络或稍后重试')
    expect(localizeTransportErrorMessage('Load Failed', '兜底')).toBe('网络请求失败，请检查网络或稍后重试')
  })

  it('超时消息本地化', () => {
    expect(localizeTransportErrorMessage('timeout of 30000ms exceeded', '兜底')).toBe('请求超时，请稍后重试')
    expect(localizeTransportErrorMessage('Request timed out', '兜底')).toBe('请求超时，请稍后重试')
  })

  it('fetch 失败消息本地化', () => {
    expect(localizeTransportErrorMessage('Failed to fetch', '兜底')).toBe('网络请求失败，请稍后重试')
    expect(localizeTransportErrorMessage('fetch failed', '兜底')).toBe('网络请求失败，请稍后重试')
  })

  it('纯状态码消息使用兜底文案', () => {
    expect(localizeTransportErrorMessage('Request failed with status code 502', '兜底')).toBe('兜底')
  })

  it('其他消息原样返回', () => {
    expect(localizeTransportErrorMessage('something unusual', '兜底')).toBe('something unusual')
  })

  it('空白消息使用兜底文案', () => {
    expect(localizeTransportErrorMessage(undefined, '兜底')).toBe('兜底')
    expect(localizeTransportErrorMessage('', '兜底')).toBe('兜底')
    expect(localizeTransportErrorMessage('  ', '兜底')).toBe('兜底')
  })
})
