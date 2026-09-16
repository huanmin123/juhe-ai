import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { http } from '../http'
import { attachChatStream, chatApi, chatAssetContentUrl, ChatStreamHttpError, ChatStreamProtocolError, streamChatMessage } from './chat'

interface CapturedRequest {
  method: string
  url: string
  params?: unknown
  data?: unknown
  timeout?: unknown
  signal?: unknown
  onUploadProgress?: (event: { loaded: number; total?: number }) => void
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
      signal: config.signal,
      onUploadProgress: config.onUploadProgress as CapturedRequest['onUploadProgress']
    })
    return { data: { data: responseData }, status: 200, statusText: 'OK', headers: {}, config }
  }
}

function requestShapes(): Array<[string, string]> {
  return requests.map((request) => [request.method, request.url])
}

function payloadOf(request: CapturedRequest): Record<string, unknown> {
  return JSON.parse(String(request.data)) as Record<string, unknown>
}

/** 构造可控的 SSE fetch Response。 */
function sseResponse(chunks: string[], options?: { status?: number; ok?: boolean; bodyText?: string }): Response {
  const encoder = new TextEncoder()
  let index = 0
  const response: unknown = {
    ok: options?.ok ?? ((options?.status ?? 200) >= 200 && (options?.status ?? 200) < 300),
    status: options?.status ?? 200,
    text: async () => options?.bodyText ?? '',
    clone: () => sseResponse(chunks, options),
    body: {
      getReader: () => ({
        read: async () => {
          if (index < chunks.length) {
            const value = encoder.encode(chunks[index])
            index += 1
            return { done: false, value }
          }
          return { done: true, value: undefined }
        },
        cancel: async () => undefined,
        releaseLock: () => undefined
      })
    }
  }
  return response as Response
}

const fetchMock = vi.fn()

beforeEach(() => {
  installCaptureAdapter()
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  http.defaults.adapter = originalAdapter
  vi.unstubAllGlobals()
})

describe('chatApi 请求形状', () => {
  it('会话与消息方法发出正确的 method 与 URL', async () => {
    await chatApi.getImagePolicy()
    await chatApi.listConversations()
    await chatApi.createConversation()
    await chatApi.getConversation('conv-1')
    await chatApi.listMessages('conv-1')
    await chatApi.getConversationSync('conv-1')
    await chatApi.getSubmissionStatus('conv-1', 'cm-1')
    await chatApi.listModels('conv-1')
    await chatApi.getModelCapabilities('conv-1', 'gpt-4o')
    await chatApi.getContextStatus('conv-1')
    await chatApi.compactContext('conv-1', { model: 'gpt-4o' })
    await chatApi.deleteAsset('conv-1', 'asset-1')
    await chatApi.updateConversation('conv-1', { title: '新标题' })
    await chatApi.stop('conv-1', { clientMessageId: 'cm-1' })
    await chatApi.clearConversation('conv-1')
    await chatApi.deleteConversation('conv-1')
    expect(requestShapes()).toEqual([
      ['GET', '/my-chat/image-policy'],
      ['GET', '/my-chat/conversations'],
      ['POST', '/my-chat/conversations'],
      ['GET', '/my-chat/conversations/conv-1'],
      ['GET', '/my-chat/conversations/conv-1/messages'],
      ['GET', '/my-chat/conversations/conv-1/sync'],
      ['GET', '/my-chat/conversations/conv-1/submissions/cm-1'],
      ['GET', '/my-chat/conversations/conv-1/models'],
      ['GET', '/my-chat/conversations/conv-1/models/gpt-4o'],
      ['GET', '/my-chat/conversations/conv-1/context-status'],
      ['POST', '/my-chat/conversations/conv-1/context/compactions'],
      ['DELETE', '/my-chat/conversations/conv-1/assets/asset-1'],
      ['PATCH', '/my-chat/conversations/conv-1'],
      ['POST', '/my-chat/conversations/conv-1/stop'],
      ['POST', '/my-chat/conversations/conv-1/clear'],
      ['DELETE', '/my-chat/conversations/conv-1']
    ])
  })

  it('conversationId 等动态路径段逐段 encodeURIComponent 编码', async () => {
    const rawId = 'conv/a?b'
    await chatApi.getConversation(rawId)
    expect(requests[0].url).toBe('/my-chat/conversations/conv%2Fa%3Fb')
    await chatApi.getModelCapabilities('conv-1', 'model/x')
    expect(requests[1].url).toBe('/my-chat/conversations/conv-1/models/model%2Fx')
    await chatApi.deleteAsset('conv-1', 'asset/1')
    expect(requests[2].url).toBe('/my-chat/conversations/conv-1/assets/asset%2F1')
  })

  it('createConversation 无 apiKeyId 时发送空 body，有则携带', async () => {
    await chatApi.createConversation()
    expect(payloadOf(requests[0])).toEqual({})
    await chatApi.createConversation('key-1')
    expect(payloadOf(requests[1])).toEqual({ apiKeyId: 'key-1' })
  })

  it('getConversationSync 默认 knownRevision=0，传入时使用实际值', async () => {
    await chatApi.getConversationSync('conv-1')
    expect(requests[0].params).toEqual({ knownRevision: 0 })
    await chatApi.getConversationSync('conv-1', 7)
    expect(requests[1].params).toEqual({ knownRevision: 7 })
  })

  it('listModels 透传 signal', async () => {
    const controller = new AbortController()
    await chatApi.listModels('conv-1', { signal: controller.signal })
    expect(requests[0].signal).toBe(controller.signal)
  })

  it('uploadAsset 以 FormData 上传文件并禁用超时', async () => {
    const file = new File(['fake-image'], 'a.png', { type: 'image/png' })
    await chatApi.uploadAsset('conv-1', file)
    expect(requests[0].method).toBe('POST')
    expect(requests[0].url).toBe('/my-chat/conversations/conv-1/assets')
    expect(requests[0].data).toBeInstanceOf(FormData)
    const uploaded = (requests[0].data as FormData).get('file')
    expect(uploaded).toBeInstanceOf(File)
    expect((uploaded as File).name).toBe('a.png')
    expect(requests[0].timeout).toBe(0)
  })

  it('uploadAsset 的进度回调按比例计算并夹在 0-100', async () => {
    const file = new File(['x'], 'a.png')
    const onProgress = vi.fn()
    await chatApi.uploadAsset('conv-1', file, { onProgress })
    const progress = requests[0].onUploadProgress
    expect(progress).toBeTypeOf('function')
    progress?.({ loaded: 50, total: 100 })
    progress?.({ loaded: 150, total: 100 })
    progress?.({ loaded: 10, total: 0 })
    progress?.({ loaded: 10 })
    expect(onProgress).toHaveBeenNthCalledWith(1, 50)
    expect(onProgress).toHaveBeenNthCalledWith(2, 100)
    expect(onProgress).toHaveBeenCalledTimes(2)
  })

  it('chatAssetContentUrl 生成内容下载地址并区分变体', () => {
    expect(chatAssetContentUrl('conv/1', 'asset/1')).toBe(
      `${window.location.origin}/__aisys__/api/my-chat/conversations/conv%2F1/assets/asset%2F1/content?variant=original`
    )
    expect(chatAssetContentUrl('conv-1', 'asset-1', 'preview')).toBe(
      `${window.location.origin}/__aisys__/api/my-chat/conversations/conv-1/assets/asset-1/content?variant=preview`
    )
  })
})

describe('streamChatMessage', () => {
  it('POST SSE 请求并按事件回调 onEvent/onActivity', async () => {
    fetchMock.mockResolvedValue(sseResponse([
      ': ping\n\n',
      'event: message.delta\ndata: {"messageId":"m-1","delta":"你好","eventVersion":1}\n\n',
      'event: message.completed\ndata: {"messageId":"m-1","eventVersion":2}\n\n'
    ]))
    const onEvent = vi.fn()
    const onActivity = vi.fn()
    await streamChatMessage({
      conversationId: 'conv/1',
      clientMessageId: 'cm-1',
      content: '问题',
      model: 'gpt-4o',
      onEvent,
      onActivity
    })
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe(`${window.location.origin}/__aisys__/api/my-chat/conversations/conv%2F1/stream`)
    expect(init.method).toBe('POST')
    expect(init.credentials).toBe('include')
    expect(init.headers).toEqual({ 'content-type': 'application/json', accept: 'text/event-stream' })
    expect(JSON.parse(String(init.body))).toEqual({ clientMessageId: 'cm-1', content: '问题', model: 'gpt-4o' })
    expect(onEvent).toHaveBeenCalledTimes(2)
    expect(onEvent).toHaveBeenNthCalledWith(1, { type: 'message.delta', data: { messageId: 'm-1', delta: '你好', eventVersion: 1 } })
    expect(onEvent).toHaveBeenNthCalledWith(2, { type: 'message.completed', data: { messageId: 'm-1', eventVersion: 2 } })
    expect(onActivity.mock.calls.length).toBeGreaterThan(0)
  })

  it('透传 signal 与可选生成参数', async () => {
    fetchMock.mockResolvedValue(sseResponse(['event: message.completed\ndata: {"messageId":"m-1","eventVersion":1}\n\n']))
    const controller = new AbortController()
    await streamChatMessage({
      conversationId: 'conv-1',
      clientMessageId: 'cm-1',
      content: 'x',
      model: 'gpt-4o',
      reasoningEffort: 'low',
      replaceTurnId: 'turn-1',
      signal: controller.signal,
      onEvent: vi.fn()
    })
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(init.signal).toBe(controller.signal)
    expect(JSON.parse(String(init.body))).toEqual({
      clientMessageId: 'cm-1',
      replaceTurnId: 'turn-1',
      content: 'x',
      model: 'gpt-4o',
      reasoningEffort: 'low'
    })
  })

  it('HTTP 错误抛出 ChatStreamHttpError（含状态码与业务 code）', async () => {
    fetchMock.mockResolvedValue(sseResponse([], { status: 429, ok: false, bodyText: '{"code":"rate_limited","message":"请求过快"}' }))
    const onEvent = vi.fn()
    const error = await streamChatMessage({ conversationId: 'conv-1', clientMessageId: 'cm-1', content: 'x', model: 'm', onEvent }).catch((reason: unknown) => reason)
    expect(error).toBeInstanceOf(ChatStreamHttpError)
    expect((error as ChatStreamHttpError).status).toBe(429)
    expect((error as ChatStreamHttpError).code).toBe('rate_limited')
    expect((error as ChatStreamHttpError).message).toBe('请求过快')
    expect((error as ChatStreamHttpError).name).toBe('ChatStreamHttpError')
    expect(onEvent).not.toHaveBeenCalled()
  })

  it('HTTP 错误体非 JSON 时 code 为 undefined 并回退通用文案', async () => {
    fetchMock.mockResolvedValue(sseResponse([], { status: 502, ok: false, bodyText: 'plain text' }))
    const error = await streamChatMessage({ conversationId: 'conv-1', clientMessageId: 'cm-1', content: 'x', model: 'm', onEvent: vi.fn() }).catch((reason: unknown) => reason)
    expect(error).toBeInstanceOf(ChatStreamHttpError)
    expect((error as ChatStreamHttpError).code).toBeUndefined()
    expect((error as ChatStreamHttpError).message).toBe('plain text')
  })

  it('无法解析的事件块抛出 ChatStreamProtocolError', async () => {
    fetchMock.mockResolvedValue(sseResponse(['event: message.delta\ndata: {"bad":true}\n\n']))
    const error = await streamChatMessage({ conversationId: 'conv-1', clientMessageId: 'cm-1', content: 'x', model: 'm', onEvent: vi.fn() }).catch((reason: unknown) => reason)
    expect(error).toBeInstanceOf(ChatStreamProtocolError)
    expect((error as ChatStreamProtocolError).name).toBe('ChatStreamProtocolError')
    expect((error as ChatStreamProtocolError).message).toBe('收到格式无效的聊天流事件')
  })

  it('响应无 body 时抛出 ChatStreamHttpError', async () => {
    fetchMock.mockResolvedValue({ ok: false, status: 500, text: async () => 'no body', clone() { return this } } as unknown as Response)
    const error = await streamChatMessage({ conversationId: 'conv-1', clientMessageId: 'cm-1', content: 'x', model: 'm', onEvent: vi.fn() }).catch((reason: unknown) => reason)
    expect(error).toBeInstanceOf(ChatStreamHttpError)
    expect((error as ChatStreamHttpError).status).toBe(500)
  })

  it('流读取中断时取消 reader 并透传错误', async () => {
    const cancel = vi.fn().mockResolvedValue(undefined)
    const encoder = new TextEncoder()
    const readError = new Error('connection reset')
    let served = false
    const response: unknown = {
      ok: true,
      status: 200,
      text: async () => '',
      clone: () => response,
      body: {
        getReader: () => ({
          read: async () => {
            if (served) throw readError
            served = true
            return { done: false, value: encoder.encode('event: message.delta\ndata: {"messageId":"m-1","delta":"x","eventVersion":1}\n\n') }
          },
          cancel,
          releaseLock: vi.fn()
        })
      }
    }
    fetchMock.mockResolvedValue(response as Response)
    await expect(streamChatMessage({ conversationId: 'conv-1', clientMessageId: 'cm-1', content: 'x', model: 'm', onEvent: vi.fn() }))
      .rejects.toBe(readError)
    expect(cancel).toHaveBeenCalled()
  })
})

describe('attachChatStream', () => {
  it('GET SSE 请求挂载到指定 turn 流', async () => {
    fetchMock.mockResolvedValue(sseResponse(['event: message.completed\ndata: {"messageId":"m-1","eventVersion":1}\n\n']))
    const onEvent = vi.fn()
    const controller = new AbortController()
    await attachChatStream({ conversationId: 'conv-1', turnId: 'turn/1', signal: controller.signal, onEvent })
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe(`${window.location.origin}/__aisys__/api/my-chat/conversations/conv-1/streams/turn%2F1`)
    expect(init.method).toBe('GET')
    expect(init.credentials).toBe('include')
    expect(init.headers).toEqual({ accept: 'text/event-stream' })
    expect(init.signal).toBe(controller.signal)
    expect(onEvent).toHaveBeenCalledWith({ type: 'message.completed', data: { messageId: 'm-1', eventVersion: 1 } })
  })
})
