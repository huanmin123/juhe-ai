import assert from 'node:assert/strict'

import type { AxiosAdapter } from 'axios'

import { externalIntegrationSourcesApi } from '../../src/api/domains/externalIntegrationSources'
import { http } from '../../src/api/http'

interface CapturedRequest {
  method: string
  url: string
}

const sourceId = 'source/a?b#c% d'
const encodedSourceId = 'source%2Fa%3Fb%23c%25%20d'
const capturedRequests: CapturedRequest[] = []
const originalAdapter = http.defaults.adapter

const requestCaptureAdapter: AxiosAdapter = async (config) => {
  capturedRequests.push({
    method: String(config.method ?? '').toUpperCase(),
    url: String(config.url ?? '')
  })

  return {
    data: { data: {} },
    status: 200,
    statusText: 'OK',
    headers: {},
    config
  }
}

try {
  http.defaults.adapter = requestCaptureAdapter

  await externalIntegrationSourcesApi.delete(sourceId)
} finally {
  http.defaults.adapter = originalAdapter
}

assert.deepEqual(capturedRequests, [
  { method: 'DELETE', url: `/external-integration-sources/${encodedSourceId}` }
], '外部集成来源 DELETE 动态路径段必须使用 encodeURIComponent 编码')

console.log('External integration source API path regression passed')
