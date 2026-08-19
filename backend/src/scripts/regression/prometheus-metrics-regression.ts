import assert from 'node:assert/strict'

import {
  classifyHttpMetricRoute,
  finishHttpMetricRequest,
  recordGatewayFirstOutputMetric,
  recordGatewayUpstreamFailureMetric,
  renderPrometheusMetrics,
  resetPrometheusMetricsForTest,
  startHttpMetricRequest
} from '../../shared/prometheus-metrics.js'
import { classifyGatewayUpstreamFailure } from '../../modules/gateway/response/upstream-failure-classifier.js'

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

const upstreamResponseFailure = classifyGatewayUpstreamFailure({ phase: 'upstream_response' })
recordGatewayUpstreamFailureMetric(upstreamResponseFailure.failureClass, 503)
const upstreamRequestFailure = classifyGatewayUpstreamFailure({ phase: 'upstream_request' })
recordGatewayUpstreamFailureMetric(upstreamRequestFailure.failureClass, undefined)
recordGatewayFirstOutputMetric(1_500, 'POST')

const rendered = renderPrometheusMetrics()
assert.match(rendered, /juhe_ai_http_requests_total\{[^}]*method="POST"[^}]*outcome="completed"[^}]*route_group="gateway"[^}]*status_class="5xx"[^}]*\} 1/)
assert.match(rendered, /juhe_ai_http_requests_total\{[^}]*method="GET"[^}]*outcome="aborted"[^}]*route_group="management"[^}]*status_class="unknown"[^}]*\} 1/)
assert.match(rendered, /juhe_ai_http_request_duration_seconds_bucket\{[^}]*le="\+Inf"[^}]*route_group="gateway"[^}]*\} 1/)
assert.doesNotMatch(rendered, /chat\/completions|traceId|requestId/i)

const upstreamFailureMetricLines = rendered
  .split('\n')
  .filter((line) => line.startsWith('juhe_ai_gateway_upstream_failures_total{'))
assert.equal(upstreamFailureMetricLines.length, 2)
assert.match(rendered, /juhe_ai_gateway_upstream_failures_total\{[^}]*failure_class="opaque_upstream_response"[^}]*status_class="5xx"[^}]*\} 1/)
assert.match(rendered, /juhe_ai_gateway_upstream_failures_total\{[^}]*failure_class="transport"[^}]*status_class="unknown"[^}]*\} 1/)
for (const line of upstreamFailureMetricLines) {
  assert.deepEqual(
    [...line.matchAll(/([a-z_]+)="/g)].map((match) => match[1]).sort(),
    ['failure_class', 'service', 'status_class']
  )
  assert.doesNotMatch(line, /account|api[_-]?key|url|model|request|user|error|trace|credential|secret/i)
}

assert.match(
  rendered,
  /juhe_ai_gateway_upstream_failure_metrics_enabled\{service="juhe-ai"\} 1/
)
assert.match(
  rendered,
  /juhe_ai_gateway_first_output_duration_seconds_bucket\{[^}]*le="2"[^}]*method="POST"[^}]*\} 1/
)
assert.match(
  rendered,
  /juhe_ai_gateway_first_output_duration_seconds_count\{[^}]*method="POST"[^}]*\} 1/
)

resetPrometheusMetricsForTest()
assert.doesNotMatch(renderPrometheusMetrics(), /juhe_ai_gateway_upstream_failures_total\{/)
assert.doesNotMatch(renderPrometheusMetrics(), /juhe_ai_gateway_first_output_duration_seconds_count\{/)
assert.match(
  renderPrometheusMetrics(),
  /juhe_ai_gateway_upstream_failure_metrics_enabled\{service="juhe-ai"\} 1/
)

console.log('prometheus metrics regression passed')
