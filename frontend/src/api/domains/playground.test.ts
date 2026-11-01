import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { PlaygroundStreamEvent } from '@/types/domain/playground'

import { http, setUnauthorizedHandler } from '../http'
import {
  consumePlaygroundSseResponse,
  parsePlaygroundSseData,
  PlaygroundStreamProtocolError,
  playgroundApi
} from './playground'

/** 构造可控的 SSE Response：chunks 逐段产出；getReader 复用同一 reader 便于断言 cancel/releaseLock。 */
function sseResponse(chunks: string[], options?: { status?: number; ok?: boolean; bodyText?: string }): Response {
  const encoder = new TextEncoder()
  let index = 0
  const status = options?.status ?? 200
  const reader = {
    read: async () => {
      if (index < chunks.length) {
        const value = encoder.encode(chunks[index])
        index += 1
        return { done: false, value }
      }
      return { done: true, value: undefined }
    },
    cancel: vi.fn(async () => undefined),
    releaseLock: vi.fn(() => undefined)
  }
  return {
    ok: options?.ok ?? (status >= 200 && status < 300),
    status,
    text: async () => options?.bodyText ?? '',
    clone: () => sseResponse(chunks, options),
    body: {
      getReader: () => reader
    }
  } as unknown as Response
}

function sseReader(response: Response): { cancel: ReturnType<typeof vi.fn>; releaseLock: ReturnType<typeof vi.fn> } {
  return (response.body as unknown as { getReader: () => { cancel: ReturnType<typeof vi.fn>; releaseLock: ReturnType<typeof vi.fn> } }).getReader()
}

const fetchMock = vi.fn()

beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('parsePlaygroundSseData 帧解析', () => {
  it('解析单 data 行的归一事件', () => {
    expect(parsePlaygroundSseData('data: {"type":"delta","text":"你好"}')).toEqual({ type: 'delta', text: '你好' })
    expect(parsePlaygroundSseData('data: {"type":"done","rounds":2}')).toEqual({ type: 'done', rounds: 2 })
  })

  it('多行 data: 按换行拼接后解析', () => {
    expect(parsePlaygroundSseData('data: {"type":"error",\ndata: "message":"上游 502"}')).toEqual({ type: 'error', message: '上游 502' })
  })

  it('无 data 行返回 null，非法 JSON 抛原错误，缺少 type 抛协议错误', () => {
    expect(parsePlaygroundSseData(': heartbeat')).toBeNull()
    expect(() => parsePlaygroundSseData('data: {oops')).toThrow()
    expect(() => parsePlaygroundSseData('data: {"message":"缺少 type"}')).toThrow(PlaygroundStreamProtocolError)
  })
})

describe('consumePlaygroundSseResponse 事件消费', () => {
  it('按顺序回调多事件（round/delta/tool_start/tool_end/usage/done）', async () => {
    const response = sseResponse([
      'data: {"type":"round","round":1}\n\n',
      'data: {"type":"delta","text":"正在查"}\n\n',
      'data: {"type":"tool_start","callId":"c1","name":"fetch_url","args":"{\\"url\\":\\"https://example.com\\"}"}\n\n',
      'data: {"type":"tool_end","callId":"c1","name":"fetch_url","ok":true,"summary":"示例域名"}\n\n',
      'data: {"type":"delta","text":"询结果"}\n\n',
      'data: {"type":"usage","promptTokens":10,"completionTokens":5,"totalTokens":15}\n\n',
      'data: {"type":"done","rounds":2}\n\n'
    ])
    const events: PlaygroundStreamEvent[] = []
    await consumePlaygroundSseResponse(response, '/playground/chat/stream', (event) => events.push(event))
    expect(events.map((event) => event.type)).toEqual(['round', 'delta', 'tool_start', 'tool_end', 'delta', 'usage', 'done'])
    // tool_start.args 是模型产出的 arguments JSON 字符串（后端 wire 原文），展示层自行解析。
    expect(events[2]).toEqual({ type: 'tool_start', callId: 'c1', name: 'fetch_url', args: '{"url":"https://example.com"}' })
    expect(events[5]).toEqual({ type: 'usage', promptTokens: 10, completionTokens: 5, totalTokens: 15 })
    expect(events[6]).toEqual({ type: 'done', rounds: 2 })
  })

  it('error 帧作为事件透出（不抛异常）', async () => {
    const response = sseResponse([
      'data: {"type":"delta","text":"部分输出"}\n\n',
      'data: {"type":"error","code":"playground_dispatch_failed","message":"上游失败"}\n\n'
    ])
    const events: PlaygroundStreamEvent[] = []
    await consumePlaygroundSseResponse(response, '/playground/chat/stream', (event) => events.push(event))
    expect(events[0]).toEqual({ type: 'delta', text: '部分输出' })
    expect(events[1]).toEqual({ type: 'error', code: 'playground_dispatch_failed', message: '上游失败' })
  })

  it('事件跨 chunk 分割时仍正确分帧', async () => {
    const response = sseResponse([
      'data: {"type":"delta","te',
      'xt":"第一段"}\n\ndata: {"type":"del',
      'ta","text":"第二段"}\n\n'
    ])
    const events: PlaygroundStreamEvent[] = []
    await consumePlaygroundSseResponse(response, '/playground/chat/stream', (event) => events.push(event))
    expect(events).toEqual([{ type: 'delta', text: '第一段' }, { type: 'delta', text: '第二段' }])
  })

  it('CRLF 分隔与结尾无空行的 done 帧也能解析', async () => {
    const response = sseResponse([
      'data: {"type":"delta","text":"你好"}\r\n\r\n',
      'data: {"type":"done","rounds":1}'
    ])
    const events: PlaygroundStreamEvent[] = []
    await consumePlaygroundSseResponse(response, '/playground/chat/stream', (event) => events.push(event))
    expect(events).toEqual([{ type: 'delta', text: '你好' }, { type: 'done', rounds: 1 }])
  })

  it('注释行被忽略，非法 JSON data 帧抛协议错误', async () => {
    const response = sseResponse([': ping\n\n', 'data: {broken}\n\n'])
    const events: PlaygroundStreamEvent[] = []
    await expect(consumePlaygroundSseResponse(response, '/playground/chat/stream', (event) => events.push(event)))
      .rejects.toThrow(PlaygroundStreamProtocolError)
    expect(events).toEqual([])
  })

  it('读流中断时 cancel 并 releaseLock；正常结束只 releaseLock', async () => {
    const cancel = vi.fn(async () => undefined)
    const releaseLock = vi.fn(() => undefined)
    const brokenResponse = {
      ok: true,
      status: 200,
      text: async () => '',
      clone: () => brokenResponse,
      body: {
        getReader: () => ({
          read: async () => {
            throw new TypeError('stream error')
          },
          cancel,
          releaseLock
        })
      }
    } as unknown as Response
    await expect(consumePlaygroundSseResponse(brokenResponse, '/playground/chat/stream', () => undefined))
      .rejects.toThrow('stream error')
    expect(cancel).toHaveBeenCalledTimes(1)
    expect(releaseLock).toHaveBeenCalledTimes(1)

    const okResponse = sseResponse(['data: {"type":"done","rounds":1}\n\n'])
    await consumePlaygroundSseResponse(okResponse, '/playground/chat/stream', () => undefined)
    const okReader = sseReader(okResponse)
    expect(okReader.cancel).not.toHaveBeenCalled()
    expect(okReader.releaseLock).toHaveBeenCalledTimes(1)
  })

  it('HTTP 非 2xx 抛 PlaygroundStreamHttpError（含扁平 code 与 message）', async () => {
    const response = sseResponse([], { status: 502, bodyText: '{"code":"playground_dispatch_failed","message":"上游 502"}' })
    await expect(consumePlaygroundSseResponse(response, '/playground/chat/stream', () => undefined))
      .rejects.toMatchObject({ name: 'PlaygroundStreamHttpError', status: 502, code: 'playground_dispatch_failed', message: '上游 502' })
  })

  it('HTTP 401 非 /auth/ 路径触发 unauthorizedHandler', async () => {
    const unauthorizedHandler = vi.fn()
    setUnauthorizedHandler(unauthorizedHandler)
    const response = sseResponse([], { status: 401, bodyText: '' })
    await expect(consumePlaygroundSseResponse(response, '/playground/chat/stream', () => undefined))
      .rejects.toThrow('请求失败：HTTP 401')
    expect(unauthorizedHandler).toHaveBeenCalledTimes(1)
  })
})

describe('chatStream 请求形状', () => {
  it('POST JSON、携带凭据、SSE 头并恒传 stream:true', async () => {
    fetchMock.mockResolvedValue(sseResponse(['data: {"type":"done","rounds":1}\n\n']))
    const onEvent = vi.fn()
    const controller = new AbortController()
    await playgroundApi.chatStream({
      accountId: 'acc_1',
      modelId: 'gpt-5.4',
      messages: [{ role: 'user', content: '你好' }],
      system: '你是助手',
      temperature: 0.7,
      maxTokens: null,
      stream: true,
      topP: 1,
      frequencyPenalty: 0,
      presencePenalty: 0,
      signal: controller.signal,
      onEvent
    })
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(String(url)).toContain('/playground/chat/stream')
    expect(init.method).toBe('POST')
    expect(init.credentials).toBe('include')
    expect(init.headers).toEqual({ accept: 'text/event-stream', 'content-type': 'application/json' })
    expect(init.signal).toBe(controller.signal)
    expect(JSON.parse(String(init.body))).toEqual({
      accountId: 'acc_1',
      modelId: 'gpt-5.4',
      messages: [{ role: 'user', content: '你好' }],
      system: '你是助手',
      temperature: 0.7,
      maxTokens: null,
      stream: true,
      topP: 1,
      frequencyPenalty: 0,
      presencePenalty: 0
    })
    expect(onEvent).toHaveBeenCalledWith({ type: 'done', rounds: 1 })
  })

  it('HTTP 400 校验错误透出 code 与状态码', async () => {
    fetchMock.mockResolvedValue(sseResponse([], { status: 400, bodyText: '{"code":"playground_model_not_available","message":"模型不可用"}' }))
    await expect(playgroundApi.chatStream({
      accountId: 'acc_1', modelId: 'bad', messages: [{ role: 'user', content: 'hi' }], stream: true,
      onEvent: () => undefined
    })).rejects.toMatchObject({ code: 'playground_model_not_available', status: 400 })
  })
})

interface CapturedRequest {
  method: string
  url: string
  params?: unknown
  data?: unknown
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
      data: config.data
    })
    return { data: { data: responseData }, status: 200, statusText: 'OK', headers: {}, config }
  }
}

describe('playgroundApi 请求形状', () => {
  beforeEach(() => installCaptureAdapter())
  afterEach(() => {
    http.defaults.adapter = originalAdapter
  })

  it('accounts 返回解包后的账户数组', async () => {
    const accounts = [{ accountId: 'acc_1', accountName: '演示', providerCode: 'gpt', models: [{ modelId: 'gpt-5.4', kind: 'chat', functionCalling: true }] }]
    installCaptureAdapter({ accounts })
    await expect(playgroundApi.accounts()).resolves.toEqual(accounts)
    expect(requests).toEqual([{ method: 'GET', url: '/playground/accounts', params: undefined, data: undefined }])
  })

  it('images POST /playground/images 并返回暂存引用（url 归一为同域绝对地址）', async () => {
    installCaptureAdapter({ images: [{ id: 'img_1', url: '/playground/artifacts/img_1/content' }], model: 'gpt-image-2' })
    await expect(playgroundApi.images({ accountId: 'acc_1', modelId: 'gpt-image-2', prompt: '一只猫' })).resolves.toEqual({
      images: [{ id: 'img_1', url: '/__aisys__/api/playground/artifacts/img_1/content' }],
      model: 'gpt-image-2'
    })
    expect(requests).toEqual([{
      method: 'POST',
      url: '/playground/images',
      params: undefined,
      data: JSON.stringify({ accountId: 'acc_1', modelId: 'gpt-image-2', prompt: '一只猫' })
    }])
  })

  it('artifacts GET /playground/artifacts 并返回暂存清单（url 归一，video 引用不动）', async () => {
    installCaptureAdapter({
      images: [{ id: 'img_1', url: '/playground/artifacts/img_1/content', createdAt: 1761840000 }],
      speech: { id: 'aud_1', url: '/playground/artifacts/aud_1/content', mimeType: 'audio/mpeg', createdAt: 1761840000 },
      video: { jobId: 'job_1', accountId: 'acc_1', modelId: 'video-1', status: 'running', createdAt: 1761840000 }
    })
    await expect(playgroundApi.artifacts()).resolves.toEqual({
      images: [{ id: 'img_1', url: '/__aisys__/api/playground/artifacts/img_1/content', createdAt: 1761840000 }],
      speech: { id: 'aud_1', url: '/__aisys__/api/playground/artifacts/aud_1/content', mimeType: 'audio/mpeg', createdAt: 1761840000 },
      video: { jobId: 'job_1', accountId: 'acc_1', modelId: 'video-1', status: 'running', createdAt: 1761840000 }
    })
    expect(requests).toEqual([{ method: 'GET', url: '/playground/artifacts', params: undefined, data: undefined }])
  })

  it('videosCreate POST /playground/videos；videosPoll GET 带 accountId 查询参数', async () => {
    installCaptureAdapter({ jobId: 'job_1' })
    await expect(playgroundApi.videosCreate({ accountId: 'acc_1', modelId: 'video-1', prompt: '海浪' })).resolves.toEqual({ jobId: 'job_1' })
    expect(requests).toEqual([
      { method: 'POST', url: '/playground/videos', params: undefined, data: JSON.stringify({ accountId: 'acc_1', modelId: 'video-1', prompt: '海浪' }) }
    ])
    installCaptureAdapter({ status: 'running', progress: 40 })
    await expect(playgroundApi.videosPoll('job_1', 'acc_1')).resolves.toEqual({ status: 'running', progress: 40 })
    expect(requests).toEqual([
      { method: 'GET', url: '/playground/videos/job_1', params: { accountId: 'acc_1' }, data: undefined }
    ])
    // error 是归一对象 {code?, message?}（后端 videosPollError 原样透出），非字符串。
    installCaptureAdapter({ status: 'failed', error: { code: 'upstream_failed', message: '上游任务失败' } })
    await expect(playgroundApi.videosPoll('job_1', 'acc_1')).resolves.toEqual({
      status: 'failed',
      error: { code: 'upstream_failed', message: '上游任务失败' }
    })
  })

  it('videosContent GET content 端点并转 blob URL', async () => {
    const blob = new Blob(['video-bytes'], { type: 'video/mp4' })
    fetchMock.mockResolvedValue({ ok: true, status: 200, blob: async () => blob } as unknown as Response)
    const url = await playgroundApi.videosContent('job_1', 'acc_1')
    expect(url).toMatch(/^blob:/)
    URL.revokeObjectURL(url)
    const [requestUrl, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(String(requestUrl)).toContain('/playground/videos/job_1/content?accountId=acc_1')
    expect(init.credentials).toBe('include')
  })

  it('speech POST /playground/speech 并返回暂存引用（url 归一为同域绝对地址）', async () => {
    installCaptureAdapter({ id: 'aud_1', url: '/playground/artifacts/aud_1/content', mimeType: 'audio/mpeg' })
    await expect(playgroundApi.speech({ accountId: 'acc_1', modelId: 'speech-1', input: '你好', voice: 'alloy' })).resolves.toEqual({
      id: 'aud_1',
      url: '/__aisys__/api/playground/artifacts/aud_1/content',
      mimeType: 'audio/mpeg'
    })
    expect(requests).toEqual([{
      method: 'POST',
      url: '/playground/speech',
      params: undefined,
      data: JSON.stringify({ accountId: 'acc_1', modelId: 'speech-1', input: '你好', voice: 'alloy' })
    }])
  })
})
