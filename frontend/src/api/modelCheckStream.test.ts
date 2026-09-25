import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { ModelCheckRunPayload } from '@/types/domain'

import { setUnauthorizedHandler } from './http'
import { runModelCheckStream } from './modelCheckStream'

const runPayload: ModelCheckRunPayload = { targetType: 'account', targetId: 'a-1', model: 'gpt-4o' }

/** 构造可控的 SSE Response：chunks 逐段产出，text() 返回错误响应体；getReader 复用同一 reader 便于断言。 */
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
  setUnauthorizedHandler(() => {})
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('runModelCheckStream 正常流', () => {
  it('按顺序回调 progress 并以 complete 详情收尾', async () => {
    fetchMock.mockResolvedValue(sseResponse([
      'event: progress\ndata: {"percent":10}\n\n',
      'event: progress\ndata: {"percent":80}\n\n',
      'event: complete\ndata: {"id":"run-1","status":"completed"}\n\n'
    ]))
    const onProgress = vi.fn()
    const onComplete = vi.fn()
    const detail = await runModelCheckStream('/model-checks/run/stream', runPayload, { onProgress, onComplete })
    expect(detail).toEqual({ id: 'run-1', status: 'completed' })
    expect(onProgress).toHaveBeenCalledTimes(2)
    expect(onProgress).toHaveBeenNthCalledWith(1, { percent: 10 })
    expect(onProgress).toHaveBeenNthCalledWith(2, { percent: 80 })
    expect(onComplete).toHaveBeenCalledTimes(1)
    expect(onComplete).toHaveBeenCalledWith(detail)
  })

  it('支持事件跨 chunk 分割与多行 data: 前缀拼接', async () => {
    fetchMock.mockResolvedValue(sseResponse([
      'event: progress\nda',
      'ta: {"percent":30,\ndata: "stage":"probe"}\n\nevent: comp',
      'lete\ndata: {"id":"run-2"}\n\n'
    ]))
    const onProgress = vi.fn()
    const detail = await runModelCheckStream('/model-checks/run/stream', runPayload, { onProgress })
    expect(onProgress).toHaveBeenCalledWith({ percent: 30, stage: 'probe' })
    expect(detail).toEqual({ id: 'run-2' })
  })

  it('CRLF 分隔符与结尾无空行的 complete 事件也能解析', async () => {
    fetchMock.mockResolvedValue(sseResponse([
      'event: progress\r\ndata: {"percent":1}\r\n\r\n',
      'event: complete\r\ndata: {"id":"run-3"}'
    ]))
    const onProgress = vi.fn()
    const detail = await runModelCheckStream('/model-checks/run/stream', runPayload, { onProgress })
    expect(onProgress).toHaveBeenCalledWith({ percent: 1 })
    expect(detail).toEqual({ id: 'run-3' })
  })

  it('空事件块与无 data 的事件被忽略', async () => {
    fetchMock.mockResolvedValue(sseResponse([
      '\n\n',
      'event: progress\n\n',
      'event: complete\ndata: {"id":"run-4"}\n\n'
    ]))
    const onProgress = vi.fn()
    const detail = await runModelCheckStream('/model-checks/run/stream', runPayload, { onProgress })
    expect(onProgress).not.toHaveBeenCalled()
    expect(detail).toEqual({ id: 'run-4' })
  })

  it('complete 之后的 progress 仍回调但返回值保持 complete 详情', async () => {
    fetchMock.mockResolvedValue(sseResponse([
      'event: complete\ndata: {"id":"run-5"}\n\n',
      'event: progress\ndata: {"percent":99}\n\n'
    ]))
    const onProgress = vi.fn()
    const detail = await runModelCheckStream('/model-checks/run/stream', runPayload, { onProgress })
    expect(onProgress).toHaveBeenCalledTimes(1)
    expect(detail).toEqual({ id: 'run-5' })
  })
})

describe('runModelCheckStream fetch 请求形状', () => {
  it('POST JSON、携带凭据、SSE 头与查询参数', async () => {
    fetchMock.mockResolvedValue(sseResponse(['event: complete\ndata: {"id":"run-6"}\n\n']))
    const controller = new AbortController()
    await runModelCheckStream('/model-checks/run/stream', runPayload, { signal: controller.signal }, { systemAccountId: 'sa-1' })
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/__aisys__/api/model-checks/run/stream?systemAccountId=sa-1')
    expect(init.method).toBe('POST')
    expect(init.credentials).toBe('include')
    expect(init.headers).toEqual({ accept: 'text/event-stream', 'content-type': 'application/json' })
    expect(JSON.parse(String(init.body))).toEqual(runPayload)
    expect(init.signal).toBe(controller.signal)
  })
})

describe('runModelCheckStream 异常路径', () => {
  it('error 事件回调 onError 并抛出服务端 message', async () => {
    fetchMock.mockResolvedValue(sseResponse([
      'event: error\ndata: {"message":"上游不可用","statusCode":502}\n\n'
    ]))
    const onError = vi.fn()
    await expect(runModelCheckStream('/model-checks/run/stream', runPayload, { onError })).rejects.toThrow('上游不可用')
    expect(onError).toHaveBeenCalledWith({ message: '上游不可用', statusCode: 502 })
  })

  it('error 事件中断时释放 reader（cancel 后 releaseLock）', async () => {
    const response = sseResponse([
      'event: progress\ndata: {"percent":10}\n\n',
      'event: error\ndata: {"message":"上游不可用"}\n\n',
      'event: complete\ndata: {"id":"run-x"}\n\n'
    ])
    fetchMock.mockResolvedValue(response)
    await expect(runModelCheckStream('/model-checks/run/stream', runPayload)).rejects.toThrow('上游不可用')
    const reader = sseReader(response)
    expect(reader.cancel).toHaveBeenCalledTimes(1)
    expect(reader.releaseLock).toHaveBeenCalledTimes(1)
  })

  it('流正常读完后不 cancel 但仍释放锁', async () => {
    const response = sseResponse(['event: complete\ndata: {"id":"run-y"}\n\n'])
    fetchMock.mockResolvedValue(response)
    await runModelCheckStream('/model-checks/run/stream', runPayload)
    const reader = sseReader(response)
    expect(reader.cancel).not.toHaveBeenCalled()
    expect(reader.releaseLock).toHaveBeenCalledTimes(1)
  })

  it('error 事件无 message 时使用默认错误文案', async () => {
    fetchMock.mockResolvedValue(sseResponse(['event: error\ndata: {"statusCode":500}\n\n']))
    await expect(runModelCheckStream('/model-checks/run/stream', runPayload)).rejects.toThrow('模型检测失败')
  })

  it('error 事件 data 非 JSON 时以原文作为 message', async () => {
    fetchMock.mockResolvedValue(sseResponse(['event: error\ndata: 上游超时退出\n\n']))
    const onError = vi.fn()
    await expect(runModelCheckStream('/model-checks/run/stream', runPayload, { onError })).rejects.toThrow('上游超时退出')
    expect(onError).toHaveBeenCalledWith({ message: '上游超时退出' })
  })

  it('流结束但缺少 complete 事件时抛出未完成错误', async () => {
    fetchMock.mockResolvedValue(sseResponse(['event: progress\ndata: {"percent":1}\n\n']))
    await expect(runModelCheckStream('/model-checks/run/stream', runPayload)).rejects.toThrow('模型检测进度流未返回完成结果')
  })

  it('HTTP 非 2xx 时抛出响应体中的 message', async () => {
    fetchMock.mockResolvedValue(sseResponse([], { status: 500, bodyText: '{"message":"服务器内部错误"}' }))
    await expect(runModelCheckStream('/model-checks/run/stream', runPayload)).rejects.toThrow('服务器内部错误')
  })

  it('HTTP 401 非 /auth/ 路径触发 unauthorizedHandler', async () => {
    const unauthorizedHandler = vi.fn()
    setUnauthorizedHandler(unauthorizedHandler)
    fetchMock.mockResolvedValue(sseResponse([], { status: 401, bodyText: '' }))
    await expect(runModelCheckStream('/model-checks/run/stream', runPayload)).rejects.toThrow('请求失败：HTTP 401')
    expect(unauthorizedHandler).toHaveBeenCalledTimes(1)
  })

  it('响应无 body 时抛出流不可用错误', async () => {
    fetchMock.mockResolvedValue({ ok: true, status: 200, text: async () => '' } as unknown as Response)
    await expect(runModelCheckStream('/model-checks/run/stream', runPayload)).rejects.toThrow('模型检测进度流不可用')
  })

  it('fetch 自身失败时原样透传错误', async () => {
    const networkError = new TypeError('fetch failed')
    fetchMock.mockRejectedValue(networkError)
    await expect(runModelCheckStream('/model-checks/run/stream', runPayload)).rejects.toBe(networkError)
  })
})
