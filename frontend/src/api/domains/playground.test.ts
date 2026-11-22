import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { PlaygroundImageJobCreateRequest, PlaygroundStreamEvent } from '@/types/domain/playground'

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
  it('解析单 data 行的归一事件（delta/usage/error 三值）', () => {
    expect(parsePlaygroundSseData('data: {"type":"delta","text":"你好"}')).toEqual({ type: 'delta', text: '你好' })
    expect(parsePlaygroundSseData('data: {"type":"usage","promptTokens":10,"completionTokens":5,"totalTokens":15}')).toEqual({ type: 'usage', promptTokens: 10, completionTokens: 5, totalTokens: 15 })
    expect(parsePlaygroundSseData('data: {"type":"error","message":"上游 502"}')).toEqual({ type: 'error', message: '上游 502' })
  })

  it('多行 data: 按换行拼接后解析', () => {
    expect(parsePlaygroundSseData('data: {"type":"error",\ndata: "message":"上游 502"}')).toEqual({ type: 'error', message: '上游 502' })
  })

  it('无 data 行返回 null，非法 JSON 抛原错误，缺少 type 抛协议错误', () => {
    expect(parsePlaygroundSseData(': heartbeat')).toBeNull()
    expect(() => parsePlaygroundSseData('data: {oops')).toThrow()
    expect(() => parsePlaygroundSseData('data: {"message":"缺少 type"}')).toThrow(PlaygroundStreamProtocolError)
  })

  it('遗留未归一事件（如退役的 done）原样透传不抛错（向后兼容）', () => {
    expect(parsePlaygroundSseData('data: {"type":"done","rounds":2}')).toEqual({ type: 'done', rounds: 2 })
  })
})

describe('consumePlaygroundSseResponse 事件消费', () => {
  it('按顺序回调多事件（delta/usage，Key 化后事件只剩三值）', async () => {
    const response = sseResponse([
      'data: {"type":"delta","text":"正在查"}\n\n',
      'data: {"type":"delta","text":"询结果"}\n\n',
      'data: {"type":"usage","promptTokens":10,"completionTokens":5,"totalTokens":15}\n\n'
    ])
    const events: PlaygroundStreamEvent[] = []
    await consumePlaygroundSseResponse(response, '/playground/chat/stream', (event) => events.push(event))
    expect(events.map((event) => event.type)).toEqual(['delta', 'delta', 'usage'])
    expect(events[0]).toEqual({ type: 'delta', text: '正在查' })
    expect(events[2]).toEqual({ type: 'usage', promptTokens: 10, completionTokens: 5, totalTokens: 15 })
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

  it('CRLF 分隔与结尾无空行的 delta 帧也能解析', async () => {
    const response = sseResponse([
      'data: {"type":"delta","text":"你好"}\r\n\r\n',
      'data: {"type":"delta","text":"世界"}'
    ])
    const events: PlaygroundStreamEvent[] = []
    await consumePlaygroundSseResponse(response, '/playground/chat/stream', (event) => events.push(event))
    expect(events).toEqual([{ type: 'delta', text: '你好' }, { type: 'delta', text: '世界' }])
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

    const okResponse = sseResponse(['data: {"type":"delta","text":"你好"}\n\n'])
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
  it('POST JSON、携带凭据、SSE 头并恒传 stream:true（Key 化载荷 apiKeyId）', async () => {
    fetchMock.mockResolvedValue(sseResponse(['data: {"type":"delta","text":"你好"}\n\n']))
    const onEvent = vi.fn()
    const controller = new AbortController()
    await playgroundApi.chatStream({
      apiKeyId: 'key_1',
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
      apiKeyId: 'key_1',
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
    expect(onEvent).toHaveBeenCalledWith({ type: 'delta', text: '你好' })
  })

  it('HTTP 400 校验错误透出 code 与状态码', async () => {
    fetchMock.mockResolvedValue(sseResponse([], { status: 400, bodyText: '{"code":"playground_model_not_available","message":"模型不可用"}' }))
    await expect(playgroundApi.chatStream({
      apiKeyId: 'key_1', modelId: 'bad', messages: [{ role: 'user', content: 'hi' }], stream: true,
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
let lastConfig: InternalAxiosRequestConfig | undefined

/** 替换 axios adapter：捕获请求形状并返回可配置的 `{ data: { data } }` 响应。 */
function installCaptureAdapter(data: unknown = {}): void {
  responseData = data
  requests = []
  lastConfig = undefined
  http.defaults.adapter = async (config: InternalAxiosRequestConfig) => {
    lastConfig = config
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

  it('keyModels GET /playground/key-models?apiKeyId=：返回所选 Key 的模型行数组（Key 化目录，行不含计价投影）', async () => {
    const models = [
      { modelId: 'gpt-5.4', kind: 'chat', providerCode: 'gpt' },
      { modelId: 'gpt-image-2', kind: 'image', providerCode: 'gpt' }
    ]
    installCaptureAdapter({ models })
    await expect(playgroundApi.keyModels('key_1')).resolves.toEqual(models)
    expect(requests).toEqual([{ method: 'GET', url: '/playground/key-models', params: { apiKeyId: 'key_1' }, data: undefined }])
  })

  it('imagesReferences POST /playground/images/references 并返回参考图引用（url 归一为同域绝对地址）', async () => {
    installCaptureAdapter({ id: 'ref_1', url: '/playground/artifacts/ref_1/content' })
    await expect(playgroundApi.imagesReferences({ dataBase64: 'aGVsbG8=' })).resolves.toEqual({
      id: 'ref_1',
      url: '/__aisys__/api/playground/artifacts/ref_1/content'
    })
    expect(requests).toEqual([{
      method: 'POST',
      url: '/playground/images/references',
      params: undefined,
      data: JSON.stringify({ dataBase64: 'aGVsbG8=' })
    }])
  })

  it('imagesCreateJob POST /playground/images/jobs：payload 透传、立即返回无需 timeout:0（设计 §3.4.2）', async () => {
    installCaptureAdapter({ jobId: 'job_9', status: 'queued' })
    const payload: PlaygroundImageJobCreateRequest = {
      apiKeyId: 'key_1',
      modelId: 'gpt-image-2',
      prompt: '一只猫',
      aspectRatio: '1:1',
      resolution: '1k',
      quality: 'standard',
      outputFormat: 'png',
      referenceIds: ['ref_1']
    }
    await expect(playgroundApi.imagesCreateJob(payload)).resolves.toEqual({ jobId: 'job_9', status: 'queued' })
    expect(requests).toEqual([{
      method: 'POST',
      url: '/playground/images/jobs',
      params: undefined,
      data: JSON.stringify(payload)
    }])
    expect(lastConfig?.timeout).toBe(15000)
  })

  it('imagesCreateJob 可选参数与空参考图：缺省键不发送、referenceIds 恒为数组', async () => {
    installCaptureAdapter({ jobId: 'job_9', status: 'queued' })
    await playgroundApi.imagesCreateJob({ apiKeyId: 'key_1', modelId: 'gpt-image-2', prompt: '一只猫', referenceIds: [] })
    expect(JSON.parse(String(requests[0]?.data))).toEqual({
      apiKeyId: 'key_1',
      modelId: 'gpt-image-2',
      prompt: '一只猫',
      referenceIds: []
    })
  })

  it('imagesJobs GET /playground/images/jobs：查询参数透传、卡片 url 归一、nextCursor 透传', async () => {
    installCaptureAdapter({
      jobs: [{
        jobId: 'job_1',
        status: 'completed',
        prompt: '一只猫',
        model: 'gpt-image-2',
        apiKeyId: 'key_1',
        size: '1024x1024',
        referenceCount: 1,
        references: [{ id: 'ref_1', url: '/playground/artifacts/ref_1/content' }],
        images: [{ id: 'img_1', url: '/playground/artifacts/img_1/content' }],
        imageDeleted: false,
        usage: { inputTokens: 12, outputTokens: 4096, totalTokens: 4108 },
        costUsd: 0.0631,
        error: '',
        createdAt: 1760000000,
        completedAt: 1760000300,
        expiresAt: 1760604800
      }],
      nextCursor: 1759999000
    })
    const page = await playgroundApi.imagesJobs({
      keyword: '猫',
      apiKeyId: 'key_1',
      status: 'completed',
      quality: 'standard',
      limit: 24,
      before: 1759999500
    })
    expect(page.nextCursor).toBe(1759999000)
    // 卡片 references/images 的内容地址在 api 域边界统一归一。
    expect(page.jobs[0]?.references[0]?.url).toBe('/__aisys__/api/playground/artifacts/ref_1/content')
    expect(page.jobs[0]?.images[0]?.url).toBe('/__aisys__/api/playground/artifacts/img_1/content')
    expect(requests).toEqual([{
      method: 'GET',
      url: '/playground/images/jobs',
      params: { keyword: '猫', apiKeyId: 'key_1', status: 'completed', quality: 'standard', limit: 24, before: 1759999500 },
      data: undefined
    }])
  })

  it('imagesJobs 无筛选默认形状：params 仅携带 limit，nextCursor 缺省透传 undefined', async () => {
    installCaptureAdapter({ jobs: [] })
    await expect(playgroundApi.imagesJobs()).resolves.toEqual({ jobs: [], nextCursor: undefined })
    expect(requests).toEqual([{ method: 'GET', url: '/playground/images/jobs', params: {}, data: undefined }])
  })

  it('imagesJob GET /playground/images/jobs/{id}：返回单卡片，失败/过期空数组原样保留', async () => {
    installCaptureAdapter({
      jobId: 'job_2',
      status: 'failed',
      prompt: '一只狗',
      model: 'gpt-image-2',
      apiKeyId: 'key_1',
      referenceCount: 0,
      references: [],
      images: [],
      imageDeleted: false,
      error: '上游 502',
      createdAt: 1760000000
    })
    const job = await playgroundApi.imagesJob('job_2')
    expect(job.status).toBe('failed')
    expect(job.images).toEqual([])
    expect(job.error).toBe('上游 502')
    expect(requests).toEqual([{ method: 'GET', url: '/playground/images/jobs/job_2', params: undefined, data: undefined }])
  })

  it('imagesJobRemove DELETE /playground/images/jobs/{id}：jobId 经 encodeURIComponent，成功 204 无响应体不 unwrap（设计 §3.4.6）', async () => {
    installCaptureAdapter(null)
    await playgroundApi.imagesJobRemove('job_3')
    expect(requests).toEqual([{ method: 'DELETE', url: '/playground/images/jobs/job_3', params: undefined, data: undefined }])
    await playgroundApi.imagesJobRemove('job/1')
    expect(requests[1]?.url).toBe('/playground/images/jobs/job%2F1')
  })

  it('artifacts GET /playground/artifacts 并返回暂存清单（v1.2 无 images 字段，url 归一，video 引用携带 apiKeyId）', async () => {
    installCaptureAdapter({
      speech: { id: 'aud_1', url: '/playground/artifacts/aud_1/content', mimeType: 'audio/mpeg', createdAt: 1761840000 },
      video: { jobId: 'job_1', apiKeyId: 'key_1', modelId: 'video-1', status: 'running', createdAt: 1761840000 }
    })
    await expect(playgroundApi.artifacts()).resolves.toEqual({
      speech: { id: 'aud_1', url: '/__aisys__/api/playground/artifacts/aud_1/content', mimeType: 'audio/mpeg', createdAt: 1761840000 },
      video: { jobId: 'job_1', apiKeyId: 'key_1', modelId: 'video-1', status: 'running', createdAt: 1761840000 }
    })
    expect(requests).toEqual([{ method: 'GET', url: '/playground/artifacts', params: undefined, data: undefined }])
  })

  it('videosCreate POST /playground/videos；videosPoll GET 带 apiKeyId 查询参数（Key 化）', async () => {
    installCaptureAdapter({ jobId: 'job_1' })
    await expect(playgroundApi.videosCreate({ apiKeyId: 'key_1', modelId: 'video-1', prompt: '海浪' })).resolves.toEqual({ jobId: 'job_1' })
    expect(requests).toEqual([
      { method: 'POST', url: '/playground/videos', params: undefined, data: JSON.stringify({ apiKeyId: 'key_1', modelId: 'video-1', prompt: '海浪' }) }
    ])
    installCaptureAdapter({ status: 'running', progress: 40 })
    await expect(playgroundApi.videosPoll('job_1', 'key_1')).resolves.toEqual({ status: 'running', progress: 40 })
    expect(requests).toEqual([
      { method: 'GET', url: '/playground/videos/job_1', params: { apiKeyId: 'key_1' }, data: undefined }
    ])
    // error 是归一对象 {code?, message?}（后端 videosPollError 原样透出），非字符串。
    installCaptureAdapter({ status: 'failed', error: { code: 'upstream_failed', message: '上游任务失败' } })
    await expect(playgroundApi.videosPoll('job_1', 'key_1')).resolves.toEqual({
      status: 'failed',
      error: { code: 'upstream_failed', message: '上游任务失败' }
    })
  })

  it('videosContent GET content 端点并转 blob URL', async () => {
    const blob = new Blob(['video-bytes'], { type: 'video/mp4' })
    fetchMock.mockResolvedValue({ ok: true, status: 200, blob: async () => blob } as unknown as Response)
    const url = await playgroundApi.videosContent('job_1', 'key_1')
    expect(url).toMatch(/^blob:/)
    URL.revokeObjectURL(url)
    const [requestUrl, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(String(requestUrl)).toContain('/playground/videos/job_1/content?apiKeyId=key_1')
    expect(init.credentials).toBe('include')
  })

  it('speech POST /playground/speech 并返回暂存引用（url 归一为同域绝对地址）', async () => {
    installCaptureAdapter({ id: 'aud_1', url: '/playground/artifacts/aud_1/content', mimeType: 'audio/mpeg' })
    await expect(playgroundApi.speech({ apiKeyId: 'key_1', modelId: 'speech-1', input: '你好', voice: 'alloy' })).resolves.toEqual({
      id: 'aud_1',
      url: '/__aisys__/api/playground/artifacts/aud_1/content',
      mimeType: 'audio/mpeg'
    })
    expect(requests).toEqual([{
      method: 'POST',
      url: '/playground/speech',
      params: undefined,
      data: JSON.stringify({ apiKeyId: 'key_1', modelId: 'speech-1', input: '你好', voice: 'alloy' })
    }])
  })

  it('embeddings POST /playground/embeddings：payload 透传、timeout 0、unwrap 后直接返回向量结果（设计 §3.9）', async () => {
    const embeddingsResult = {
      model: 'text-embedding-v4',
      usage: { promptTokens: 12, totalTokens: 12 },
      items: [
        { index: 0, dimensions: 2, vector: [1, 0] },
        { index: 1, dimensions: 2, vector: [0.8, 0.6] }
      ]
    }
    installCaptureAdapter(embeddingsResult)
    const payload = {
      apiKeyId: 'key_1',
      modelId: 'text-embedding-v4',
      input: ['文本一', '文本二'],
      dimensions: 1024
    }
    // 响应为纯 JSON 向量结果（无产物 url），不走 withContentUrl 归一。
    await expect(playgroundApi.embeddings(payload)).resolves.toEqual(embeddingsResult)
    // encodingFormat 是后端契约保留的透传字段，演练场前端请求不再携带该键。
    expect(JSON.parse(String(requests[0]?.data))).not.toHaveProperty('encodingFormat')
    expect(requests).toEqual([{
      method: 'POST',
      url: '/playground/embeddings',
      params: undefined,
      data: JSON.stringify(payload)
    }])
    expect(lastConfig?.timeout).toBe(0)
  })

  it('embeddings 可选参数缺省时不发 dimensions/encodingFormat 键', async () => {
    installCaptureAdapter({ model: 'text-embedding-v4', items: [] })
    await playgroundApi.embeddings({ apiKeyId: 'key_1', modelId: 'text-embedding-v4', input: ['只有一条'] })
    const sentBody = JSON.parse(String(requests[0]?.data))
    expect(sentBody).not.toHaveProperty('dimensions')
    expect(sentBody).not.toHaveProperty('encodingFormat')
    expect(sentBody).toEqual({
      apiKeyId: 'key_1',
      modelId: 'text-embedding-v4',
      input: ['只有一条']
    })
  })

  it('rerank POST /playground/rerank：payload 透传、timeout 0、降序 results 原样透传（设计 §3.10）', async () => {
    const rerankResult = {
      results: [
        { index: 2, relevanceScore: 0.98 },
        { index: 0, relevanceScore: 0.42 },
        { index: 1, relevanceScore: 0.07 }
      ],
      usage: { promptTokens: 100, totalTokens: 100 }
    }
    installCaptureAdapter(rerankResult)
    const payload = {
      apiKeyId: 'key_1',
      modelId: 'gte-rerank-v2',
      query: '查询语句',
      documents: ['文档一', '文档二', '文档三'],
      topN: 3,
      returnDocuments: false
    }
    // returnDocuments=false 时结果无 document 字段；api 域不改写顺序与形状。
    await expect(playgroundApi.rerank(payload)).resolves.toEqual(rerankResult)
    expect(requests).toEqual([{
      method: 'POST',
      url: '/playground/rerank',
      params: undefined,
      data: JSON.stringify(payload)
    }])
    expect(lastConfig?.timeout).toBe(0)
  })

  it('rerank 默认形状只发 apiKeyId/modelId/query/documents，回显结果带 document.text', async () => {
    installCaptureAdapter({
      results: [{ index: 0, relevanceScore: 0.5, document: { text: '文档一' } }]
    })
    await playgroundApi.rerank({ apiKeyId: 'key_1', modelId: 'gte-rerank-v2', query: '查询', documents: ['文档一'] })
    expect(JSON.parse(String(requests[0]?.data))).toEqual({
      apiKeyId: 'key_1',
      modelId: 'gte-rerank-v2',
      query: '查询',
      documents: ['文档一']
    })
    await expect(playgroundApi.rerank({ apiKeyId: 'key_1', modelId: 'gte-rerank-v2', query: '查询', documents: ['文档一'] }))
      .resolves.toEqual({ results: [{ index: 0, relevanceScore: 0.5, document: { text: '文档一' } }] })
  })
})
