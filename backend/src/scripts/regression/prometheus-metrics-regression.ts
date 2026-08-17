import assert from 'node:assert/strict'

import {
  classifyHttpMetricRoute,
  finishHttpMetricRequest,
  renderPrometheusMetrics,
  resetPrometheusMetricsForTest,
  startHttpMetricRequest
} from '../../shared/prometheus-metrics.js'

assert.equal(classifyHttpMetricRoute('/__aisys__/metrics'), 'observability')
assert.equal(classifyHttpMetricRoute('/__aisys__/api/settings'), 'management')
assert.equal(classifyHttpMetricRoute('/__aipublic__/api-keys'), 'public_api')
assert.equal(classifyHttpMetricRoute('/v1/chat/completions'), 'gateway')
assert.equal(classifyHttpMetricRoute('/v12/not-a-gateway-route'), 'other')

resetPrometheusMetricsForTest()
const completed = startHttpMetricRequest('/v1/chat/completions', 'POST', 1_000)
finishHttpMetricRequest(completed, 503, 'completed', 1_150)
const aborted = startHttpMetricRequest('/__aisys__/api/settings', 'GET', 2_000)
finishHttpMetricRequest(aborted, undefined, 'aborted', 2_010)
assert.equal(startHttpMetricRequest('/__aisys__/metrics', 'GET', 3_000), undefined)

const rendered = renderPrometheusMetrics()
assert.match(rendered, /juhe_ai_http_requests_total\{[^}]*method="POST"[^}]*outcome="completed"[^}]*route_group="gateway"[^}]*status_class="5xx"[^}]*\} 1/)
assert.match(rendered, /juhe_ai_http_requests_total\{[^}]*method="GET"[^}]*outcome="aborted"[^}]*route_group="management"[^}]*status_class="unknown"[^}]*\} 1/)
assert.match(rendered, /juhe_ai_http_request_duration_seconds_bucket\{[^}]*le="\+Inf"[^}]*route_group="gateway"[^}]*\} 1/)
assert.doesNotMatch(rendered, /chat\/completions|traceId|requestId/i)

console.log('prometheus metrics regression passed')
