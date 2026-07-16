import { strict as assert } from 'node:assert'
import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http'
import type { AddressInfo } from 'node:net'

import type { AxiosAdapter, AxiosRequestConfig } from 'axios'

import { externalIntegrationSourcesApi } from '../../src/api/domains/externalIntegrationSources'
import { http } from '../../src/api/http'

import {
  formatRealGoManagementSmokeSummary,
  loadRealGoManagementSmokeConfig,
  realGoManagementSmokeEnv,
  runRealGoManagementSmoke,
  runRealGoManagementSmokeFromEnvironment,
  type SmokeEnvironment
} from '../smoke/plan0081-real-go-management-smoke'

interface RequestRecord {
  method?: string
  url?: string
  headers: IncomingMessage['headers']
  body?: unknown
}

type MockScenario =
  | 'normal'
  | 'api_keys_empty'
  | 'api_keys_admin_owner_missing'
  | 'api_keys_self_owner_leak'
  | 'api_keys_sensitive_field'
  | 'api_keys_nested_preview_field'
  | 'api_keys_pagination_invalid'
  | 'account_test_options_not_object'
  | 'account_test_options_account_mismatch'
  | 'account_test_options_default_model_empty'
  | 'account_test_options_models_empty'
  | 'account_test_options_model_empty'
  | 'account_test_options_protocols_invalid'
  | 'account_test_options_model_endpoint_modes_missing'
  | 'account_test_options_model_endpoint_modes_empty'
  | 'account_test_options_model_endpoint_mode_invalid'
  | 'account_test_options_default_model_missing'
  | 'account_test_options_endpoint_modes_empty'
  | 'account_test_options_endpoint_mode_invalid'
  | 'account_test_options_default_model_modes_mismatch'
  | 'account_test_options_default_endpoint_mode_mismatch'
  | 'external_integration_source_scopes_missing_item'
  | 'external_integration_source_scopes_invalid_field'
  | 'external_integration_source_api_docs_base_path_invalid'
  | 'external_integration_source_api_docs_auth_type_invalid'
  | 'external_integration_source_api_docs_missing_item'
  | 'external_integration_source_api_docs_get_contract_invalid'
  | 'external_integration_source_api_docs_get_fields_empty'
  | 'external_integration_source_api_docs_post_body_missing'
  | 'external_integration_source_api_docs_last_item_fields_empty'
  | 'external_integration_source_list_missing_field'
  | 'external_integration_source_list_rate_limits_unsorted'
  | 'external_integration_source_list_sensitive_field'
  | 'external_integration_source_list_primary_status_invalid'
  | 'external_integration_source_list_primary_not_active'
  | 'external_integration_source_list_primary_time_invalid'
  | 'external_integration_source_list_pagination'
  | 'external_integration_source_list_pagination_duplicate'
  | 'external_integration_source_detail_success'
  | 'external_integration_source_detail_no_non_built_in'
  | 'external_integration_source_detail_newer_snapshot'
  | 'external_integration_source_detail_id_mismatch'
  | 'external_integration_source_detail_edit_field_mismatch'
  | 'external_integration_source_detail_unknown_field'
  | 'external_integration_source_detail_tokens_not_array'
  | 'external_integration_source_detail_token_count_mismatch'
  | 'external_integration_source_detail_active_token_count_mismatch'
  | 'external_integration_source_detail_created_at_unsorted'
  | 'external_integration_source_detail_id_unsorted'
  | 'external_integration_source_detail_token_unknown_field'
  | 'external_integration_source_detail_sensitive_token'
  | 'external_integration_source_detail_sensitive_hash'
  | 'external_integration_source_detail_sensitive_ciphertext'
  | 'external_integration_source_detail_sensitive_preview_value'
  | 'external_integration_source_detail_sensitive_primary_token'
  | 'external_integration_source_secret_success'
  | 'external_integration_source_secret_malformed'
  | 'external_integration_source_secret_empty'
  | 'external_integration_source_secret_preview_mismatch'
  | 'external_integration_source_secret_pragma_invalid'
  | 'ip_stats_not_ready'
  | 'ip_stats_empty'
  | 'ip_stats_detail_not_ready'
  | 'ip_stats_detail_empty'
  | 'ip_stats_failure'
  | 'ip_stats_timeout'
  | 'public_api_logs_non_empty'
  | 'public_api_logs_envelope_invalid'
  | 'public_api_logs_items_invalid'
  | 'public_api_logs_pagination_invalid'
  | 'public_api_logs_required_field_invalid'
  | 'public_api_logs_optional_field_invalid'
  | 'public_api_logs_capture_status_invalid'
  | 'public_api_logs_no_store'
  | 'public_api_log_detail_request_data_invalid'
  | 'public_api_log_detail_response_data_invalid'
  | 'public_api_log_detail_no_store'
  | 'route_strategies_empty'
  | 'route_strategies_invalid'
  | 'route_strategy_detail_invalid'
  | 'route_strategies_failure'
  | 'route_strategies_timeout'
  | 'patch_failure'
  | 'cleanup_404'
  | 'patch_and_cleanup_failure'

interface MockGroup {
  id: string
  ownerSystemAccountId: string
  name: string
  providerCode: string
  description?: string
  enabled: boolean
  isDefault: boolean
  groupType: 'personal' | 'high_concurrency'
  accessType: 'owner' | 'authorized'
  accountIds: string[]
  schedulingPolicy?: Record<string, unknown>
}

const cookie = 'juhe_ai_session=regression-secret; another_cookie=opaque-value'
const systemAccountId = 'sys_plan0081_target'
const selectedGroupId = 'grp_plan0081_owner_secondary'
const temporaryGroupId = 'grp_plan0081_temporary'
const missingGroupId = 'grp_plan0081_missing_sensitive'
const missingProviderCode = 'missing-provider-sensitive'
const sensitiveClientIPHash = 'a'.repeat(64)
const explicitClientIPHash = 'd'.repeat(64)
const selectedRouteStrategyId = 'route_plan0081_normal_sensitive'
const missingRouteStrategyId = 'route_plan0081_missing_sensitive'
const configuredAccountId = 'acct_plan0081/encoded target?read-only'
const mismatchedAccountId = 'acct_plan0081_mismatched_sensitive'
const listedPublicApiLogId = 'publog_plan0081_list_sensitive'
const configuredPublicApiLogId = 'publog_plan0081/encoded target?read-only'
const selectedExternalIntegrationSourceId = 'extsrc_plan0081/encoded target?read-only'
const configuredExternalIntegrationSourceId = 'extsrc/plan0081?opt-in#%'
const configuredExternalIntegrationSourceTokenId = 'exttok/plan0081?revoked#%'
const externalIntegrationSourceTokenSecret = 'juis_Op1_plan0081_secret_value_revoked1'
const requestRecords: RequestRecord[] = []
const groups = new Map<string, MockGroup>()
let scenario: MockScenario = 'normal'
let patchFailureDelivered = false

const server = createServer((req, res) => {
  void handleRequest(req, res).catch(() => {
    if (!res.headersSent) {
      res.statusCode = 500
      res.setHeader('Cache-Control', 'no-store')
      res.setHeader('Content-Type', 'application/json; charset=utf-8')
    }
    res.end(JSON.stringify({ message: 'mock handler failed' }))
  })
})
await listen(server)

try {
  const baseUrl = serverBaseUrl(server)

  await assertPublicAPILogReadScenarios(baseUrl)
  await assertExternalIntegrationSourceCatalogReadScenarios(baseUrl)
  await assertExternalIntegrationSourceTokenSecretScenarios(baseUrl)
  await assertExternalIntegrationSourceApiEncoding()
  await assertLogBoundaryRedaction(baseUrl)
  await assertRouteStrategyReadScenarios(baseUrl)
  await assertApiKeyListReadScenarios(baseUrl)
  await assertReadOnlySmoke(baseUrl)
  await assertAccountTestOptionsReadSmoke(baseUrl)
  await assertAccountTestOptionsResponseRequirements(baseUrl)
  await assertClientIPRangeNotReadySmoke(baseUrl)
  await assertClientIPRangeEmptySmoke(baseUrl)
  await assertStrictClientIPDetailRequiresTarget(baseUrl)
  await assertStrictClientIPDetailResponseRequirements(baseUrl)
  await assertExplicitClientIPHashSmoke(baseUrl)
  await assertSuccessfulMutationSmoke(baseUrl)
  await assertPatchFailureStillCleansUp(baseUrl)
  await assertCleanup404IsIdempotent(baseUrl)
  await assertPrimaryAndCleanupErrorsArePreserved(baseUrl)
  await assertInvalidConfiguration(baseUrl)
} finally {
  await close(server)
}

console.log('PLAN-0081 real Go management smoke regression passed')

async function assertApiKeyListReadScenarios(baseUrl: string): Promise<void> {
  resetMock('normal')
  const output: string[] = []
  const summary = await runRealGoManagementSmokeFromEnvironment(
    smokeEnvironment(baseUrl),
    (message) => output.push(message)
  )
  assert.equal(summary.adminApiKeyCount, 2)
  assert.equal(summary.selfApiKeyCount, 1)
  assert.deepEqual(output, [formatRealGoManagementSmokeSummary(summary)])
  assert.match(output[0] ?? '', /adminApiKeyCount=2 selfApiKeyCount=1/)
  assert.equal(requestPaths().includes(adminApiKeysListPath()), true)
  assert.equal(requestPaths().includes(selfApiKeysListPath()), true)
  assert.equal(
    requestPaths().some((path) => path.startsWith('GET /__aisys__/api/my-api-keys?') && path.includes('systemAccountId=')),
    false
  )
  assert.equal(
    requestPaths().some((path) => /\/api-keys\/[^?]+/.test(path) || path.includes('/secret')),
    false
  )
  assertRequestHeaders()

  resetMock('api_keys_empty')
  const emptySummary = await runRealGoManagementSmokeFromEnvironment(smokeEnvironment(baseUrl), () => undefined)
  assert.deepEqual(emptySummary, {
    ...expectedSummary(),
    adminApiKeyCount: 0,
    selfApiKeyCount: 0
  })
  assert.equal(requestPaths().includes(adminApiKeysListPath()), true)
  assert.equal(requestPaths().includes(selfApiKeysListPath()), true)
  assertRequestHeaders()

  const failureCases = [
    ['api_keys_admin_owner_missing', /admin API keys list item 0\.systemAccountId must be a non-empty string/, [adminApiKeysListPath()]],
    ['api_keys_self_owner_leak', /self API keys list item 0 must not contain systemAccountId or systemAccountName/, [adminApiKeysListPath(), selfApiKeysListPath()]],
    ['api_keys_sensitive_field', /admin API keys list item 0 must not contain sensitive field keyHash/, [adminApiKeysListPath()]],
    ['api_keys_nested_preview_field', /admin API keys list item 0 must not contain sensitive field keyPrefix/, [adminApiKeysListPath()]],
    ['api_keys_pagination_invalid', /admin API keys list total must be the progressive first-page upper bound/, [adminApiKeysListPath()]]
  ] as const
  for (const [requestScenario, expectedMessage, apiKeyPaths] of failureCases) {
    resetMock(requestScenario)
    const failureMessage = await captureFailureMessage(
      runRealGoManagementSmokeFromEnvironment(smokeEnvironment(baseUrl), () => undefined)
    )
    assert.match(failureMessage, expectedMessage)
    const paths = requestPaths()
    assert.deepEqual(paths.slice(paths.indexOf(adminApiKeysListPath())), apiKeyPaths)
    assertRequestHeaders()
  }
}

async function assertExternalIntegrationSourceTokenSecretScenarios(baseUrl: string): Promise<void> {
  resetMock('external_integration_source_secret_success')
  const output: string[] = []
  const env = smokeEnvironment(baseUrl, {
    [realGoManagementSmokeEnv.externalIntegrationSourceId]: configuredExternalIntegrationSourceId,
    [realGoManagementSmokeEnv.externalIntegrationSourceTokenId]: configuredExternalIntegrationSourceTokenId
  })
  const loaded = loadRealGoManagementSmokeConfig(env)
  assert.equal(loaded.externalIntegrationSourceId, configuredExternalIntegrationSourceId)
  assert.equal(loaded.externalIntegrationSourceTokenId, configuredExternalIntegrationSourceTokenId)
  const summary = await runRealGoManagementSmokeFromEnvironment(env, (message) => output.push(message))
  assert.equal(summary.externalIntegrationSourceTokenSecretChecked, true)
  assert.equal(output.length, 1)
  assert.match(output[0] ?? '', /externalIntegrationSourceTokenSecretChecked=true/)
  assert.equal(output.some((line) => line.includes(externalIntegrationSourceTokenSecret)), false)
  const secretRequests = requestRecords.filter(
    (record) => `${record.method} ${record.url}` === externalIntegrationSourceTokenSecretPath()
  )
  assert.equal(secretRequests.length, 1)
  assert.equal(secretRequests[0]?.method, 'GET')
  assert.equal(secretRequests[0]?.body, undefined)
  assert.equal(requestPaths().includes(externalIntegrationSourceDetailPath(configuredExternalIntegrationSourceId)), true)
  assertRequestHeaders()

  for (const requestScenario of [
    'external_integration_source_secret_malformed',
    'external_integration_source_secret_empty',
    'external_integration_source_secret_preview_mismatch',
    'external_integration_source_secret_pragma_invalid'
  ] as const) {
    resetMock(requestScenario)
    const failureOutput: string[] = []
    const failureMessage = await captureFailureMessage(
      runRealGoManagementSmokeFromEnvironment(env, (message) => failureOutput.push(message))
    )
    assert.equal(failureMessage.includes(externalIntegrationSourceTokenSecret), false)
    assert.equal(failureOutput.some((line) => line.includes(externalIntegrationSourceTokenSecret)), false)
    assert.deepEqual(failureOutput, [])
    if (requestScenario === 'external_integration_source_secret_pragma_invalid') {
      assert.equal(failureMessage, 'external integration source token secret must return Pragma: no-cache')
    }
    assert.equal(
      requestRecords.filter((record) => `${record.method} ${record.url}` === externalIntegrationSourceTokenSecretPath()).length,
      1
    )
    assertRequestHeaders()
  }

  for (const [key, value] of [
    [realGoManagementSmokeEnv.externalIntegrationSourceId, configuredExternalIntegrationSourceId],
    [realGoManagementSmokeEnv.externalIntegrationSourceTokenId, configuredExternalIntegrationSourceTokenId]
  ] as const) {
    resetMock('normal')
    await assert.rejects(
      runRealGoManagementSmokeFromEnvironment(smokeEnvironment(baseUrl, { [key]: value }), () => undefined),
      /must be configured together/
    )
    assert.deepEqual(requestRecords, [])
  }
}

async function assertExternalIntegrationSourceApiEncoding(): Promise<void> {
  const capturedRequests: Array<{ method: string; url: string; body: unknown }> = []
  const originalAdapter = http.defaults.adapter
  const captureAdapter: AxiosAdapter = async (config) => {
    capturedRequests.push({
      method: String(config.method ?? '').toUpperCase(),
      url: String(config.url ?? ''),
      body: parseAxiosRequestBody(config.data)
    })
    return {
      data: {
        data: String(config.url ?? '').endsWith('/secret')
          ? { token: 'local-capture-token' }
          : {}
      },
      status: 200,
      statusText: 'OK',
      headers: {},
      config
    }
  }

  try {
    http.defaults.adapter = captureAdapter
    await externalIntegrationSourcesApi.detail(configuredExternalIntegrationSourceId)
    await externalIntegrationSourcesApi.update(configuredExternalIntegrationSourceId, { status: 'disabled' })
    await externalIntegrationSourcesApi.tokenSecret(
      configuredExternalIntegrationSourceId,
      configuredExternalIntegrationSourceTokenId
    )
  } finally {
    http.defaults.adapter = originalAdapter
  }

  assert.deepEqual(capturedRequests, [
    {
      method: 'GET',
      url: `/external-integration-sources/${encodeURIComponent(configuredExternalIntegrationSourceId)}`,
      body: undefined
    },
    {
      method: 'PATCH',
      url: `/external-integration-sources/${encodeURIComponent(configuredExternalIntegrationSourceId)}`,
      body: { status: 'disabled' }
    },
    {
      method: 'GET',
      url: `/external-integration-sources/${encodeURIComponent(configuredExternalIntegrationSourceId)}/tokens/${encodeURIComponent(configuredExternalIntegrationSourceTokenId)}/secret`,
      body: undefined
    }
  ])
}

function parseAxiosRequestBody(data: AxiosRequestConfig['data']): unknown {
  return typeof data === 'string' ? JSON.parse(data) as unknown : data
}

async function assertPublicAPILogReadScenarios(baseUrl: string): Promise<void> {
  resetMock('normal')
  const emptyOutput: string[] = []
  const emptySummary = await runRealGoManagementSmokeFromEnvironment(
    smokeEnvironment(baseUrl),
    (message) => emptyOutput.push(message)
  )
  assert.deepEqual(emptySummary, expectedSummary())
  assert.deepEqual(emptyOutput, [formatRealGoManagementSmokeSummary(emptySummary)])
  assert.equal(emptySummary.publicApiLogCount, 0)
  assert.equal(emptySummary.publicApiLogDetailChecked, false)
  assert.equal(requestPaths()[0], publicAPILogsListPath())
  assert.equal(requestPaths().some((path) => path.startsWith('GET /__aisys__/api/public-api-logs/')), false)
  assertRequestHeaders()

  resetMock('public_api_logs_non_empty')
  const listOnlySummary = await runRealGoManagementSmokeFromEnvironment(smokeEnvironment(baseUrl), () => undefined)
  assert.deepEqual(listOnlySummary, {
    ...expectedSummary(),
    publicApiLogCount: 1,
    publicApiLogDetailChecked: false
  })
  assert.equal(requestPaths()[0], publicAPILogsListPath())
  assert.equal(requestPaths().some((path) => path.startsWith('GET /__aisys__/api/public-api-logs/')), false)
  assertRequestHeaders()

  resetMock('public_api_logs_non_empty')
  const detailOutput: string[] = []
  const detailEnv = smokeEnvironment(baseUrl, {
    [realGoManagementSmokeEnv.publicApiLogId]: `  ${configuredPublicApiLogId}  `
  })
  const loadedConfig = loadRealGoManagementSmokeConfig(detailEnv)
  assert.equal(loadedConfig.publicApiLogId, configuredPublicApiLogId)
  const detailSummary = await runRealGoManagementSmokeFromEnvironment(
    detailEnv,
    (message) => detailOutput.push(message)
  )
  assert.deepEqual(detailSummary, {
    ...expectedSummary(),
    publicApiLogCount: 1,
    publicApiLogDetailChecked: true
  })
  assert.deepEqual(detailOutput, [formatRealGoManagementSmokeSummary(detailSummary)])
  assert.match(detailOutput[0] ?? '', /publicApiLogCount=1 publicApiLogDetailChecked=true$/)
  assert.equal(requestPaths()[0], publicAPILogsListPath())
  assert.equal(requestPaths()[1], publicAPILogDetailPath(configuredPublicApiLogId))
  assert.notEqual(listedPublicApiLogId, configuredPublicApiLogId)
  assertRequestHeaders()

  const listFailureCases = [
    ['public_api_logs_envelope_invalid', /public API logs list envelope must contain only the data field/],
    ['public_api_logs_items_invalid', /public API logs list items must be an array/],
    ['public_api_logs_pagination_invalid', /public API logs list total must be the progressive first-page upper bound/],
    ['public_api_logs_required_field_invalid', /public API logs list item 0\.method must be a non-empty string/],
    ['public_api_logs_optional_field_invalid', /public API logs list item 0\.traceId must be a string when present/],
    ['public_api_logs_capture_status_invalid', /public API logs list item 0\.requestCaptureStatus must be a valid capture status/],
    ['public_api_logs_no_store', /public API logs list must return Cache-Control: no-store/]
  ] as const
  for (const [requestScenario, expectedMessage] of listFailureCases) {
    resetMock(requestScenario)
    const failureMessage = await captureFailureMessage(
      runRealGoManagementSmokeFromEnvironment(smokeEnvironment(baseUrl), () => undefined)
    )
    assert.match(failureMessage, expectedMessage)
    assert.deepEqual(requestPaths(), [publicAPILogsListPath()])
    assertRequestHeaders()
  }

  const detailFailureCases = [
    ['public_api_log_detail_request_data_invalid', /public API log detail\.requestData must be a non-array object/],
    ['public_api_log_detail_response_data_invalid', /public API log detail\.responseData must be a non-array object/],
    ['public_api_log_detail_no_store', /public API log detail must return Cache-Control: no-store/]
  ] as const
  for (const [requestScenario, expectedMessage] of detailFailureCases) {
    resetMock(requestScenario)
    const failureMessage = await captureFailureMessage(
      runRealGoManagementSmokeFromEnvironment(smokeEnvironment(baseUrl, {
        [realGoManagementSmokeEnv.publicApiLogId]: configuredPublicApiLogId
      }), () => undefined)
    )
    assert.match(failureMessage, expectedMessage)
    assert.deepEqual(requestPaths(), [publicAPILogsListPath(), publicAPILogDetailPath(configuredPublicApiLogId)])
    assertRequestHeaders()
  }

  assertNoEnvironmentIdentifierLeak(
    [...emptyOutput, ...detailOutput],
    baseUrl,
    [listedPublicApiLogId, configuredPublicApiLogId]
  )
}

async function assertExternalIntegrationSourceCatalogReadScenarios(baseUrl: string): Promise<void> {
  resetMock('external_integration_source_detail_success')
  await runRealGoManagementSmokeFromEnvironment(smokeEnvironment(baseUrl), () => undefined)

  const catalogRequests = requestRecords.filter((record) =>
    record.url?.startsWith('/__aisys__/api/external-integration-sources')
  )
  assert.deepEqual(
    catalogRequests.map((record) => `${record.method} ${record.url}`),
    [
      externalIntegrationSourceScopesPath(),
      externalIntegrationSourceApiDocsPath(),
      externalIntegrationSourcesListPath(),
      externalIntegrationSourceDetailPath()
    ]
  )
  assert.equal(catalogRequests.every((record) => record.method === 'GET' && record.body === undefined), true)
  assertExternalIntegrationSourceDetailRequest()
  assertNoExternalIntegrationSourceSecretRequest()
  assertRequestHeaders()

  resetMock('external_integration_source_detail_no_non_built_in')
  await runRealGoManagementSmokeFromEnvironment(smokeEnvironment(baseUrl), () => undefined)
  assert.deepEqual(
    requestPaths().filter((path) => path.startsWith('GET /__aisys__/api/external-integration-sources')),
    [
      externalIntegrationSourceScopesPath(),
      externalIntegrationSourceApiDocsPath(),
      externalIntegrationSourcesListPath()
    ]
  )
  assertNoExternalIntegrationSourceSecretRequest()
  assertRequestHeaders()

  resetMock('external_integration_source_detail_newer_snapshot')
  await runRealGoManagementSmokeFromEnvironment(smokeEnvironment(baseUrl), () => undefined)
  assert.deepEqual(
    requestPaths().filter((path) => path.startsWith('GET /__aisys__/api/external-integration-sources')),
    [
      externalIntegrationSourceScopesPath(),
      externalIntegrationSourceApiDocsPath(),
      externalIntegrationSourcesListPath(),
      externalIntegrationSourceDetailPath()
    ]
  )
  assertExternalIntegrationSourceDetailRequest()
  assertNoExternalIntegrationSourceSecretRequest()
  assertRequestHeaders()

  const detailCases = [
    [
      'external_integration_source_detail_id_mismatch',
      /external integration source detail.*id.*list/i
    ],
    [
      'external_integration_source_detail_edit_field_mismatch',
      /external integration source detail.*notes.*list/i
    ],
    [
      'external_integration_source_detail_unknown_field',
      /external integration source detail.*undocumented field debugMetadata/i
    ],
    [
      'external_integration_source_detail_tokens_not_array',
      /external integration source detail.*tokens must be an array/i
    ],
    [
      'external_integration_source_detail_token_count_mismatch',
      /external integration source detail.*tokenCount.*tokens/i
    ],
    [
      'external_integration_source_detail_active_token_count_mismatch',
      /external integration source detail.*activeTokenCount.*active/i
    ],
    [
      'external_integration_source_detail_created_at_unsorted',
      /external integration source detail.*tokens.*createdAt.*id.*descending/i
    ],
    [
      'external_integration_source_detail_id_unsorted',
      /external integration source detail.*tokens.*createdAt.*id.*descending/i
    ],
    [
      'external_integration_source_detail_token_unknown_field',
      /external integration source detail.*token.*undocumented field debugMetadata/i
    ],
    [
      'external_integration_source_detail_sensitive_token',
      /external integration source detail.*token.*undocumented field token/i
    ],
    [
      'external_integration_source_detail_sensitive_hash',
      /external integration source detail.*token.*undocumented field tokenHash/i
    ],
    [
      'external_integration_source_detail_sensitive_ciphertext',
      /external integration source detail.*token.*undocumented field tokenSecretEncrypted/i
    ],
    [
      'external_integration_source_detail_sensitive_preview_value',
      /external integration source detail.*tokenPrefix must be an 8-character juis_ preview/i
    ],
    [
      'external_integration_source_detail_sensitive_primary_token',
      /external integration source detail.*undocumented field primaryToken/i
    ]
  ] as const
  for (const [requestScenario, expectedMessage] of detailCases) {
    resetMock(requestScenario)
    const output: string[] = []
    const failureMessage = await captureFailureMessage(
      runRealGoManagementSmokeFromEnvironment(
        smokeEnvironment(baseUrl),
        (message) => output.push(message)
      )
    )

    assert.match(failureMessage, expectedMessage)
    assert.deepEqual(output, [])
    assert.deepEqual(requestPaths(), [
      publicAPILogsListPath(),
      externalIntegrationSourceScopesPath(),
      externalIntegrationSourceApiDocsPath(),
      externalIntegrationSourcesListPath(),
      externalIntegrationSourceDetailPath()
    ])
    assertExternalIntegrationSourceDetailRequest()
    assertNoExternalIntegrationSourceSecretRequest()
    assertRequestHeaders()
  }

  const scopeCases = [
    [
      'external_integration_source_scopes_missing_item',
      /external integration source scopes data must contain exactly 16 items/
    ],
    [
      'external_integration_source_scopes_invalid_field',
      /external integration source scopes data item 0\.label must be a string/
    ]
  ] as const
  for (const [requestScenario, expectedMessage] of scopeCases) {
    resetMock(requestScenario)
    const output: string[] = []
    const failureMessage = await captureFailureMessage(
      runRealGoManagementSmokeFromEnvironment(
        smokeEnvironment(baseUrl),
        (message) => output.push(message)
      )
    )

    assert.match(failureMessage, expectedMessage)
    assert.deepEqual(output, [])
    assert.deepEqual(requestPaths(), [publicAPILogsListPath(), externalIntegrationSourceScopesPath()])
    assert.equal(requestRecords.every((record) => record.method === 'GET' && record.body === undefined), true)
    assertRequestHeaders()
  }

  const apiDocsCases = [
    [
      'external_integration_source_api_docs_base_path_invalid',
      /external integration source api docs data\.basePath must equal \/__aipublic__/
    ],
    [
      'external_integration_source_api_docs_auth_type_invalid',
      /external integration source api docs data\.authType must equal Bearer/
    ],
    [
      'external_integration_source_api_docs_missing_item',
      /external integration source api docs data\.items must contain exactly 16 items/
    ],
    [
      'external_integration_source_api_docs_get_contract_invalid',
      /api-key-list\.scope must equal juhe_ai_public:api_key_list:read/
    ],
    [
      'external_integration_source_api_docs_get_fields_empty',
      /api-key-list\.responseFields must be a non-empty array/
    ],
    [
      'external_integration_source_api_docs_post_body_missing',
      /api-key-add\.requestBody must be an object/
    ],
    [
      'external_integration_source_api_docs_last_item_fields_empty',
      /account-delete\.responseFields must be a non-empty array/
    ]
  ] as const
  for (const [requestScenario, expectedMessage] of apiDocsCases) {
    resetMock(requestScenario)
    const output: string[] = []
    const failureMessage = await captureFailureMessage(
      runRealGoManagementSmokeFromEnvironment(
        smokeEnvironment(baseUrl),
        (message) => output.push(message)
      )
    )

    assert.match(failureMessage, expectedMessage)
    assert.deepEqual(output, [])
    assert.deepEqual(requestPaths(), [
      publicAPILogsListPath(),
      externalIntegrationSourceScopesPath(),
      externalIntegrationSourceApiDocsPath()
    ])
    assert.equal(requestRecords.every((record) => record.method === 'GET' && record.body === undefined), true)
    assertRequestHeaders()
  }

  const listCases = [
    [
      'external_integration_source_list_missing_field',
      /external integration source list item 0\.primaryToken must be present when tokenCount is positive/
    ],
    [
      'external_integration_source_list_rate_limits_unsorted',
      /external integration source list item 0\.rateLimits must be sorted by windowSeconds ascending/
    ],
    [
      'external_integration_source_list_sensitive_field',
      /external integration source list item 0\.primaryToken must not contain undocumented field TokenHash/
    ],
    [
      'external_integration_source_list_primary_status_invalid',
      /external integration source list item 0\.primaryToken\.status must be active, disabled, or revoked/
    ],
    [
      'external_integration_source_list_primary_not_active',
      /external integration source list item 0\.primaryToken\.status must be active when activeTokenCount is positive/
    ],
    [
      'external_integration_source_list_primary_time_invalid',
      /external integration source list item 0\.primaryToken\.createdAt must be a canonical UTC ISO timestamp/
    ]
  ] as const
  for (const [requestScenario, expectedMessage] of listCases) {
    resetMock(requestScenario)
    const output: string[] = []
    const failureMessage = await captureFailureMessage(
      runRealGoManagementSmokeFromEnvironment(
        smokeEnvironment(baseUrl),
        (message) => output.push(message)
      )
    )

    assert.match(failureMessage, expectedMessage)
    assert.deepEqual(output, [])
    assert.deepEqual(requestPaths(), [
      publicAPILogsListPath(),
      externalIntegrationSourceScopesPath(),
      externalIntegrationSourceApiDocsPath(),
      externalIntegrationSourcesListPath()
    ])
    assert.equal(requestRecords.every((record) => record.method === 'GET' && record.body === undefined), true)
    assertRequestHeaders()
  }

  resetMock('external_integration_source_list_pagination')
  await runRealGoManagementSmokeFromEnvironment(smokeEnvironment(baseUrl), () => undefined)
  assert.deepEqual(
    requestPaths().filter((path) => path.startsWith('GET /__aisys__/api/external-integration-sources')),
    [
      externalIntegrationSourceScopesPath(),
      externalIntegrationSourceApiDocsPath(),
      externalIntegrationSourcesListPath(1),
      externalIntegrationSourceDetailPath('extsrc_plan0081_page_01'),
      externalIntegrationSourcesListPath(2)
    ]
  )
  assertExternalIntegrationSourceDetailRequest('extsrc_plan0081_page_01')
  assertNoExternalIntegrationSourceSecretRequest()
  assertRequestHeaders()

  resetMock('external_integration_source_list_pagination_duplicate')
  const paginationFailureMessage = await captureFailureMessage(
    runRealGoManagementSmokeFromEnvironment(smokeEnvironment(baseUrl), () => undefined)
  )
  assert.match(
    paginationFailureMessage,
    /external integration source list pages must not contain duplicate id extsrc_plan0081_page_01/
  )
  assert.deepEqual(requestPaths(), [
    publicAPILogsListPath(),
    externalIntegrationSourceScopesPath(),
    externalIntegrationSourceApiDocsPath(),
    externalIntegrationSourcesListPath(1),
    externalIntegrationSourceDetailPath('extsrc_plan0081_page_01'),
    externalIntegrationSourcesListPath(2)
  ])
  assertExternalIntegrationSourceDetailRequest('extsrc_plan0081_page_01')
  assertNoExternalIntegrationSourceSecretRequest()
  assertRequestHeaders()
}

async function assertLogBoundaryRedaction(baseUrl: string): Promise<void> {
  const messages: string[] = []

  resetMock('normal')
  await runRealGoManagementSmokeFromEnvironment(
    smokeEnvironment(baseUrl, {
      [realGoManagementSmokeEnv.allowGroupMutations]: '0'
    }),
    (message) => messages.push(message)
  )

  resetMock('normal')
  const groupFailureMessage = await captureFailureMessage(
    runRealGoManagementSmokeFromEnvironment(smokeEnvironment(baseUrl, {
      [realGoManagementSmokeEnv.groupId]: missingGroupId
    }), () => undefined)
  )
  assert.match(groupFailureMessage, /Configured group was not returned by groups list/)
  messages.push(groupFailureMessage)

  resetMock('normal')
  const providerFailureMessage = await captureFailureMessage(
    runRealGoManagementSmokeFromEnvironment(mutationEnvironment(baseUrl, {
      [realGoManagementSmokeEnv.providerCode]: missingProviderCode
    }), () => undefined)
  )
  assert.match(providerFailureMessage, /Mutation provider was not returned by providers\/options/)
  messages.push(providerFailureMessage)

  resetMock('ip_stats_failure')
  const clientIPFailureMessage = await captureFailureMessage(
    runRealGoManagementSmokeFromEnvironment(smokeEnvironment(baseUrl), () => undefined)
  )
  assert.equal(clientIPFailureMessage, 'client IP stats list failed with HTTP 503')
  messages.push(clientIPFailureMessage)

  resetMock('ip_stats_timeout')
  const clientIPTimeoutMessage = await captureFailureMessage(
    runRealGoManagementSmokeFromEnvironment(smokeEnvironment(baseUrl, {
      [realGoManagementSmokeEnv.timeoutMs]: '250'
    }), () => undefined)
  )
  assert.match(clientIPTimeoutMessage, /^client IP stats list request failed: (TimeoutError|AbortError)$/)
  messages.push(clientIPTimeoutMessage)

  const credentialedBaseUrl = 'https://smoke-user:smoke-password@example.test/private'
  const baseUrlFailureMessage = await captureFailureMessage(
    runRealGoManagementSmokeFromEnvironment(smokeEnvironment(credentialedBaseUrl), () => undefined)
  )
  assert.match(baseUrlFailureMessage, /must not contain credentials/)
  messages.push(baseUrlFailureMessage)

  assertNoEnvironmentIdentifierLeak(messages, baseUrl, [
    missingGroupId,
    missingProviderCode,
    'smoke-user',
    'smoke-password',
    credentialedBaseUrl
  ])
}

async function assertRouteStrategyReadScenarios(baseUrl: string): Promise<void> {
  resetMock('route_strategies_empty')
  const emptyOutput: string[] = []
  const emptySummary = await runRealGoManagementSmokeFromEnvironment(
    smokeEnvironment(baseUrl),
    (message) => emptyOutput.push(message)
  )
  assert.deepEqual(emptySummary, expectedSummary(true, 3, true, 0, false))
  assert.deepEqual(requestPaths(), [
    publicAPILogsListPath(),
    externalIntegrationSourceScopesPath(),
    externalIntegrationSourceApiDocsPath(),
    externalIntegrationSourcesListPath(),
    groupsListPath(), groupDetailPath(selectedGroupId), routeStrategiesListPath(),
    providersPath(), modelOptionsPath(), adminApiKeysListPath(), selfApiKeysListPath(), clientIPStatsPath(), clientIPStatsDetailPath()
  ])
  assertNoEnvironmentIdentifierLeak(emptyOutput, baseUrl)

  resetMock('route_strategies_empty')
  const explicitEnv = smokeEnvironment(baseUrl, {
    [realGoManagementSmokeEnv.routeStrategyId]: missingRouteStrategyId
  })
  assert.equal(loadRealGoManagementSmokeConfig(explicitEnv).routeStrategyId, missingRouteStrategyId)
  const missingMessage = await captureFailureMessage(
    runRealGoManagementSmokeFromEnvironment(explicitEnv, () => undefined)
  )
  assert.equal(missingMessage, 'Configured route strategy was not returned by route strategies list')
  assert.deepEqual(requestPaths(), [
    publicAPILogsListPath(), externalIntegrationSourceScopesPath(), externalIntegrationSourceApiDocsPath(),
    externalIntegrationSourcesListPath(),
    groupsListPath(), groupDetailPath(selectedGroupId), routeStrategiesListPath()
  ])

  const safeFailures = [missingMessage]
  const cases = [
    ['route_strategies_invalid', /route strategies list item 0 must not expose groupBindings/],
    ['route_strategy_detail_invalid', /route strategy detail\.apiKeyCount must match the list item/],
    ['route_strategies_failure', /^route strategies list failed with HTTP 503$/],
    ['route_strategies_timeout', /^route strategies list request failed: (TimeoutError|AbortError)$/, '250']
  ] as const
  for (const [requestScenario, expected, timeoutMs] of cases) {
    resetMock(requestScenario)
    const message = await captureFailureMessage(
      runRealGoManagementSmokeFromEnvironment(smokeEnvironment(baseUrl, timeoutMs
        ? { [realGoManagementSmokeEnv.timeoutMs]: timeoutMs }
        : {}), () => undefined)
    )
    assert.match(message, expected)
    safeFailures.push(message)
  }

  assertNoEnvironmentIdentifierLeak(safeFailures, baseUrl)
}

async function assertReadOnlySmoke(baseUrl: string): Promise<void> {
  resetMock('normal')
  const output: string[] = []
  const env = smokeEnvironment(baseUrl, {
    [realGoManagementSmokeEnv.accountId]: '   ',
    [realGoManagementSmokeEnv.allowGroupMutations]: '0',
    [realGoManagementSmokeEnv.requireClientIpDetail]: '1'
  })
  const loadedConfig = loadRealGoManagementSmokeConfig(env)
  assert.equal(loadedConfig.accountId, undefined)
  assert.equal(loadedConfig.requireClientIpDetail, true)
  assert.equal(loadedConfig.clientIpHash, undefined)
  const summary = await runRealGoManagementSmokeFromEnvironment(env, (message) => output.push(message))

  assert.deepEqual(summary, expectedSummary())
  assert.deepEqual(output, [formatRealGoManagementSmokeSummary(summary)])
  assert.equal(
    output[0],
    'PLAN-0081 real Go management smoke passed groups=3 providers=2 modelOptions=2 adminApiKeyCount=2 selfApiKeyCount=1 externalIntegrationSourceTokenSecretChecked=false clientIpItems=3 clientIpRangeReady=true clientIpDetailChecked=true routeStrategies=1 routeStrategyDetailChecked=true accountTestOptionsChecked=false publicApiLogCount=0 publicApiLogDetailChecked=false'
  )
  assert.deepEqual(requestPaths(), [
    publicAPILogsListPath(),
    externalIntegrationSourceScopesPath(),
    externalIntegrationSourceApiDocsPath(),
    externalIntegrationSourcesListPath(),
    groupsListPath(), groupDetailPath(selectedGroupId), routeStrategiesListPath(), routeStrategyDetailPath(),
    providersPath(), modelOptionsPath(), adminApiKeysListPath(), selfApiKeysListPath(), clientIPStatsPath(), clientIPStatsDetailPath()
  ])
  assert.equal(requestRecords.every((record) => record.method === 'GET'), true)
  assertNoCookieLeak(output)
  assertNoEnvironmentIdentifierLeak(output, baseUrl)
  assertRequestHeaders()
}

async function assertAccountTestOptionsReadSmoke(baseUrl: string): Promise<void> {
  resetMock('normal')
  const output: string[] = []
  const env = smokeEnvironment(baseUrl, {
    [realGoManagementSmokeEnv.accountId]: `  ${configuredAccountId}  `
  })
  const loadedConfig = loadRealGoManagementSmokeConfig(env)
  assert.equal(loadedConfig.accountId, configuredAccountId)

  const summary = await runRealGoManagementSmokeFromEnvironment(env, (message) => output.push(message))
  assert.deepEqual(summary, expectedSummary(true, 3, true, 1, true, true))
  assert.deepEqual(output, [formatRealGoManagementSmokeSummary(summary)])
  assert.deepEqual(requestPaths(), [
    publicAPILogsListPath(),
    externalIntegrationSourceScopesPath(),
    externalIntegrationSourceApiDocsPath(),
    externalIntegrationSourcesListPath(),
    groupsListPath(), groupDetailPath(selectedGroupId), routeStrategiesListPath(), routeStrategyDetailPath(),
    providersPath(), modelOptionsPath(), adminApiKeysListPath(), selfApiKeysListPath(), accountTestOptionsPath(), clientIPStatsPath(), clientIPStatsDetailPath()
  ])

  const accountTestOptionsRequests = requestRecords.filter((record) =>
    record.url?.startsWith(`/__aisys__/api/accounts/${encodeURIComponent(configuredAccountId)}/test-options`)
  )
  assert.equal(accountTestOptionsRequests.length, 1)
  assert.equal(accountTestOptionsRequests[0]?.method, 'GET')
  assert.equal(accountTestOptionsRequests[0]?.body, undefined)
  assert.equal(requestRecords.every((record) => record.method === 'GET'), true)
  assert.equal(
    requestRecords.some((record) => /\/accounts\/[^/]+\/test(?:\?|$)/.test(record.url ?? '')),
    false,
    'account test-options smoke must not call the account test mutation endpoint'
  )
  assertNoCookieLeak(output)
  assertNoEnvironmentIdentifierLeak(output, baseUrl)
  assertRequestHeaders()
}

async function assertAccountTestOptionsResponseRequirements(baseUrl: string): Promise<void> {
  const cases = [
    ['account_test_options_not_object', /account test options data must be an object/],
    ['account_test_options_account_mismatch', /account test options data\.accountId must match the configured account/],
    ['account_test_options_default_model_empty', /account test options data\.defaultModel must be a non-empty string/],
    ['account_test_options_models_empty', /account test options data\.models must be a non-empty array/],
    ['account_test_options_model_empty', /account test options data\.models item 0\.model must be a non-empty string/],
    ['account_test_options_protocols_invalid', /account test options data\.models item 0\.supportedApiProtocols must contain only strings/],
    ['account_test_options_model_endpoint_modes_missing', /account test options data\.models item 0\.testEndpointModes must be a non-empty array/],
    ['account_test_options_model_endpoint_modes_empty', /account test options data\.models item 0\.testEndpointModes must be a non-empty array/],
    ['account_test_options_model_endpoint_mode_invalid', /account test options data\.models item 0\.testEndpointModes must contain only legal account test endpoint modes/],
    ['account_test_options_default_model_missing', /account test options data\.defaultModel must reference a model/],
    ['account_test_options_endpoint_modes_empty', /account test options data\.testEndpointModes must be a non-empty array/],
    ['account_test_options_endpoint_mode_invalid', /account test options data\.testEndpointModes must contain only legal account test endpoint modes/],
    ['account_test_options_default_model_modes_mismatch', /account test options data\.testEndpointModes must equal the default model testEndpointModes/],
    ['account_test_options_default_endpoint_mode_mismatch', /account test options data\.defaultTestEndpointMode must equal the first testEndpointModes item/]
  ] as const

  for (const [requestScenario, expectedMessage] of cases) {
    resetMock(requestScenario)
    const output: string[] = []
    const failureMessage = await captureFailureMessage(
      runRealGoManagementSmokeFromEnvironment(smokeEnvironment(baseUrl, {
        [realGoManagementSmokeEnv.accountId]: configuredAccountId
      }), (message) => output.push(message))
    )

    assert.match(failureMessage, expectedMessage)
    assert.deepEqual(output, [])
    assert.deepEqual(requestPaths(), [
      publicAPILogsListPath(),
      externalIntegrationSourceScopesPath(),
      externalIntegrationSourceApiDocsPath(),
      externalIntegrationSourcesListPath(),
      groupsListPath(), groupDetailPath(selectedGroupId), routeStrategiesListPath(), routeStrategyDetailPath(),
      providersPath(), modelOptionsPath(), adminApiKeysListPath(), selfApiKeysListPath(), accountTestOptionsPath()
    ])
    assert.equal(requestRecords.every((record) => record.method === 'GET' && record.body === undefined), true)
    assertNoEnvironmentIdentifierLeak([failureMessage], baseUrl)
    assertRequestHeaders()
  }
}

async function assertStrictClientIPDetailRequiresTarget(baseUrl: string): Promise<void> {
  for (const requestScenario of ['ip_stats_not_ready', 'ip_stats_empty'] as const) {
    resetMock(requestScenario)
    const output: string[] = []
    const failureMessage = await captureFailureMessage(
      runRealGoManagementSmokeFromEnvironment(smokeEnvironment(baseUrl, {
        [realGoManagementSmokeEnv.requireClientIpDetail]: '1'
      }), (message) => output.push(message))
    )

    assert.equal(
      failureMessage,
      `client IP detail is required but no verifiable target is available; set ${realGoManagementSmokeEnv.clientIpHash} to a known 64-character hexadecimal hash`
    )
    assert.deepEqual(output, [])
    assert.deepEqual(requestPaths(), [
      publicAPILogsListPath(),
      externalIntegrationSourceScopesPath(),
      externalIntegrationSourceApiDocsPath(),
      externalIntegrationSourcesListPath(),
      groupsListPath(), groupDetailPath(selectedGroupId), routeStrategiesListPath(), routeStrategyDetailPath(),
      providersPath(), modelOptionsPath(), adminApiKeysListPath(), selfApiKeysListPath(), clientIPStatsPath()
    ])
    assertNoEnvironmentIdentifierLeak([failureMessage], baseUrl)
    assertRequestHeaders()
  }
}

async function assertStrictClientIPDetailResponseRequirements(baseUrl: string): Promise<void> {
  const cases = [
    {
      scenario: 'ip_stats_detail_not_ready',
      expectedMessage: 'client IP stats detail is required but rangeReady is false'
    },
    {
      scenario: 'ip_stats_detail_empty',
      expectedMessage: 'client IP stats detail is required but items is empty'
    }
  ] as const

  for (const testCase of cases) {
    resetMock(testCase.scenario)
    const output: string[] = []
    const failureMessage = await captureFailureMessage(
      runRealGoManagementSmokeFromEnvironment(smokeEnvironment(baseUrl, {
        [realGoManagementSmokeEnv.requireClientIpDetail]: '1'
      }), (message) => output.push(message))
    )

    assert.equal(failureMessage, testCase.expectedMessage)
    assert.deepEqual(output, [])
    assert.deepEqual(requestPaths(), [
      publicAPILogsListPath(),
      externalIntegrationSourceScopesPath(),
      externalIntegrationSourceApiDocsPath(),
      externalIntegrationSourcesListPath(),
      groupsListPath(), groupDetailPath(selectedGroupId), routeStrategiesListPath(), routeStrategyDetailPath(),
      providersPath(), modelOptionsPath(), adminApiKeysListPath(), selfApiKeysListPath(), clientIPStatsPath(), clientIPStatsDetailPath()
    ])
    assertNoEnvironmentIdentifierLeak([failureMessage], baseUrl)
    assertRequestHeaders()
  }
}

async function assertExplicitClientIPHashSmoke(baseUrl: string): Promise<void> {
  const cases = [
    { scenario: 'ip_stats_empty', expectedItemCount: 0 },
    { scenario: 'ip_stats_detail_not_ready', expectedItemCount: 3 },
    { scenario: 'ip_stats_detail_empty', expectedItemCount: 3 }
  ] as const

  for (const testCase of cases) {
    resetMock(testCase.scenario)
    const output: string[] = []
    const env = smokeEnvironment(baseUrl, {
      [realGoManagementSmokeEnv.clientIpHash]: explicitClientIPHash.toUpperCase()
    })
    const loadedConfig = loadRealGoManagementSmokeConfig(env)
    assert.equal(loadedConfig.clientIpHash, explicitClientIPHash)
    assert.equal(loadedConfig.requireClientIpDetail, false)

    const summary = await runRealGoManagementSmokeFromEnvironment(env, (message) => output.push(message))
    assert.deepEqual(summary, expectedSummary(true, testCase.expectedItemCount, true))
    assert.deepEqual(output, [formatRealGoManagementSmokeSummary(summary)])
    assert.deepEqual(requestPaths(), [
      publicAPILogsListPath(),
      externalIntegrationSourceScopesPath(),
      externalIntegrationSourceApiDocsPath(),
      externalIntegrationSourcesListPath(),
      groupsListPath(), groupDetailPath(selectedGroupId), routeStrategiesListPath(), routeStrategyDetailPath(),
      providersPath(), modelOptionsPath(), adminApiKeysListPath(), selfApiKeysListPath(), clientIPStatsPath(), clientIPStatsDetailPath(explicitClientIPHash)
    ])
    assertNoEnvironmentIdentifierLeak(output, baseUrl)
    assertRequestHeaders()
  }
}

async function assertClientIPRangeNotReadySmoke(baseUrl: string): Promise<void> {
  resetMock('ip_stats_not_ready')
  const output: string[] = []
  const summary = await runRealGoManagementSmokeFromEnvironment(
    smokeEnvironment(baseUrl),
    (message) => output.push(message)
  )

  assert.deepEqual(summary, expectedSummary(false))
  assert.deepEqual(output, [formatRealGoManagementSmokeSummary(summary)])
  assert.equal(
    output[0],
    'PLAN-0081 real Go management smoke passed groups=3 providers=2 modelOptions=2 adminApiKeyCount=2 selfApiKeyCount=1 externalIntegrationSourceTokenSecretChecked=false clientIpItems=0 clientIpRangeReady=false clientIpDetailChecked=false routeStrategies=1 routeStrategyDetailChecked=true accountTestOptionsChecked=false publicApiLogCount=0 publicApiLogDetailChecked=false'
  )
  assert.deepEqual(requestPaths(), [
    publicAPILogsListPath(),
    externalIntegrationSourceScopesPath(),
    externalIntegrationSourceApiDocsPath(),
    externalIntegrationSourcesListPath(),
    groupsListPath(), groupDetailPath(selectedGroupId), routeStrategiesListPath(), routeStrategyDetailPath(),
    providersPath(), modelOptionsPath(), adminApiKeysListPath(), selfApiKeysListPath(), clientIPStatsPath()
  ])
  assert.equal(requestRecords.every((record) => record.method === 'GET'), true)
  assertNoEnvironmentIdentifierLeak(output, baseUrl)
  assertRequestHeaders()
}

async function assertClientIPRangeEmptySmoke(baseUrl: string): Promise<void> {
  resetMock('ip_stats_empty')
  const output: string[] = []
  const summary = await runRealGoManagementSmokeFromEnvironment(
    smokeEnvironment(baseUrl),
    (message) => output.push(message)
  )

  assert.deepEqual(summary, expectedSummary(true, 0))
  assert.deepEqual(output, [formatRealGoManagementSmokeSummary(summary)])
  assert.equal(
    output[0],
    'PLAN-0081 real Go management smoke passed groups=3 providers=2 modelOptions=2 adminApiKeyCount=2 selfApiKeyCount=1 externalIntegrationSourceTokenSecretChecked=false clientIpItems=0 clientIpRangeReady=true clientIpDetailChecked=false routeStrategies=1 routeStrategyDetailChecked=true accountTestOptionsChecked=false publicApiLogCount=0 publicApiLogDetailChecked=false'
  )
  assert.deepEqual(requestPaths(), [
    publicAPILogsListPath(),
    externalIntegrationSourceScopesPath(),
    externalIntegrationSourceApiDocsPath(),
    externalIntegrationSourcesListPath(),
    groupsListPath(), groupDetailPath(selectedGroupId), routeStrategiesListPath(), routeStrategyDetailPath(),
    providersPath(), modelOptionsPath(), adminApiKeysListPath(), selfApiKeysListPath(), clientIPStatsPath()
  ])
  assert.equal(requestRecords.every((record) => record.method === 'GET'), true)
  assertNoEnvironmentIdentifierLeak(output, baseUrl)
  assertRequestHeaders()
}

async function assertSuccessfulMutationSmoke(baseUrl: string): Promise<void> {
  resetMock('normal')
  const output: string[] = []
  const env = mutationEnvironment(baseUrl)
  const loadedConfig = loadRealGoManagementSmokeConfig(env)
  assert.equal(loadedConfig.allowGroupMutations, true)
  assert.equal(loadedConfig.providerCode, 'openai')
  assert.equal(loadedConfig.timeoutMs, 2_500)

  const summary = await runRealGoManagementSmokeFromEnvironment(env, (message) => output.push(message))
  assert.deepEqual(summary, expectedSummary())
  assert.deepEqual(output, [formatRealGoManagementSmokeSummary(summary)])
  assert.equal(groups.has(temporaryGroupId), false)
  assert.deepEqual(requestPaths(), [
    publicAPILogsListPath(),
    externalIntegrationSourceScopesPath(),
    externalIntegrationSourceApiDocsPath(),
    externalIntegrationSourcesListPath(),
    groupsListPath(), groupDetailPath(selectedGroupId), routeStrategiesListPath(), routeStrategyDetailPath(),
    providersPath(), modelOptionsPath(), adminApiKeysListPath(), selfApiKeysListPath(), clientIPStatsPath(), clientIPStatsDetailPath(),
    groupsCreatePath(),
    groupsListPath(),
    groupDetailPath(temporaryGroupId),
    groupPatchPath(temporaryGroupId),
    groupDetailPath(temporaryGroupId),
    groupDeletePath(temporaryGroupId),
    groupDetailPath(temporaryGroupId),
    groupDetailPath(temporaryGroupId)
  ])

  const createRequest = requestRecords.find((record) => record.method === 'POST')
  assert(createRequest)
  assert.deepEqual(
    omitDynamicName(createRequest.body),
    {
      providerCode: 'openai',
      enabled: true,
      groupType: 'personal'
    }
  )
  assert.match(
    String(recordBody(createRequest).name),
    /^PLAN-0081 real Go management smoke [0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/
  )
  const patchRequest = requestRecords.find((record) => record.method === 'PATCH')
  assert.deepEqual(patchRequest?.body, {
    name: `${String(recordBody(createRequest).name)} updated`,
    description: 'PLAN-0081 W5 group CRUD real Go smoke',
    groupType: 'high_concurrency',
    schedulingPolicy: {
      defaultSoftConcurrency: 7,
      maxQueueWaitMs: 45_000,
      clientIpConcurrencyLimit: 3,
      clientIpConcurrencyOverflowMode: 'queue',
      imageLaneMaxConcurrency: 2
    }
  })
  assertNoCookieLeak(output)
  assertNoEnvironmentIdentifierLeak(output, baseUrl)
  assertRequestHeaders()
}

async function assertPatchFailureStillCleansUp(baseUrl: string): Promise<void> {
  resetMock('patch_failure')
  const output: string[] = []
  let failure: unknown
  try {
    await runRealGoManagementSmokeFromEnvironment(mutationEnvironment(baseUrl), (message) => output.push(message))
  } catch (error) {
    failure = error
  }

  assert(failure instanceof Error)
  assert.match(failure.message, /temporary group PATCH failed with HTTP 503/)
  assertNoEnvironmentIdentifierLeak([failure.message], baseUrl)
  assert.deepEqual(output, [])
  assert.equal(groups.has(temporaryGroupId), false, 'finally cleanup must remove the PATCH-mutated group')
  assert.deepEqual(requestPaths().slice(-3), [
    groupPatchPath(temporaryGroupId),
    groupDetailPath(temporaryGroupId),
    groupDeletePath(temporaryGroupId)
  ])
  assertRequestHeaders()
}

async function assertCleanup404IsIdempotent(baseUrl: string): Promise<void> {
  resetMock('cleanup_404')
  let failure: unknown
  try {
    await runRealGoManagementSmokeFromEnvironment(mutationEnvironment(baseUrl), () => undefined)
  } catch (error) {
    failure = error
  }

  assert(failure instanceof Error)
  assert.match(failure.message, /temporary group PATCH failed with HTTP 503/)
  assert.doesNotMatch(failure.message, /cleanup failed/)
  assertNoEnvironmentIdentifierLeak([failure.message], baseUrl)
  assert.equal(groups.has(temporaryGroupId), false)
  assert.deepEqual(requestPaths().slice(-3), [
    groupPatchPath(temporaryGroupId),
    groupDetailPath(temporaryGroupId),
    groupDeletePath(temporaryGroupId)
  ])
  assert.equal(requestRecords.at(-1)?.method, 'DELETE')
  assertRequestHeaders()
}

async function assertPrimaryAndCleanupErrorsArePreserved(baseUrl: string): Promise<void> {
  resetMock('patch_and_cleanup_failure')
  let failure: unknown
  try {
    await runRealGoManagementSmokeFromEnvironment(mutationEnvironment(baseUrl), () => undefined)
  } catch (error) {
    failure = error
  }

  assert(failure instanceof AggregateError)
  assert.match(failure.message, /temporary group PATCH failed with HTTP 503/)
  assert.match(failure.message, /cleanup failed: temporary group cleanup check failed with HTTP 502/)
  assertNoEnvironmentIdentifierLeak(
    [
      failure.message,
      ...failure.errors.map((error) => error instanceof Error ? error.message : String(error))
    ],
    baseUrl
  )
  assert.deepEqual(
    failure.errors.map((error) => error instanceof Error ? error.message : String(error)),
    [
      'temporary group PATCH failed with HTTP 503',
      'temporary group cleanup check failed with HTTP 502'
    ]
  )
  assertRequestHeaders()
}

async function assertInvalidConfiguration(baseUrl: string): Promise<void> {
  await assert.rejects(
    runRealGoManagementSmokeFromEnvironment({}, () => undefined),
    new RegExp(`Missing required environment variable: ${realGoManagementSmokeEnv.baseUrl}`)
  )
  await assert.rejects(
    runRealGoManagementSmokeFromEnvironment({
      [realGoManagementSmokeEnv.baseUrl]: baseUrl
    }, () => undefined),
    new RegExp(`Missing required environment variable: ${realGoManagementSmokeEnv.cookie}`)
  )
  await assert.rejects(
    runRealGoManagementSmokeFromEnvironment(smokeEnvironment(baseUrl, {
      [realGoManagementSmokeEnv.allowGroupMutations]: 'true'
    }), () => undefined),
    new RegExp(`${realGoManagementSmokeEnv.allowGroupMutations} must be 0 or 1`)
  )
  await assert.rejects(
    runRealGoManagementSmokeFromEnvironment(smokeEnvironment(baseUrl, {
      [realGoManagementSmokeEnv.requireClientIpDetail]: 'true'
    }), () => undefined),
    new RegExp(`${realGoManagementSmokeEnv.requireClientIpDetail} must be 0 or 1`)
  )
  resetMock('normal')
  await assert.rejects(
    runRealGoManagementSmokeFromEnvironment(smokeEnvironment(baseUrl, {
      [realGoManagementSmokeEnv.clientIpHash]: 'not-a-64-character-hexadecimal-hash'
    }), () => undefined),
    new RegExp(`${realGoManagementSmokeEnv.clientIpHash} must be a 64-character hexadecimal hash`)
  )
  assert.deepEqual(requestRecords, [])
  await assert.rejects(
    runRealGoManagementSmokeFromEnvironment(smokeEnvironment(baseUrl, {
      [realGoManagementSmokeEnv.timeoutMs]: '0'
    }), () => undefined),
    new RegExp(`${realGoManagementSmokeEnv.timeoutMs} must be a positive integer`)
  )
  await assert.rejects(
    runRealGoManagementSmoke({
      baseUrl,
      cookie,
      timeoutMs: 0
    }),
    /Smoke timeout must be a positive integer/
  )
  await assert.rejects(
    runRealGoManagementSmoke({
      baseUrl,
      clientIpHash: 'g'.repeat(64),
      cookie
    }),
    /Smoke client IP hash must be a 64-character hexadecimal hash/
  )

  resetMock('normal')
  const unsupportedProviderEnv = mutationEnvironment(baseUrl, {
    [realGoManagementSmokeEnv.providerCode]: missingProviderCode
  })
  await assert.rejects(
    runRealGoManagementSmokeFromEnvironment(unsupportedProviderEnv, () => undefined),
    /Mutation provider was not returned by providers\/options/
  )
  assert.equal(requestRecords.some((record) => record.method === 'POST'), false)
  assertRequestHeaders()
}

function resetMock(nextScenario: MockScenario): void {
  scenario = nextScenario
  patchFailureDelivered = false
  requestRecords.length = 0
  groups.clear()
  groups.set('grp_plan0081_default', groupFixture('grp_plan0081_default', '默认分组', true, 'owner'))
  groups.set('grp_plan0081_authorized', groupFixture('grp_plan0081_authorized', '授权分组', false, 'authorized'))
  groups.set(selectedGroupId, groupFixture(selectedGroupId, '真实 Go 管理 smoke 分组', false, 'owner'))
}

function smokeEnvironment(baseUrl: string, overrides: SmokeEnvironment = {}): SmokeEnvironment {
  return {
    [realGoManagementSmokeEnv.baseUrl]: baseUrl,
    [realGoManagementSmokeEnv.cookie]: cookie,
    [realGoManagementSmokeEnv.systemAccountId]: systemAccountId,
    ...overrides
  }
}

function mutationEnvironment(baseUrl: string, overrides: SmokeEnvironment = {}): SmokeEnvironment {
  return smokeEnvironment(baseUrl, {
    [realGoManagementSmokeEnv.allowGroupMutations]: '1',
    [realGoManagementSmokeEnv.providerCode]: 'openai',
    [realGoManagementSmokeEnv.timeoutMs]: '2500',
    ...overrides
  })
}

async function handleRequest(req: IncomingMessage, res: ServerResponse): Promise<void> {
  const body = await readRequestBody(req)
  requestRecords.push({
    method: req.method,
    url: req.url,
    headers: { ...req.headers },
    body
  })

  const url = new URL(req.url ?? '/', 'http://127.0.0.1')
  res.setHeader('Cache-Control', 'no-store')
  res.setHeader('Content-Type', 'application/json; charset=utf-8')

  if (req.method === 'GET' && url.pathname === '/__aisys__/api/public-api-logs') {
    handlePublicAPILogsListRequest(res, scenario)
    return
  }
  const publicApiLogId = publicAPILogIdFromPath(url.pathname)
  if (req.method === 'GET' && publicApiLogId !== undefined) {
    handlePublicAPILogDetailRequest(res, publicApiLogId, scenario)
    return
  }
  if (req.method === 'GET' && url.pathname === '/__aisys__/api/external-integration-sources/scopes') {
    const scopes = externalIntegrationSourceScopesFixture()
    if (scenario === 'external_integration_source_scopes_missing_item') {
      scopes.pop()
    }
    if (scenario === 'external_integration_source_scopes_invalid_field' && scopes[0]) {
      scopes[0].label = 42
    }
    sendEnvelope(res, scopes)
    return
  }
  if (req.method === 'GET' && url.pathname === '/__aisys__/api/external-integration-sources/api-docs') {
    const apiDocs = externalIntegrationSourceApiDocsFixture()
    switch (scenario) {
      case 'external_integration_source_api_docs_base_path_invalid':
        apiDocs.basePath = '/invalid-public-base-path'
        break
      case 'external_integration_source_api_docs_auth_type_invalid':
        apiDocs.authType = 'Cookie'
        break
      case 'external_integration_source_api_docs_missing_item':
        apiDocs.items.pop()
        break
      case 'external_integration_source_api_docs_get_contract_invalid': {
        const item = apiDocs.items.find((candidate) => candidate.id === 'api-key-list')
        assert(item)
        item.scope = 'juhe_ai_public:api_key_list:write'
        break
      }
      case 'external_integration_source_api_docs_get_fields_empty': {
        const item = apiDocs.items.find((candidate) => candidate.id === 'api-key-list')
        assert(item)
        item.responseFields = []
        break
      }
      case 'external_integration_source_api_docs_post_body_missing': {
        const item = apiDocs.items.find((candidate) => candidate.id === 'api-key-add')
        assert(item)
        delete item.requestBody
        break
      }
      case 'external_integration_source_api_docs_last_item_fields_empty': {
        const item = apiDocs.items.find((candidate) => candidate.id === 'account-delete')
        assert(item)
        item.responseFields = []
        break
      }
    }
    sendEnvelope(res, apiDocs)
    return
  }
  if (req.method === 'GET' && url.pathname === '/__aisys__/api/external-integration-sources') {
    const requestedPage = Number(url.searchParams.get('page') ?? '1')
    const paginationScenario = scenario === 'external_integration_source_list_pagination'
      || scenario === 'external_integration_source_list_pagination_duplicate'
    const includeNonBuiltInDetailTarget = scenario.startsWith('external_integration_source_detail_')
      && scenario !== 'external_integration_source_detail_no_non_built_in'
    const list = paginationScenario
      ? externalIntegrationSourcePaginationFixture(
          requestedPage,
          scenario === 'external_integration_source_list_pagination_duplicate'
        )
      : externalIntegrationSourceListFixture(includeNonBuiltInDetailTarget)
    const firstSource = fixtureRecord(list.items[0])
    switch (scenario) {
      case 'external_integration_source_list_missing_field':
        delete firstSource.primaryToken
        break
      case 'external_integration_source_list_rate_limits_unsorted':
        firstSource.rateLimits = [
          { windowSeconds: 60, maxRequests: 100 },
          { windowSeconds: 1, maxRequests: 2 }
        ]
        break
      case 'external_integration_source_list_sensitive_field':
        fixtureRecord(firstSource.primaryToken).TokenHash = 'plan0081-token-hash'
        break
      case 'external_integration_source_list_primary_status_invalid':
        fixtureRecord(firstSource.primaryToken).status = 'pending'
        break
      case 'external_integration_source_list_primary_not_active':
        fixtureRecord(firstSource.primaryToken).status = 'disabled'
        break
      case 'external_integration_source_list_primary_time_invalid':
        fixtureRecord(firstSource.primaryToken).createdAt = 'not-an-iso-timestamp'
        break
    }
    sendEnvelope(res, list)
    return
  }
  const externalIntegrationSourceSecretTarget = externalIntegrationSourceSecretTargetFromPath(url.pathname)
  if (req.method === 'GET' && externalIntegrationSourceSecretTarget) {
    assert.deepEqual(externalIntegrationSourceSecretTarget, {
      sourceId: configuredExternalIntegrationSourceId,
      tokenId: configuredExternalIntegrationSourceTokenId
    })
    res.setHeader(
      'Pragma',
      scenario === 'external_integration_source_secret_pragma_invalid' ? 'cache' : 'no-cache'
    )
    if (scenario === 'external_integration_source_secret_malformed') {
      sendEnvelope(res, { token: 42 })
    } else if (scenario === 'external_integration_source_secret_empty') {
      sendEnvelope(res, { token: '' })
    } else if (scenario === 'external_integration_source_secret_preview_mismatch') {
      sendEnvelope(res, { token: 'juis_Bad_plan0081_secret_value_mismatch' })
    } else {
      sendEnvelope(res, { token: externalIntegrationSourceTokenSecret })
    }
    return
  }
  const externalIntegrationSourceId = externalIntegrationSourceIdFromPath(url.pathname)
  if (req.method === 'GET' && externalIntegrationSourceId !== undefined) {
    const paginationScenario = scenario === 'external_integration_source_list_pagination'
      || scenario === 'external_integration_source_list_pagination_duplicate'
    if (paginationScenario) {
      assert.equal(
        externalIntegrationSourceId,
        'extsrc_plan0081_page_01',
        '分页场景应读取第一页第一个非内置来源详情'
      )
    }
    const detail = paginationScenario
      ? {
          ...externalIntegrationSourcePaginationItem(1),
          tokens: []
        }
      : externalIntegrationSourceDetailFixture(externalIntegrationSourceId)
    const tokens = detail.tokens
    assert(Array.isArray(tokens))
    const firstToken = (): Record<string, unknown> => {
      const token = tokens[0]
      assert(token)
      return fixtureRecord(token)
    }
    switch (scenario) {
      case 'external_integration_source_detail_newer_snapshot':
        detail.name = 'PLAN-0081 并发更新后的编码来源'
        detail.status = 'disabled'
        detail.scopes = []
        detail.rateLimits = []
        delete detail.expiresAt
        detail.notes = 'PLAN-0081 newer detail snapshot'
        detail.lastUsedAt = '2026-07-15T11:00:00.000Z'
        detail.updatedAt = '2026-07-15T11:00:00.000Z'
        detail.tokens = tokens.slice(1)
        detail.tokenCount = 2
        detail.activeTokenCount = 0
        break
      case 'external_integration_source_detail_id_mismatch':
        detail.id = `${selectedExternalIntegrationSourceId}_mismatch`
        break
      case 'external_integration_source_detail_edit_field_mismatch':
        detail.notes = 'PLAN-0081 mismatched edit field'
        break
      case 'external_integration_source_detail_unknown_field':
        detail.debugMetadata = { unsafe: true }
        break
      case 'external_integration_source_detail_tokens_not_array':
        detail.tokens = { unsafe: true }
        break
      case 'external_integration_source_detail_token_count_mismatch':
        detail.tokenCount = tokens.length - 1
        break
      case 'external_integration_source_detail_active_token_count_mismatch':
        detail.activeTokenCount = 0
        break
      case 'external_integration_source_detail_created_at_unsorted':
        detail.tokens = [tokens[2], tokens[0], tokens[1]]
        break
      case 'external_integration_source_detail_id_unsorted':
        detail.tokens = [tokens[1], tokens[0], tokens[2]]
        break
      case 'external_integration_source_detail_token_unknown_field':
        firstToken().debugMetadata = { unsafe: true }
        break
      case 'external_integration_source_detail_sensitive_token':
        firstToken().token = 'juis_plan0081_plaintext_secret'
        break
      case 'external_integration_source_detail_sensitive_hash':
        firstToken().tokenHash = 'plan0081-token-hash'
        break
      case 'external_integration_source_detail_sensitive_ciphertext':
        firstToken().tokenSecretEncrypted = 'plan0081-token-ciphertext'
        break
      case 'external_integration_source_detail_sensitive_preview_value':
        firstToken().tokenPrefix = 'juis_plan0081_plaintext_secret'
        break
      case 'external_integration_source_detail_sensitive_primary_token':
        detail.primaryToken = { ...firstToken() }
        break
    }
    sendEnvelope(res, detail)
    return
  }
  if (req.method === 'GET' && url.pathname === '/__aisys__/api/groups') {
    sendEnvelope(res, groupListFixture())
    return
  }
  if (req.method === 'GET' && url.pathname === '/__aisys__/api/route-strategies') {
    await handleRouteStrategiesListRequest(res, scenario)
    return
  }
  const routeStrategyId = routeStrategyIdFromPath(url.pathname)
  if (req.method === 'GET' && routeStrategyId) {
    const detail = routeStrategyFixture(true)
    if (scenario === 'route_strategy_detail_invalid') {
      detail.apiKeyCount = 3
    }
    sendEnvelope(res, detail)
    return
  }
  if (req.method === 'POST' && url.pathname === '/__aisys__/api/groups') {
    const input = recordBody({ body })
    const name = String(input.name ?? '')
    const providerCode = String(input.providerCode ?? '')
    const created = groupFixture(temporaryGroupId, name, false, 'owner', providerCode)
    groups.set(created.id, created)
    res.statusCode = 201
    sendEnvelope(res, groupResponse(created))
    return
  }
  if (req.method === 'GET' && url.pathname === '/__aisys__/api/providers/options') {
    sendEnvelope(res, [
      providerFixture('gpt', 'GPT'),
      providerFixture('openai', 'OpenAI')
    ])
    return
  }
  if (req.method === 'GET' && url.pathname === '/__aisys__/api/providers/models/options') {
    sendEnvelope(res, [
      {
        providerCode: 'gpt',
        model: 'gpt-5.6-sol',
        supportedApiProtocols: ['responses'],
        supportedServiceTiers: ['priority'],
        supportedReasoningEfforts: ['low', 'high'],
        defaultReasoningEffort: 'high'
      },
      {
        providerCode: 'openai',
        model: 'gpt-4.1',
        supportedApiProtocols: ['chat_completions'],
        defaultReasoningEffort: null
      }
    ])
    return
  }
  if (req.method === 'GET' && url.pathname === '/__aisys__/api/api-keys') {
    sendEnvelope(res, apiKeyListFixture('admin', scenario))
    return
  }
  if (req.method === 'GET' && url.pathname === '/__aisys__/api/my-api-keys') {
    sendEnvelope(res, apiKeyListFixture('self', scenario))
    return
  }
  const accountTestOptionsAccountId = accountTestOptionsAccountIdFromPath(url.pathname)
  if (req.method === 'GET' && accountTestOptionsAccountId !== undefined) {
    sendEnvelope(res, accountTestOptionsFixture(accountTestOptionsAccountId, scenario))
    return
  }
  if (req.method === 'GET' && url.pathname === '/__aisys__/api/ip-stats') {
    await handleClientIPStatsRequest(res, scenario)
    return
  }
  const clientIPHash = clientIPHashFromDetailPath(url.pathname)
  if (req.method === 'GET' && clientIPHash) {
    sendEnvelope(res, clientIPStatsDetailFixture(clientIPHash, scenario))
    return
  }

  const groupId = groupIdFromPath(url.pathname)
  if (groupId) {
    await handleGroupDetailRequest(req, res, groupId, body)
    return
  }

  res.statusCode = 404
  res.end(JSON.stringify({ message: 'not found' }))
}

function handlePublicAPILogsListRequest(res: ServerResponse, requestScenario: MockScenario): void {
  const includesSummary = requestScenario === 'public_api_logs_non_empty'
    || requestScenario === 'public_api_logs_required_field_invalid'
    || requestScenario === 'public_api_logs_optional_field_invalid'
    || requestScenario === 'public_api_logs_capture_status_invalid'
  const summary = publicAPILogSummaryFixture(listedPublicApiLogId)
  if (requestScenario === 'public_api_logs_required_field_invalid') {
    summary.method = 42
  }
  if (requestScenario === 'public_api_logs_optional_field_invalid') {
    summary.traceId = 42
  }
  if (requestScenario === 'public_api_logs_capture_status_invalid') {
    summary.requestCaptureStatus = 'partial'
  }

  const data: Record<string, unknown> = {
    items: includesSummary ? [summary] : [],
    total: includesSummary ? 1 : 0,
    hasMore: false,
    page: 1,
    pageSize: 20
  }
  if (requestScenario === 'public_api_logs_items_invalid') {
    data.items = { id: listedPublicApiLogId }
  }
  if (requestScenario === 'public_api_logs_pagination_invalid') {
    data.total = 1
  }
  if (requestScenario === 'public_api_logs_no_store') {
    res.setHeader('Cache-Control', 'private, max-age=60')
  }
  if (requestScenario === 'public_api_logs_envelope_invalid') {
    res.end(JSON.stringify({ data, meta: { source: 'invalid-envelope' } }))
    return
  }
  sendEnvelope(res, data)
}

function handlePublicAPILogDetailRequest(
  res: ServerResponse,
  publicApiLogId: string,
  requestScenario: MockScenario
): void {
  const detail = publicAPILogDetailFixture(publicApiLogId)
  if (requestScenario === 'public_api_log_detail_request_data_invalid') {
    detail.requestData = []
  }
  if (requestScenario === 'public_api_log_detail_response_data_invalid') {
    detail.responseData = []
  }
  if (requestScenario === 'public_api_log_detail_no_store') {
    res.setHeader('Cache-Control', 'private, max-age=60')
  }
  sendEnvelope(res, detail)
}

async function handleClientIPStatsRequest(res: ServerResponse, requestScenario: MockScenario): Promise<void> {
  if (requestScenario === 'ip_stats_timeout') {
    await delay(1_000)
    if (res.destroyed) {
      return
    }
  }
  if (requestScenario === 'ip_stats_failure') {
    res.statusCode = 503
    res.end(JSON.stringify({
      message: `client IP window unavailable for ${sensitiveClientIPHash}; cookie=${cookie}`
    }))
    return
  }
  const rangeReady = requestScenario !== 'ip_stats_not_ready'
  sendEnvelope(res, clientIPStatsListFixture(rangeReady, rangeReady && requestScenario !== 'ip_stats_empty'))
}

async function handleRouteStrategiesListRequest(
  res: ServerResponse,
  requestScenario: MockScenario
): Promise<void> {
  if (requestScenario === 'route_strategies_timeout') {
    await delay(1_000)
    if (res.destroyed) {
      return
    }
  }
  if (requestScenario === 'route_strategies_failure') {
    res.statusCode = 503
    res.end(JSON.stringify({
      message: `route strategy unavailable: ${selectedRouteStrategyId}; cookie=${cookie}; config=route-config-sensitive-model`
    }))
    return
  }
  const items = requestScenario === 'route_strategies_empty' ? [] : [routeStrategyFixture(false)]
  const result = { items, total: items.length, hasMore: false, page: 1, pageSize: 200 }
  if (requestScenario === 'route_strategies_invalid' && result.items[0]) {
    result.items[0].groupBindings = routeStrategyBindings()
    result.items[0].hybridRoutingConfig = { scoringModel: 'route-config-sensitive-model' }
  }
  sendEnvelope(res, result)
}

async function handleGroupDetailRequest(
  req: IncomingMessage,
  res: ServerResponse,
  groupId: string,
  body: unknown
): Promise<void> {
  const group = groups.get(groupId)

  if (
    req.method === 'GET'
    && groupId === temporaryGroupId
    && scenario === 'patch_and_cleanup_failure'
    && patchFailureDelivered
  ) {
    res.statusCode = 502
    res.end(JSON.stringify({ message: 'cleanup lookup unavailable' }))
    return
  }

  if (req.method === 'GET') {
    if (!group) {
      res.statusCode = 404
      res.end(JSON.stringify({ message: 'not found' }))
      return
    }
    sendEnvelope(res, groupResponse(group))
    return
  }

  if (req.method === 'PATCH') {
    if (!group) {
      res.statusCode = 404
      res.end(JSON.stringify({ message: 'not found' }))
      return
    }
    const input = recordBody({ body })
    if (typeof input.name === 'string') {
      group.name = input.name
    }
    if (typeof input.description === 'string') {
      group.description = input.description
    }
    if (input.groupType === 'high_concurrency') {
      group.groupType = 'high_concurrency'
    }
    if (input.schedulingPolicy && typeof input.schedulingPolicy === 'object' && !Array.isArray(input.schedulingPolicy)) {
      group.schedulingPolicy = input.schedulingPolicy as Record<string, unknown>
    }
    if (scenario !== 'normal') {
      patchFailureDelivered = true
      res.statusCode = 503
      res.end(JSON.stringify({ message: 'patch response unavailable' }))
      return
    }
    sendEnvelope(res, groupResponse(group))
    return
  }

  if (req.method === 'DELETE') {
    if (scenario === 'cleanup_404') {
      groups.delete(groupId)
      res.statusCode = 404
      res.end(JSON.stringify({ message: 'already deleted' }))
      return
    }
    if (!group) {
      res.statusCode = 404
      res.end(JSON.stringify({ message: 'not found' }))
      return
    }
    groups.delete(groupId)
    res.statusCode = 204
    res.end()
    return
  }

  res.statusCode = 405
  res.end(JSON.stringify({ message: 'method not allowed' }))
}

function groupListFixture(): Record<string, unknown> {
  return {
    items: [...groups.values()].map(groupListItem),
    total: groups.size,
    hasMore: false,
    page: 1,
    pageSize: 500,
    runtimeSnapshot: {
      accountConcurrencyAvailable: true
    }
  }
}

function groupFixture(
  id: string,
  name: string,
  isDefault: boolean,
  accessType: 'owner' | 'authorized',
  providerCode = 'gpt'
): MockGroup {
  return {
    id,
    ownerSystemAccountId: accessType === 'owner' ? systemAccountId : 'sys_plan0081_owner',
    name,
    providerCode,
    enabled: true,
    isDefault,
    groupType: 'personal',
    accessType,
    accountIds: accessType === 'owner' ? ['acct_plan0081_one'] : []
  }
}

function groupListItem(group: MockGroup): Record<string, unknown> {
  const { accountIds, ...item } = groupResponse(group)
  return {
    ...item,
    accountCount: accountIds.length
  }
}

function groupResponse(group: MockGroup): Record<string, unknown> {
  return {
    ...group,
    ownerSystemAccountName: 'Owner',
    systemAccountId: group.ownerSystemAccountId,
    accountStats: {
      total: group.accountIds.length,
      available: group.accountIds.length
    },
    authorizationLimits: {},
    permissions: {
      canManageAccounts: group.accessType === 'owner'
    },
    schedulingPolicy: group.groupType === 'high_concurrency'
      ? {
          mode: 'balanced_fast',
          defaultSoftConcurrency: 5,
          maxQueueWaitMs: 60_000,
          clientIpConcurrencyLimit: 0,
          clientIpConcurrencyOverflowMode: 'reject',
          imageLaneMaxConcurrency: 0,
          ...group.schedulingPolicy
        }
      : undefined
  }
}

function providerFixture(code: string, name: string): Record<string, unknown> {
  const profileId = `profile_${code}_openai_v1`
  return {
    id: `provider_${code}`,
    code,
    name,
    enabled: true,
    defaultProtocolProfileId: profileId,
    protocolCode: 'openai',
    protocolVersion: 'v1',
    baseUrl: 'https://api.example.test/v1',
    defaultHealthCheckModel: code === 'gpt' ? 'gpt-5.6-sol' : 'gpt-4.1',
    defaultSupportedModels: code === 'gpt' ? ['gpt-5.6-sol'] : ['gpt-4.1'],
    accountTypes: ['api_key'],
    capabilities: ['chat_completions', 'responses'],
    protocolProfiles: [{
      id: profileId,
      providerCode: code,
      name: `${name} OpenAI v1`,
      enabled: true,
      protocolCode: 'openai',
      protocolVersion: 'v1',
      baseUrl: 'https://api.example.test/v1',
      defaultHealthCheckModel: code === 'gpt' ? 'gpt-5.6-sol' : 'gpt-4.1',
      accountTypes: ['api_key'],
      capabilities: ['chat_completions', 'responses'],
      endpointFamilies: []
    }]
  }
}

function accountTestOptionsFixture(accountId: string, requestScenario: MockScenario): unknown {
  if (requestScenario === 'account_test_options_not_object') {
    return []
  }

  const result: Record<string, unknown> = {
    accountId,
    defaultModel: 'gpt-test-options-sensitive',
    models: [
      {
        model: 'gpt-test-options-sensitive',
        supportedApiProtocols: ['responses'],
        testEndpointModes: ['responses_json', 'responses_sse']
      },
      {
        model: 'gpt-test-options-secondary',
        supportedApiProtocols: [],
        testEndpointModes: ['chat_json', 'chat_sse']
      }
    ],
    testEndpointModes: ['responses_json', 'responses_sse'],
    defaultTestEndpointMode: 'responses_json'
  }

  switch (requestScenario) {
    case 'account_test_options_account_mismatch':
      result.accountId = mismatchedAccountId
      break
    case 'account_test_options_default_model_empty':
      result.defaultModel = '   '
      break
    case 'account_test_options_models_empty':
      result.models = []
      break
    case 'account_test_options_model_empty':
      result.models = [{
        model: '',
        supportedApiProtocols: ['responses'],
        testEndpointModes: ['responses_json']
      }]
      break
    case 'account_test_options_protocols_invalid':
      result.models = [{
        model: 'gpt-test-options-sensitive',
        supportedApiProtocols: ['responses', 1],
        testEndpointModes: ['responses_json']
      }]
      break
    case 'account_test_options_model_endpoint_modes_missing':
      result.models = [{
        model: 'gpt-test-options-sensitive',
        supportedApiProtocols: ['responses']
      }]
      break
    case 'account_test_options_model_endpoint_modes_empty':
      result.models = [{
        model: 'gpt-test-options-sensitive',
        supportedApiProtocols: ['responses'],
        testEndpointModes: []
      }]
      break
    case 'account_test_options_model_endpoint_mode_invalid':
      result.models = [{
        model: 'gpt-test-options-sensitive',
        supportedApiProtocols: ['responses'],
        testEndpointModes: ['responses_json', 'invalid_mode']
      }]
      break
    case 'account_test_options_default_model_missing':
      result.defaultModel = 'gpt-test-options-missing'
      break
    case 'account_test_options_endpoint_modes_empty':
      result.testEndpointModes = []
      break
    case 'account_test_options_endpoint_mode_invalid':
      result.testEndpointModes = ['responses_json', 'invalid_mode']
      break
    case 'account_test_options_default_model_modes_mismatch':
      result.testEndpointModes = ['responses_sse']
      break
    case 'account_test_options_default_endpoint_mode_mismatch':
      result.defaultTestEndpointMode = 'responses_sse'
      break
  }
  return result
}

function publicAPILogSummaryFixture(id: string): Record<string, unknown> {
  return {
    id,
    traceId: 'trace_plan0081_public_log_sensitive',
    sourceRefId: 'source_plan0081_public_log_sensitive',
    sourceName: 'PLAN-0081 public log source',
    tokenId: 'token_plan0081_public_log_sensitive',
    tokenName: 'PLAN-0081 public log token',
    tokenPrefix: 'juis_plan0081',
    isTestToken: true,
    method: 'POST',
    path: '/__aipublic__/account/add',
    queryString: 'targetUsername=plan0081-sensitive',
    clientIp: '192.0.2.81',
    userAgent: 'plan0081-public-api-client/1.0',
    statusCode: 201,
    success: true,
    durationMs: 81,
    requestSizeBytes: 128,
    responseSizeBytes: 256,
    requestCaptureStatus: 'truncated',
    responseCaptureStatus: 'dropped',
    errorCode: '',
    errorMessage: '',
    startedAt: '2026-07-14T01:02:03.000Z',
    endedAt: '2026-07-14T01:02:03.081Z',
    createdAt: '2026-07-14T01:02:03.081Z'
  }
}

function publicAPILogDetailFixture(id: string): Record<string, unknown> {
  return {
    ...publicAPILogSummaryFixture(id),
    requestCaptureStatus: 'complete',
    responseCaptureStatus: 'empty',
    requestData: {
      method: 'POST',
      path: '/__aipublic__/account/add',
      body: { apiKey: '[redacted]' }
    },
    responseData: {
      statusCode: 201,
      body: {}
    }
  }
}

function sendEnvelope(res: ServerResponse, data: unknown): void {
  res.end(JSON.stringify({ data }))
}

function groupIdFromPath(pathname: string): string | undefined {
  const match = /^\/__aisys__\/api\/groups\/([^/]+)$/.exec(pathname)
  return match?.[1] ? decodeURIComponent(match[1]) : undefined
}

function routeStrategyIdFromPath(pathname: string): string | undefined {
  const match = /^\/__aisys__\/api\/route-strategies\/([^/]+)$/.exec(pathname)
  return match?.[1] ? decodeURIComponent(match[1]) : undefined
}

function accountTestOptionsAccountIdFromPath(pathname: string): string | undefined {
  const match = /^\/__aisys__\/api\/accounts\/([^/]+)\/test-options$/.exec(pathname)
  return match?.[1] ? decodeURIComponent(match[1]) : undefined
}

function publicAPILogIdFromPath(pathname: string): string | undefined {
  const match = /^\/__aisys__\/api\/public-api-logs\/([^/]+)$/.exec(pathname)
  return match?.[1] ? decodeURIComponent(match[1]) : undefined
}

function externalIntegrationSourceIdFromPath(pathname: string): string | undefined {
  const match = /^\/__aisys__\/api\/external-integration-sources\/([^/]+)$/.exec(pathname)
  return match?.[1] ? decodeURIComponent(match[1]) : undefined
}

function externalIntegrationSourceSecretTargetFromPath(
  pathname: string
): { sourceId: string; tokenId: string } | undefined {
  const match = /^\/__aisys__\/api\/external-integration-sources\/([^/]+)\/tokens\/([^/]+)\/secret$/.exec(pathname)
  return match?.[1] && match[2]
    ? { sourceId: decodeURIComponent(match[1]), tokenId: decodeURIComponent(match[2]) }
    : undefined
}

async function readRequestBody(req: IncomingMessage): Promise<unknown> {
  if (req.method === 'GET' || req.method === 'DELETE') {
    return undefined
  }
  const chunks: Buffer[] = []
  for await (const chunk of req) {
    chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk))
  }
  if (!chunks.length) {
    return undefined
  }
  return JSON.parse(Buffer.concat(chunks).toString('utf8')) as unknown
}

function recordBody(record: Pick<RequestRecord, 'body'>): Record<string, unknown> {
  assert(record.body && typeof record.body === 'object' && !Array.isArray(record.body))
  return record.body as Record<string, unknown>
}

function omitDynamicName(value: unknown): Record<string, unknown> {
  const { name: _name, ...rest } = recordBody({ body: value })
  return rest
}

function expectedSummary(
  clientIpRangeReady = true,
  clientIpItemCount = clientIpRangeReady ? 3 : 0,
  clientIpDetailChecked = clientIpRangeReady && clientIpItemCount > 0,
  routeStrategyCount = 1,
  routeStrategyDetailChecked = routeStrategyCount > 0,
  accountTestOptionsChecked = false
): Record<string, unknown> {
  return {
    accountTestOptionsChecked,
    adminApiKeyCount: 2,
    groupCount: 3,
    selectedGroupId,
    providerCount: 2,
    modelOptionCount: 2,
    publicApiLogCount: 0,
    publicApiLogDetailChecked: false,
    clientIpItemCount,
    clientIpRangeReady,
    clientIpDetailChecked,
    routeStrategyCount,
    routeStrategyDetailChecked,
    selfApiKeyCount: 1,
    externalIntegrationSourceTokenSecretChecked: false
  }
}

function apiKeyListFixture(scope: 'admin' | 'self', requestScenario: MockScenario): Record<string, unknown> {
  const empty = requestScenario === 'api_keys_empty'
  const items = empty
    ? []
    : scope === 'admin'
      ? [apiKeyFixture('admin-primary', true), apiKeyFixture('admin-secondary', true)]
      : [apiKeyFixture('self-primary', false)]
  if (scope === 'admin' && requestScenario === 'api_keys_admin_owner_missing') {
    delete fixtureRecord(items[0]).systemAccountId
  }
  if (scope === 'self' && requestScenario === 'api_keys_self_owner_leak') {
    fixtureRecord(items[0]).systemAccountId = systemAccountId
    fixtureRecord(items[0]).systemAccountName = 'PLAN-0081 leaked owner'
  }
  if (scope === 'admin' && requestScenario === 'api_keys_sensitive_field') {
    fixtureRecord(items[0]).keyHash = 'plan0081-derived-secret'
  }
  if (scope === 'admin' && requestScenario === 'api_keys_nested_preview_field') {
    fixtureRecord(fixtureRecord(items[0]).usage).keyPrefix = 'plan0081-nested-sensitive-value'
  }
  return {
    items,
    total: scope === 'admin' && requestScenario === 'api_keys_pagination_invalid' ? items.length + 1 : items.length,
    hasMore: false,
    page: 1,
    pageSize: 20
  }
}

function apiKeyFixture(idSuffix: string, includeOwner: boolean): Record<string, unknown> {
  return {
    id: `key_plan0081_${idSuffix}`,
    ...(includeOwner ? {
      systemAccountId,
      systemAccountName: 'PLAN-0081 API Key owner'
    } : {}),
    name: `PLAN-0081 API Key ${idSuffix}`,
    description: 'regression fixture',
    keyPrefix: 'sk-plan0081',
    keySuffix: '0081',
    status: idSuffix.endsWith('secondary') ? 'disabled' : 'active',
    isDefault: idSuffix.endsWith('primary'),
    routeStrategyId: selectedRouteStrategyId,
    routeStrategyName: 'PLAN-0081 route',
    routeStrategyMode: 'normal',
    routeStrategyStatus: 'active',
    expiresAt: '2026-08-01T00:00:00.000Z',
    quotaLimits: { dailyUsd: 10 },
    availabilitySchedule: { enabled: false, timezone: 'Asia/Shanghai', mode: 'allow', windows: [] },
    usage: { requestCount: 3, totalTokens: 120 }
  }
}

function routeStrategyBindings(): Array<Record<string, unknown>> {
  return Array.from({ length: 4 }, (_, index) => ({
    id: `${selectedRouteStrategyId}_binding_${index + 1}`,
    groupId: `route_group_sensitive_${index + 1}`,
    priority: index + 1,
    weight: 25,
    status: 'active',
    groupEnabled: true
  }))
}

function routeStrategyFixture(detail: boolean): Record<string, unknown> {
  const bindings = routeStrategyBindings()
  const shared = {
    id: selectedRouteStrategyId,
    systemAccountId,
    systemAccountName: 'PLAN-0081 sensitive route owner',
    name: 'PLAN-0081 normal route sensitive name',
    mode: 'normal',
    status: 'active',
    isDefault: true,
    normalRoutingConfig: { schedulingPreference: 'cost_first' },
    createdAt: '2026-07-14T01:00:00Z',
    updatedAt: '2026-07-14T02:00:00Z'
  }
  return detail
    ? { ...shared, groupBindings: bindings, apiKeyCount: 2 }
    : { ...shared, groupBindingPreview: bindings.slice(0, 3), bindingCount: 4, apiKeyCount: 2 }
}

function externalIntegrationSourceScopesFixture(): Array<Record<string, unknown>> {
  return [
    { value: 'juhe_ai_public:api_key_list:read', label: 'GET API Key 列表' },
    { value: 'juhe_ai_public:route_strategy_list:read', label: 'GET 路由策略列表' },
    { value: 'juhe_ai_public:group_list:read', label: 'GET 分组列表' },
    { value: 'juhe_ai_public:account_list:read', label: 'GET 账号列表' },
    { value: 'juhe_ai_public:api_key_add:write', label: 'POST API Key 新增' },
    { value: 'juhe_ai_public:api_key_update:write', label: 'POST API Key 修改' },
    { value: 'juhe_ai_public:api_key_delete:write', label: 'POST API Key 删除' },
    { value: 'juhe_ai_public:route_strategy_add:write', label: 'POST 路由策略新增' },
    { value: 'juhe_ai_public:route_strategy_update:write', label: 'POST 路由策略修改' },
    { value: 'juhe_ai_public:route_strategy_delete:write', label: 'POST 路由策略删除' },
    { value: 'juhe_ai_public:group_add:write', label: 'POST 分组新增' },
    { value: 'juhe_ai_public:group_update:write', label: 'POST 分组修改' },
    { value: 'juhe_ai_public:group_delete:write', label: 'POST 分组删除' },
    { value: 'juhe_ai_public:account_add:write', label: 'POST 账号新增' },
    { value: 'juhe_ai_public:account_update:write', label: 'POST 账号修改' },
    { value: 'juhe_ai_public:account_delete:write', label: 'POST 账号删除' }
  ]
}

function externalIntegrationSourceApiDocsFixture(): {
  basePath: string
  authType: string
  items: Array<Record<string, unknown>>
} {
  const items = externalIntegrationSourceScopesFixture().map((option): Record<string, unknown> => {
    const scope = String(option.value)
    const match = /^juhe_ai_public:(api_key|route_strategy|group|account)_(list|add|update|delete):(read|write)$/.exec(scope)
    assert(match)
    const resource = String(match[1]).replace('_', '-')
    const action = String(match[2])
    const method = action === 'list' ? 'GET' : 'POST'
    const publicAction = action === 'delete' ? 'del' : action
    const documentedField = {
      name: 'targetUsername',
      type: 'string',
      required: true,
      description: '目标系统用户账号。',
      example: 'plan0081-user'
    }

    return {
      id: `${resource}-${action}`,
      name: String(option.label).replace(/^(GET|POST)\s+/, ''),
      summary: `${String(option.label)} regression fixture`,
      status: 'available',
      method,
      path: `/__aipublic__/${resource}/${publicAction}`,
      scope,
      headers: [{
        name: 'Authorization',
        required: true,
        description: '来源授权 Bearer token。',
        example: 'Bearer <source_token>'
      }],
      query: method === 'GET' ? [documentedField] : [],
      ...(method === 'POST'
        ? {
            requestBody: {
              contentType: 'application/json',
              fields: [documentedField],
              example: { targetUsername: 'plan0081-user' }
            }
          }
        : {}),
      responseFields: [{
        name: 'data',
        type: 'object',
        required: true,
        description: '接口响应数据。'
      }],
      responseExample: { data: { source: 'stats' } }
    }
  })

  return {
    basePath: '/__aipublic__',
    authType: 'Bearer',
    items
  }
}

function externalIntegrationSourceListFixture(includeNonBuiltInDetailTarget = false): {
  items: Array<Record<string, unknown>>
  page: number
  pageSize: number
  pageUpperBound: number
  hasMore: boolean
} {
  const detailPrimaryToken = externalIntegrationSourceDetailTokensFixture()[0]
  assert(detailPrimaryToken)
  const selectedSourceIsBuiltIn = !includeNonBuiltInDetailTarget
  const items: Array<Record<string, unknown>> = [
    {
      id: 'extsrc_builtin_test',
      name: 'PLAN-0081 内建来源',
      status: 'active',
      scopes: [
        'juhe_ai_public:account_list:read',
        'juhe_ai_public:group_list:read'
      ],
      rateLimits: [
        { windowSeconds: 1, maxRequests: 1 },
        { windowSeconds: 86_400, maxRequests: 100_000 }
      ],
      expiresAt: '2026-08-15T00:00:00.000Z',
      notes: 'PLAN-0081 external integration source list fixture',
      lastUsedAt: '2026-07-15T08:00:00.000Z',
      createdAt: '2026-07-14T01:00:00.000Z',
      updatedAt: '2026-07-15T08:00:00.000Z',
      tokenCount: 3,
      activeTokenCount: 1,
      primaryToken: {
        id: 'exttok_builtin_test',
        name: 'PLAN-0081 primary token',
        tokenPrefix: 'juis_Bi1',
        tokenSuffix: 'active01',
        status: 'active',
        scopes: ['juhe_ai_public:group_list:read'],
        expiresAt: '2026-07-31T00:00:00.000Z',
        lastUsedAt: '2026-07-15T08:00:00.000Z',
        createdAt: '2026-07-14T01:05:00.000Z',
        updatedAt: '2026-07-15T08:00:00.000Z',
        isBuiltIn: true
      },
      isBuiltIn: true
    },
    {
      id: selectedExternalIntegrationSourceId,
      name: 'PLAN-0081 编码来源',
      status: 'active',
      scopes: [
        'juhe_ai_public:api_key_list:read',
        'juhe_ai_public:group_list:read'
      ],
      rateLimits: [
        { windowSeconds: 1, maxRequests: 2 },
        { windowSeconds: 60, maxRequests: 120 }
      ],
      expiresAt: '2026-09-01T00:00:00.000Z',
      notes: 'PLAN-0081 external integration source detail fixture',
      lastUsedAt: '2026-07-15T10:30:00.000Z',
      createdAt: '2026-07-13T02:00:00.000Z',
      updatedAt: '2026-07-15T07:00:00.000Z',
      tokenCount: 3,
      activeTokenCount: 1,
      primaryToken: {
        ...detailPrimaryToken,
        isBuiltIn: selectedSourceIsBuiltIn
      },
      isBuiltIn: selectedSourceIsBuiltIn
    }
  ]
  if (includeNonBuiltInDetailTarget) {
    items.push({
      id: 'extsrc_plan0081_later_non_builtin',
      name: 'PLAN-0081 后续非内置来源',
      status: 'disabled',
      scopes: [],
      rateLimits: [],
      createdAt: '2026-07-12T02:00:00.000Z',
      updatedAt: '2026-07-15T06:00:00.000Z',
      tokenCount: 0,
      activeTokenCount: 0,
      isBuiltIn: false
    })
  }
  return {
    items,
    page: 1,
    pageSize: 20,
    pageUpperBound: items.length,
    hasMore: false
  }
}

function externalIntegrationSourceDetailFixture(sourceId: string): Record<string, unknown> {
  if (sourceId === configuredExternalIntegrationSourceId) {
    const { primaryToken: _primaryToken, ...source } = externalIntegrationSourceListFixture(true).items[1] ?? {}
    return {
      ...source,
      id: sourceId,
      status: 'disabled',
      tokenCount: 1,
      activeTokenCount: 0,
      tokens: [{
        ...externalIntegrationSourceDetailTokensFixture()[2],
        id: configuredExternalIntegrationSourceTokenId,
        tokenPrefix: 'juis_Op1',
        tokenSuffix: 'revoked1'
      }],
      isBuiltIn: false
    }
  }
  assert.equal(sourceId, selectedExternalIntegrationSourceId, '详情只应读取第一页第一个非内置来源')
  const list = externalIntegrationSourceListFixture(true)
  const source = list.items.find((item) => item.id === sourceId)
  assert(source)
  const detail = {
    ...source,
    tokens: externalIntegrationSourceDetailTokensFixture()
  }
  delete detail.primaryToken
  return detail
}

function externalIntegrationSourceDetailTokensFixture(): Array<Record<string, unknown>> {
  return [
    {
      id: 'exttok_plan0081_z',
      name: 'PLAN-0081 active token',
      tokenPrefix: 'juis_Ac1',
      tokenSuffix: 'active01',
      status: 'active',
      scopes: ['juhe_ai_public:group_list:read'],
      expiresAt: '2026-08-31T00:00:00.000Z',
      lastUsedAt: '2026-07-15T10:30:00.000Z',
      createdAt: '2026-07-15T10:00:00.000Z',
      updatedAt: '2026-07-15T10:30:00.000Z',
      isBuiltIn: false
    },
    {
      id: 'exttok_plan0081_a',
      name: 'PLAN-0081 disabled token',
      tokenPrefix: 'juis_Di1',
      tokenSuffix: 'disable1',
      status: 'disabled',
      scopes: ['juhe_ai_public:api_key_list:read'],
      createdAt: '2026-07-15T10:00:00.000Z',
      updatedAt: '2026-07-15T10:20:00.000Z',
      isBuiltIn: false
    },
    {
      id: 'exttok_plan0081_old',
      name: 'PLAN-0081 revoked token',
      tokenPrefix: 'juis_Rv1',
      tokenSuffix: 'revoked1',
      status: 'revoked',
      scopes: [],
      createdAt: '2026-07-14T09:00:00.000Z',
      updatedAt: '2026-07-15T09:00:00.000Z',
      revokedAt: '2026-07-15T09:00:00.000Z',
      isBuiltIn: false
    }
  ]
}

function externalIntegrationSourcePaginationFixture(
  page: number,
  duplicateSecondPage: boolean
): {
  items: Array<Record<string, unknown>>
  page: number
  pageSize: number
  pageUpperBound: number
  hasMore: boolean
} {
  const firstPageItems = Array.from(
    { length: 20 },
    (_, index) => externalIntegrationSourcePaginationItem(index + 1)
  )
  if (page === 1) {
    return {
      items: firstPageItems,
      page: 1,
      pageSize: 20,
      pageUpperBound: 21,
      hasMore: true
    }
  }
  assert.equal(page, 2, '分页场景只应读取前两页')
  return {
    items: [
      duplicateSecondPage
        ? externalIntegrationSourcePaginationItem(1)
        : externalIntegrationSourcePaginationItem(21)
    ],
    page: 2,
    pageSize: 20,
    pageUpperBound: 21,
    hasMore: false
  }
}

function externalIntegrationSourcePaginationItem(index: number): Record<string, unknown> {
  return {
    id: `extsrc_plan0081_page_${String(index).padStart(2, '0')}`,
    name: `PLAN-0081 分页来源 ${index}`,
    status: index % 2 === 0 ? 'disabled' : 'active',
    scopes: [],
    rateLimits: [],
    createdAt: '2026-07-14T01:00:00.000Z',
    updatedAt: '2026-07-15T08:00:00.000Z',
    tokenCount: 0,
    activeTokenCount: 0,
    isBuiltIn: false
  }
}

function fixtureRecord(value: unknown): Record<string, unknown> {
  assert(value && typeof value === 'object' && !Array.isArray(value))
  return value as Record<string, unknown>
}

function requestPaths(): string[] {
  return requestRecords.map((record) => `${record.method} ${record.url}`)
}

function publicAPILogsListPath(): string {
  return 'GET /__aisys__/api/public-api-logs?page=1&pageSize=20'
}

function adminApiKeysListPath(): string {
  return `GET /__aisys__/api/api-keys?page=1&pageSize=20&status=all&systemAccountId=${systemAccountId}`
}

function selfApiKeysListPath(): string {
  return 'GET /__aisys__/api/my-api-keys?page=1&pageSize=20&status=all'
}

function publicAPILogDetailPath(publicApiLogId = configuredPublicApiLogId): string {
  return `GET /__aisys__/api/public-api-logs/${encodeURIComponent(publicApiLogId)}`
}

function externalIntegrationSourceScopesPath(): string {
  return 'GET /__aisys__/api/external-integration-sources/scopes'
}

function externalIntegrationSourceApiDocsPath(): string {
  return 'GET /__aisys__/api/external-integration-sources/api-docs'
}

function externalIntegrationSourcesListPath(page = 1): string {
  return `GET /__aisys__/api/external-integration-sources?page=${page}&pageSize=20`
}

function externalIntegrationSourceDetailPath(sourceId = selectedExternalIntegrationSourceId): string {
  return `GET /__aisys__/api/external-integration-sources/${encodeURIComponent(sourceId)}`
}

function externalIntegrationSourceTokenSecretPath(): string {
  return `GET /__aisys__/api/external-integration-sources/${encodeURIComponent(configuredExternalIntegrationSourceId)}/tokens/${encodeURIComponent(configuredExternalIntegrationSourceTokenId)}/secret`
}

function groupsListPath(): string {
  return `GET /__aisys__/api/groups?page=1&pageSize=500&systemAccountId=${systemAccountId}`
}

function groupsCreatePath(): string {
  return `POST /__aisys__/api/groups?systemAccountId=${systemAccountId}`
}

function groupDetailPath(groupId: string): string {
  return `GET /__aisys__/api/groups/${groupId}?systemAccountId=${systemAccountId}`
}

function routeStrategiesListPath(): string {
  return `GET /__aisys__/api/route-strategies?page=1&pageSize=200&systemAccountId=${systemAccountId}`
}

function routeStrategyDetailPath(routeStrategyId = selectedRouteStrategyId): string {
  return `GET /__aisys__/api/route-strategies/${routeStrategyId}?systemAccountId=${systemAccountId}`
}

function groupPatchPath(groupId: string): string {
  return `PATCH /__aisys__/api/groups/${groupId}?systemAccountId=${systemAccountId}`
}

function groupDeletePath(groupId: string): string {
  return `DELETE /__aisys__/api/groups/${groupId}?systemAccountId=${systemAccountId}`
}

function providersPath(): string {
  return `GET /__aisys__/api/providers/options?systemAccountId=${systemAccountId}`
}

function modelOptionsPath(): string {
  return `GET /__aisys__/api/providers/models/options?systemAccountId=${systemAccountId}`
}

function accountTestOptionsPath(accountId = configuredAccountId): string {
  return `GET /__aisys__/api/accounts/${encodeURIComponent(accountId)}/test-options?systemAccountId=${systemAccountId}`
}

function clientIPStatsPath(): string {
  return 'GET /__aisys__/api/ip-stats?page=1&pageSize=20&sortField=requestCount&sortOrder=desc'
}

function clientIPStatsDetailPath(ipHash = sensitiveClientIPHash): string {
  return `GET /__aisys__/api/ip-stats/${encodeURIComponent(ipHash)}/detail?startDate=2026-07-14&endDate=2026-07-14&page=1&pageSize=20&sortOrder=asc`
}

function assertRequestHeaders(): void {
  for (const record of requestRecords) {
    assert.equal(record.headers.cookie, cookie)
    assert.equal(record.headers.accept, 'application/json')
    assert.equal(record.headers['user-agent'], 'juhe-ai-plan0081-real-go-management-smoke/1.0')
    assert.equal(record.headers['x-juhe-ai-smoke'], 'plan0081-real-go-management')
    if (record.body !== undefined) {
      assert.equal(record.headers['content-type'], 'application/json')
    }
  }
}

function assertExternalIntegrationSourceDetailRequest(
  sourceId = selectedExternalIntegrationSourceId
): void {
  const expectedUrl = externalIntegrationSourceDetailPath(sourceId).replace(/^GET /, '')
  const detailRequests = requestRecords.filter((record) => record.url === expectedUrl)
  assert.equal(detailRequests.length, 1, '详情应只读取第一页第一个非内置来源一次')
  const detailRequest = detailRequests[0]
  assert(detailRequest)
  assert.equal(detailRequest.method, 'GET')
  assert.equal(detailRequest.body, undefined)
  assert.equal(new URL(detailRequest.url ?? '/', 'http://127.0.0.1').search, '')
}

function assertNoExternalIntegrationSourceSecretRequest(): void {
  assert.equal(
    requestRecords.some((record) => {
      const pathname = new URL(record.url ?? '/', 'http://127.0.0.1').pathname
      return pathname.startsWith('/__aisys__/api/external-integration-sources/')
        && pathname.includes('/tokens/')
        && pathname.endsWith('/secret')
    }),
    false,
    '外部来源详情 smoke 不得调用 Token secret endpoint'
  )
}

function assertNoCookieLeak(messages: string[]): void {
  assert.equal(messages.some((line) => line.includes(cookie)), false, 'output must not expose the Cookie header')
}

function assertNoEnvironmentIdentifierLeak(
  messages: string[],
  baseUrl: string,
  additionalIdentifiers: readonly string[] = []
): void {
  const identifiers = [
    cookie,
    baseUrl,
    systemAccountId,
    selectedGroupId,
    temporaryGroupId,
    sensitiveClientIPHash,
    explicitClientIPHash,
    listedPublicApiLogId,
    configuredPublicApiLogId,
    selectedExternalIntegrationSourceId,
    selectedRouteStrategyId,
    missingRouteStrategyId,
    configuredAccountId,
    mismatchedAccountId,
    'PLAN-0081 normal route sensitive name',
    'PLAN-0081 sensitive route owner',
    'route-config-sensitive-model',
    'gpt',
    'openai',
    ...additionalIdentifiers
  ]
  for (const identifier of identifiers) {
    assert.equal(
      messages.some((message) => message.includes(identifier)),
      false,
      `output must not expose environment identifier: ${identifier}`
    )
  }
}

function clientIPStatsListFixture(rangeReady: boolean, includeItems = rangeReady): Record<string, unknown> {
  const items = includeItems ? clientIPStatsItemsFixture() : []
  return {
    items,
    pageUpperBound: items.length,
    hasMore: false,
    page: 1,
    pageSize: 20,
    range: {
      startDate: '2026-07-14',
      endDate: '2026-07-14',
      days: 1,
      maxDays: 31
    },
    rangeReady
  }
}

function clientIPStatsDetailFixture(ipHash: string, requestScenario: MockScenario): Record<string, unknown> {
  const rangeReady = requestScenario !== 'ip_stats_detail_not_ready'
  const includeItems = rangeReady && requestScenario !== 'ip_stats_detail_empty'
  const items = includeItems
    ? [
        {
          accountId: 'acct_plan0081_low_usage',
          accountName: '低用量账号',
          accountOwnerSystemAccountId: systemAccountId,
          accountOwnerSystemAccountName: 'PLAN-0081 系统账号',
          rangeUsage: clientIPUsageFixture({
            requestCount: 2,
            successCount: 2,
            errorCount: 0,
            inputTokens: 200,
            outputTokens: 50,
            activeDays: 1,
            averageDurationMs: 180.5,
            averageFirstTokenMs: 35.25,
            maxDurationMs: 260,
            lastUsedAt: '2026-07-14T07:30:00.000Z',
            lastErrorAt: '2026-07-14T06:30:00.000Z'
          })
        },
        {
          accountId: 'acct_plan0081_high_usage',
          rangeUsage: clientIPUsageFixture({
            requestCount: 8,
            successCount: 6,
            errorCount: 2,
            inputTokens: 1_000,
            outputTokens: 250,
            activeDays: 1
          })
        }
      ]
    : []
  return {
    ipHash,
    aggregateIpKey: '203.0.113.0/24',
    lastSeenAt: '2026-07-14T08:30:00.000Z',
    items,
    pageUpperBound: items.length,
    hasMore: false,
    page: 1,
    pageSize: 20,
    range: {
      startDate: '2026-07-14',
      endDate: '2026-07-14',
      days: 1,
      maxDays: 31
    },
    rangeReady
  }
}

function clientIPStatsItemsFixture(): Record<string, unknown>[] {
  return [
    {
      ipHash: sensitiveClientIPHash,
      aggregateIpKey: '203.0.113.0/24',
      lastSeenAt: '2026-07-14T08:30:00.000Z',
      status: 'blacklisted',
      rangeUsage: clientIPUsageFixture({
        requestCount: 10,
        successCount: 8,
        errorCount: 2,
        inputTokens: 1_200,
        outputTokens: 300,
        activeDays: 1,
        averageDurationMs: 425.5,
        averageFirstTokenMs: 80.25,
        maxDurationMs: 900,
        lastUsedAt: '2026-07-14T08:25:00.000Z',
        lastErrorAt: '2026-07-14T07:00:00.000Z'
      })
    },
    {
      ipHash: 'b'.repeat(64),
      aggregateIpKey: '198.51.100.0/24',
      status: 'allowlisted',
      rangeUsage: clientIPUsageFixture({
        requestCount: 0,
        successCount: 0,
        errorCount: 0,
        inputTokens: 0,
        outputTokens: 0,
        activeDays: 0
      })
    },
    {
      ipHash: 'c'.repeat(64),
      aggregateIpKey: '192.0.2.0/24',
      lastSeenAt: '',
      status: 'normal',
      rangeUsage: clientIPUsageFixture({
        requestCount: 4,
        successCount: 3,
        errorCount: 1,
        inputTokens: 80,
        outputTokens: 20,
        activeDays: 1
      })
    }
  ]
}

function clientIPHashFromDetailPath(pathname: string): string | undefined {
  const match = /^\/__aisys__\/api\/ip-stats\/([^/]+)\/detail$/.exec(pathname)
  return match?.[1] ? decodeURIComponent(match[1]) : undefined
}

function clientIPUsageFixture(overrides: Record<string, unknown>): Record<string, unknown> {
  const requestCount = Number(overrides.requestCount ?? 0)
  const errorCount = Number(overrides.errorCount ?? 0)
  const inputTokens = Number(overrides.inputTokens ?? 0)
  const outputTokens = Number(overrides.outputTokens ?? 0)
  return {
    requestCount,
    successCount: 0,
    errorCount,
    errorRate: requestCount > 0 ? errorCount / requestCount : 0,
    inputTokens,
    outputTokens,
    cacheReadTokens: 5,
    cacheReadCost: 0.001,
    cacheWriteTokens: 3,
    cacheWrite1hTokens: 2,
    cacheWriteCost: 0.002,
    thinkingTokens: 1,
    inputImageTokens: 0,
    outputImageTokens: 0,
    totalTokens: inputTokens + outputTokens,
    totalCost: 0.03,
    activeDays: 0,
    ...overrides
  }
}

async function captureFailureMessage(operation: Promise<unknown>): Promise<string> {
  try {
    await operation
  } catch (error) {
    assert(error instanceof Error)
    return error.message
  }
  assert.fail('expected operation to fail')
}

function delay(milliseconds: number): Promise<void> {
  return new Promise((resolvePromise) => setTimeout(resolvePromise, milliseconds))
}

function listen(target: Server): Promise<void> {
  return new Promise((resolvePromise, rejectPromise) => {
    target.once('error', rejectPromise)
    target.listen(0, '127.0.0.1', () => {
      target.off('error', rejectPromise)
      resolvePromise()
    })
  })
}

function close(target: Server): Promise<void> {
  return new Promise((resolvePromise, rejectPromise) => {
    target.close((error) => {
      if (error) {
        rejectPromise(error)
        return
      }
      resolvePromise()
    })
  })
}

function serverBaseUrl(target: Server): string {
  const address = target.address()
  assert(address && typeof address !== 'string')
  return `http://127.0.0.1:${(address as AddressInfo).port}`
}
