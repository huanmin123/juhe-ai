import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { http } from '../http'
import { newStudioClientOperationId, studioApi, studioListLimit, studioAssetContentUrl } from './studio'

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

function requestShapes(): Array<[string, string]> {
  return requests.map((request) => [request.method, request.url])
}

function lastRequest(): CapturedRequest {
  return requests[requests.length - 1]
}

function jsonPayloadOf(request: CapturedRequest): Record<string, unknown> {
  return JSON.parse(String(request.data)) as Record<string, unknown>
}

beforeEach(() => installCaptureAdapter())
afterEach(() => {
  http.defaults.adapter = originalAdapter
})

describe('studioApi 请求形状', () => {
  it('账户候选与模型目录发出 GET 并携带 domain 查询', async () => {
    await studioApi.listAccounts()
    await studioApi.listAccounts('image')
    await studioApi.listModels('audio')
    expect(requestShapes()).toEqual([
      ['GET', '/my-studio/accounts'],
      ['GET', '/my-studio/accounts'],
      ['GET', '/my-studio/models']
    ])
    expect(requests[1].params).toEqual({ domain: 'image' })
    expect(requests[2].params).toEqual({ domain: 'audio' })
  })

  it('图像生成 POST 携带 clientOperationId，且不含契约外字段', async () => {
    const operationId = newStudioClientOperationId()
    await studioApi.generateImage({
      accountId: 'acc-1',
      model: 'gpt-image-1',
      prompt: '一只猫',
      size: '1024x1024',
      clientOperationId: operationId
    })
    expect(requestShapes()).toEqual([['POST', '/my-studio/generations/images']])
    const payload = jsonPayloadOf(lastRequest())
    expect(payload.clientOperationId).toBe(operationId)
    expect(payload).toMatchObject({ accountId: 'acc-1', model: 'gpt-image-1', prompt: '一只猫', size: '1024x1024' })
    // 契约外字段类比（决策 #9④ 无数量参数；无问答域 webSearch 透传）。
    expect(payload).not.toHaveProperty('n')
    expect(payload).not.toHaveProperty('enableWebSearch')
    expect(Object.keys(payload).every((key) => [
      'accountId', 'model', 'prompt', 'negativePrompt', 'size', 'referenceAssetId', 'providerOptions', 'clientOperationId'
    ].includes(key))).toBe(true)
  })

  it('视频与 TTS 生成 POST 携带 clientOperationId，同步端点返回已保存资产', async () => {
    await studioApi.generateVideo({
      accountId: 'acc-2',
      model: 'veo-3',
      prompt: '城市延时',
      seconds: 4,
      clientOperationId: newStudioClientOperationId()
    })
    await studioApi.generateSpeech({
      accountId: 'acc-3',
      model: 'tts-1',
      input: '你好',
      voice: 'alloy',
      clientOperationId: newStudioClientOperationId()
    })
    expect(requestShapes()).toEqual([
      ['POST', '/my-studio/generations/videos'],
      ['POST', '/my-studio/generations/speech']
    ])
    expect(jsonPayloadOf(requests[0])).toHaveProperty('clientOperationId')
    expect(jsonPayloadOf(requests[1])).toMatchObject({ input: '你好', voice: 'alloy' })
  })

  it('STT multipart 携带 file 与 accountId/model/clientOperationId 字段', async () => {
    const controller = new AbortController()
    const file = new File(['audio-bytes'], 'clip.mp3', { type: 'audio/mpeg' })
    await studioApi.generateTranscription(
      file,
      { accountId: 'acc-4', model: 'whisper-1', clientOperationId: 'op-stt-1', language: 'zh' },
      { signal: controller.signal }
    )
    const request = lastRequest()
    expect(requestShapes()).toEqual([['POST', '/my-studio/generations/transcriptions']])
    expect(request.timeout).toBe(0)
    expect(request.signal).toBe(controller.signal)
    const body = request.data as FormData
    expect(body).toBeInstanceOf(FormData)
    expect(body.get('accountId')).toBe('acc-4')
    expect(body.get('model')).toBe('whisper-1')
    expect(body.get('clientOperationId')).toBe('op-stt-1')
    expect(body.get('language')).toBe('zh')
    expect(body.get('file')).toBeInstanceOf(File)
  })

  it('上传素材 timeout 0 且 AbortSignal/paramsJson 透传', async () => {
    const controller = new AbortController()
    const onProgress = (): void => undefined
    await studioApi.uploadAsset(new File(['png'], 'a.png', { type: 'image/png' }), {
      signal: controller.signal,
      onProgress,
      paramsJson: { source: 'image-workbench' }
    })
    const request = lastRequest()
    expect(requestShapes()).toEqual([['POST', '/my-studio/assets']])
    expect(request.timeout).toBe(0)
    expect(request.signal).toBe(controller.signal)
    const body = request.data as FormData
    expect(body.get('paramsJson')).toBe(JSON.stringify({ source: 'image-workbench' }))
    expect(body.get('file')).toBeInstanceOf(File)
  })

  it('资产与任务列表 limit/offset 归一，任务查询支持 status/kind/operation_id', async () => {
    await studioApi.listAssets({ limit: 24, offset: 48, kind: 'image' })
    await studioApi.listTasks({ limit: 20, offset: 0, kind: 'video', status: 'queued' })
    await studioApi.listTasks({ limit: 5, operation_id: 'op-1' })
    expect(requestShapes()).toEqual([
      ['GET', '/my-studio/assets'],
      ['GET', '/my-studio/tasks'],
      ['GET', '/my-studio/tasks']
    ])
    expect(requests[0].params).toEqual({ limit: 24, offset: 48, kind: 'image' })
    expect(requests[1].params).toEqual({ limit: 20, offset: 0, kind: 'video', status: 'queued' })
    expect(requests[2].params).toEqual({ limit: 5, operation_id: 'op-1' })
  })

  it('删除素材与任务取消命中资源路径，取消返回任务快照', async () => {
    installCaptureAdapter({ id: 'stask_1', status: 'cancelled' })
    await studioApi.deleteAsset('stasset/9')
    const cancelled = await studioApi.cancelTask('stask/7')
    expect(requestShapes()).toEqual([
      ['DELETE', '/my-studio/assets/stasset%2F9'],
      ['DELETE', '/my-studio/tasks/stask%2F7']
    ])
    expect(cancelled).toMatchObject({ id: 'stask_1', status: 'cancelled' })
  })

  it('任务快照轮询端点透传 AbortSignal', async () => {
    const controller = new AbortController()
    await studioApi.getTask('stask_1', { signal: controller.signal })
    expect(requestShapes()).toEqual([['GET', '/my-studio/tasks/stask_1']])
    expect(lastRequest().signal).toBe(controller.signal)
  })
})

describe('studioAssetContentUrl', () => {
  it('默认 original 直出，preview/download 以查询串表达', () => {
    // apiUrl 返回绝对地址（window.location.origin 基准），断言路径与查询串形态。
    expect(studioAssetContentUrl('stasset_1')).toMatch(/\/__aisys__\/api\/my-studio\/assets\/stasset_1\/content$/)
    expect(studioAssetContentUrl('stasset_1', { variant: 'preview' }))
      .toMatch(/\/__aisys__\/api\/my-studio\/assets\/stasset_1\/content\?variant=preview$/)
    expect(studioAssetContentUrl('stasset_1', { download: true }))
      .toMatch(/\/__aisys__\/api\/my-studio\/assets\/stasset_1\/content\?download=1$/)
  })
})

describe('studioListLimit 边界', () => {
  it('非法值回落 undefined（服务端默认 20），越界封顶 100', () => {
    expect(studioListLimit(undefined)).toBeUndefined()
    expect(studioListLimit(Number.NaN)).toBeUndefined()
    expect(studioListLimit(0)).toBeUndefined()
    expect(studioListLimit(-3)).toBeUndefined()
    expect(studioListLimit(1)).toBe(1)
    expect(studioListLimit(24)).toBe(24)
    expect(studioListLimit(100)).toBe(100)
    expect(studioListLimit(101)).toBe(100)
  })
})

describe('newStudioClientOperationId', () => {
  it('生成 1..128 字符内的唯一幂等键', () => {
    const first = newStudioClientOperationId()
    const second = newStudioClientOperationId()
    expect(first).toBeTruthy()
    expect(first.length).toBeLessThanOrEqual(128)
    expect(first).not.toBe(second)
  })
})
