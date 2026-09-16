import axios, { AxiosError } from 'axios'
import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  apiUrl,
  http,
  normalizeApiBaseUrl,
  noTimeout,
  queryString,
  readFetchErrorMessage,
  setMustChangePasswordHandler,
  setUnauthorizedHandler,
  unwrap
} from './http'

interface CapturedRequest {
  method: string
  url: string
  params?: unknown
  data?: unknown
  timeout?: unknown
  signal?: unknown
}

const originalAdapter = http.defaults.adapter
let requests: CapturedRequest[] = []
let responseData: unknown = {}

/** 替换 axios adapter：捕获请求形状并返回可配置的 `{ data: { data } }` 响应。 */
function installCaptureAdapter(data: unknown = {}): void {
  responseData = data
  requests = []
  http.defaults.adapter = async (config: InternalAxiosRequestConfig) => {
    requests.push({
      method: String(config.method ?? '').toUpperCase(),
      url: String(config.url ?? ''),
      params: config.params,
      data: config.data,
      timeout: config.timeout,
      signal: config.signal
    })
    return { data: { data: responseData }, status: 200, statusText: 'OK', headers: {}, config }
  }
}

/** 替换 axios adapter：直接以给定错误 reject，用于触发响应拦截器错误分支。 */
function installRejectAdapter(error: unknown): void {
  http.defaults.adapter = () => Promise.reject(error)
}

function axiosError(status: number, url: string | undefined, data: unknown): AxiosError {
  const config = { url } as InternalAxiosRequestConfig
  return new AxiosError('Request failed', undefined, config, undefined, {
    status,
    data,
    statusText: '',
    headers: {},
    config
  })
}

function fakeResponse(status: number, body: string): Response {
  return { status, text: async () => body } as unknown as Response
}

const unauthorizedHandler = vi.fn()
const mustChangePasswordHandler = vi.fn()

beforeEach(() => {
  installCaptureAdapter()
  unauthorizedHandler.mockClear()
  mustChangePasswordHandler.mockClear()
  setUnauthorizedHandler(unauthorizedHandler)
  setMustChangePasswordHandler(mustChangePasswordHandler)
})

afterEach(() => {
  http.defaults.adapter = originalAdapter
  // 模块级 handler 是单例状态，测试后重置为 no-op 避免泄漏
  setUnauthorizedHandler(() => {})
  setMustChangePasswordHandler(() => {})
})

describe('http axios 实例配置', () => {
  it('默认实例使用网关代理前缀、15s 超时并携带凭据', () => {
    expect(http.defaults.baseURL).toBe('/__aisys__/api')
    expect(http.defaults.timeout).toBe(15000)
    expect(http.defaults.withCredentials).toBe(true)
  })
})

describe('normalizeApiBaseUrl', () => {
  it('空值与纯空白回退到默认网关前缀', () => {
    expect(normalizeApiBaseUrl(undefined)).toBe('/__aisys__/api')
    expect(normalizeApiBaseUrl('')).toBe('/__aisys__/api')
    expect(normalizeApiBaseUrl('   ')).toBe('/__aisys__/api')
  })

  it('去除结尾斜杠（含多个）', () => {
    expect(normalizeApiBaseUrl('/prefix/')).toBe('/prefix')
    expect(normalizeApiBaseUrl('/prefix///')).toBe('/prefix')
    expect(normalizeApiBaseUrl('https://api.example.com/v1/')).toBe('https://api.example.com/v1')
  })

  it('去除后为空时回退默认前缀', () => {
    expect(normalizeApiBaseUrl('///')).toBe('/__aisys__/api')
    expect(normalizeApiBaseUrl('/')).toBe('/__aisys__/api')
  })

  it('保留无尾斜杠的普通值并修剪两端空白', () => {
    expect(normalizeApiBaseUrl('  https://api.example.com  ')).toBe('https://api.example.com')
  })
})

describe('apiUrl', () => {
  it('相对 path 会补斜杠并拼接到 baseURL', () => {
    expect(apiUrl('accounts')).toBe(`${window.location.origin}/__aisys__/api/accounts`)
    expect(apiUrl('/accounts/1')).toBe(`${window.location.origin}/__aisys__/api/accounts/1`)
  })

  it('仅设置 truthy 查询参数', () => {
    const url = apiUrl('/x', { keep: '1', dropEmpty: '', dropUndefined: undefined })
    expect(url).toBe(`${window.location.origin}/__aisys__/api/x?keep=1`)
  })

  it('查询参数按 URLSearchParams 规则编码', () => {
    const url = apiUrl('/x', { q: 'a b/c' })
    expect(url).toBe(`${window.location.origin}/__aisys__/api/x?q=a+b%2Fc`)
  })

  it('baseURL 缺失时回退默认网关前缀', () => {
    const original = http.defaults.baseURL
    http.defaults.baseURL = undefined
    try {
      expect(apiUrl('/accounts')).toBe(`${window.location.origin}/__aisys__/api/accounts`)
    } finally {
      http.defaults.baseURL = original
    }
  })
})

describe('unwrap', () => {
  it('解包响应中的 data.data 字段', async () => {
    installCaptureAdapter({ id: 'a-1', name: '账户' })
    await expect(unwrap<{ id: string }>(http.get('/accounts'))).resolves.toEqual({ id: 'a-1', name: '账户' })
    expect(requests).toHaveLength(1)
  })

  it('请求失败时保留原始拒绝原因', async () => {
    const failure = new Error('网络中断')
    installRejectAdapter(failure)
    await expect(unwrap(http.get('/accounts'))).rejects.toBe(failure)
  })
})

describe('noTimeout 常量', () => {
  it('表示不设超时', () => {
    expect(noTimeout).toEqual({ timeout: 0 })
  })
})

describe('queryString', () => {
  it('无参数或空对象返回空字符串', () => {
    expect(queryString()).toBe('')
    expect(queryString({})).toBe('')
  })

  it('跳过 undefined/null/空字符串并字符串化其余值', () => {
    expect(queryString({ page: 1, keyword: 'k', dropU: undefined, dropN: null, dropE: '', flag: false }))
      .toBe('?page=1&keyword=k&flag=false')
  })
})

describe('readFetchErrorMessage', () => {
  it('空响应体返回带状态码的通用失败信息', async () => {
    await expect(readFetchErrorMessage(fakeResponse(500, '   '), '/x')).resolves.toBe('请求失败：HTTP 500')
  })

  it('优先提取 JSON 响应体的 message 字段', async () => {
    await expect(readFetchErrorMessage(fakeResponse(500, '{"message":"服务不可用"}'), '/x')).resolves.toBe('服务不可用')
  })

  it('提取嵌套 error.message 字段', async () => {
    await expect(readFetchErrorMessage(fakeResponse(400, '{"error":{"message":"参数错误"}}'), '/x')).resolves.toBe('参数错误')
  })

  it('JSON 无可识别 message 时回退原始文本', async () => {
    await expect(readFetchErrorMessage(fakeResponse(400, '{"code":"E1"}'), '/x')).resolves.toBe('{"code":"E1"}')
  })

  it('非 JSON 文本原样返回，常见网络错误文案本地化', async () => {
    await expect(readFetchErrorMessage(fakeResponse(502, 'bad gateway text'), '/x')).resolves.toBe('bad gateway text')
    await expect(readFetchErrorMessage(fakeResponse(0, 'Network Error'), '/x')).resolves.toBe('网络请求失败，请检查网络或稍后重试')
  })

  it('401 且路径非 /auth/ 前缀时触发 unauthorizedHandler', async () => {
    await readFetchErrorMessage(fakeResponse(401, ''), '/accounts')
    expect(unauthorizedHandler).toHaveBeenCalledTimes(1)
  })

  it('401 且路径为 /auth/ 前缀时不触发 unauthorizedHandler', async () => {
    await readFetchErrorMessage(fakeResponse(401, ''), '/auth/login')
    expect(unauthorizedHandler).not.toHaveBeenCalled()
  })

  it('403 且响应体 code 为 must_change_password 时触发 mustChangePasswordHandler', async () => {
    await readFetchErrorMessage(fakeResponse(403, '{"code":"must_change_password"}'), '/accounts')
    expect(mustChangePasswordHandler).toHaveBeenCalledTimes(1)
  })

  it('403 其他响应体不触发 mustChangePasswordHandler', async () => {
    await readFetchErrorMessage(fakeResponse(403, '{"code":"forbidden"}'), '/accounts')
    await readFetchErrorMessage(fakeResponse(403, 'not-json'), '/accounts')
    expect(mustChangePasswordHandler).not.toHaveBeenCalled()
  })
})

describe('响应拦截器', () => {
  it('401 且非 /auth/ 前缀路径触发 unauthorizedHandler 并保留原始拒绝', async () => {
    const error = axiosError(401, '/accounts', {})
    installRejectAdapter(error)
    await expect(http.get('/accounts')).rejects.toBe(error)
    expect(unauthorizedHandler).toHaveBeenCalledTimes(1)
    expect(mustChangePasswordHandler).not.toHaveBeenCalled()
  })

  it('401 且为 /auth/ 前缀路径不触发 unauthorizedHandler', async () => {
    const error = axiosError(401, '/auth/login', {})
    installRejectAdapter(error)
    await expect(http.post('/auth/login', {})).rejects.toBe(error)
    expect(unauthorizedHandler).not.toHaveBeenCalled()
  })

  it('401 且 url 缺失时按需通知', async () => {
    const error = axiosError(401, undefined, {})
    installRejectAdapter(error)
    await expect(http.get('/x')).rejects.toBe(error)
    expect(unauthorizedHandler).toHaveBeenCalledTimes(1)
  })

  it('403 且响应体 code 为 must_change_password 触发 mustChangePasswordHandler', async () => {
    const error = axiosError(403, '/accounts', { code: 'must_change_password' })
    installRejectAdapter(error)
    await expect(http.get('/accounts')).rejects.toBe(error)
    expect(mustChangePasswordHandler).toHaveBeenCalledTimes(1)
    expect(unauthorizedHandler).not.toHaveBeenCalled()
  })

  it('403 其他响应体不触发任何 handler', async () => {
    const error = axiosError(403, '/accounts', { code: 'forbidden' })
    installRejectAdapter(error)
    await expect(http.get('/accounts')).rejects.toBe(error)
    expect(unauthorizedHandler).not.toHaveBeenCalled()
    expect(mustChangePasswordHandler).not.toHaveBeenCalled()
  })

  it('非 axios 错误不触发 handler 且原样拒绝', async () => {
    const error = new Error('boom')
    installRejectAdapter(error)
    await expect(http.get('/accounts')).rejects.toBe(error)
    expect(unauthorizedHandler).not.toHaveBeenCalled()
    expect(mustChangePasswordHandler).not.toHaveBeenCalled()
  })

  it('其他状态码（如 500）不触发任何 handler', async () => {
    const error = axiosError(500, '/accounts', {})
    installRejectAdapter(error)
    await expect(http.get('/accounts')).rejects.toBe(error)
    expect(unauthorizedHandler).not.toHaveBeenCalled()
    expect(mustChangePasswordHandler).not.toHaveBeenCalled()
  })

  it('未注入 handler 时错误分支静默透传', async () => {
    setUnauthorizedHandler(() => {})
    setMustChangePasswordHandler(() => {})
    const error = axiosError(401, '/accounts', {})
    installRejectAdapter(error)
    await expect(http.get('/accounts')).rejects.toBe(error)
  })

  it('成功响应正常返回且不触发任何 handler', async () => {
    installCaptureAdapter({ ok: true })
    await expect(http.get('/accounts')).resolves.toMatchObject({ data: { data: { ok: true } } })
    expect(unauthorizedHandler).not.toHaveBeenCalled()
    expect(mustChangePasswordHandler).not.toHaveBeenCalled()
  })

  it('axios.isAxiosError 可识别拦截器透传的错误', async () => {
    const error = axiosError(401, '/accounts', {})
    installRejectAdapter(error)
    const caught = await http.get('/accounts').catch((reason: unknown) => reason)
    expect(axios.isAxiosError(caught)).toBe(true)
  })
})
