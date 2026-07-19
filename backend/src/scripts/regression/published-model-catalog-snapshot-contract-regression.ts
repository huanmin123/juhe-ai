import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const schema = readFileSync(new URL('../../storage/schema/business-schema.ts', import.meta.url), 'utf8')
assert.match(schema, /CREATE TABLE IF NOT EXISTS gateway_model_catalog_snapshots/, '业务 schema 必须持久化网关发布模型快照')
assert.match(schema, /PRIMARY KEY \(system_account_id, protocol, variant\)/, '发布快照必须按系统账户、协议和变体唯一')
assert.match(schema, /custom_provider_models[\s\S]*catalog_visible INTEGER NOT NULL DEFAULT 1/, '自定义模型必须有独立发布开关')

const postgresSchema = readFileSync(new URL('../../../../backend-go/db/migrations/000058_w2_published_gateway_model_catalog_snapshots.sql', import.meta.url), 'utf8')
assert.match(postgresSchema, /gateway_model_catalog_snapshots/, 'PostgreSQL 当前 schema 必须包含发布快照表')
assert.match(postgresSchema, /custom_provider_models[\s\S]*catalog_visible boolean NOT NULL DEFAULT true/i, 'PostgreSQL 自定义模型必须有发布开关')

const snapshotService = readFileSync(new URL('../../modules/model-pricing/published-model-catalog.service.ts', import.meta.url), 'utf8')
assert.match(snapshotService, /createSharedJsonCache<PublishedModelCatalogCacheEntry>/, '发布快照必须镜像到 Redis 单 key')
assert.match(snapshotService, /findGatewayModelCatalogSnapshotAsync/, 'Redis miss 只能读取一行持久化快照')
assert.doesNotMatch(snapshotService, /listCachedProviderModelCatalogAsync/, '请求读取服务不得调用运行态模型目录构建')
assert.match(snapshotService, /rebuildPublishedModelCatalogSnapshotsForSystemAccountAsync/, '写路径必须提供系统账户级快照重建入口')

const fixedResponses = readFileSync(new URL('../../modules/gateway/response/fixed-responses.ts', import.meta.url), 'utf8')
assert.match(fixedResponses, /readPublishedModelCatalogResponseAsync/, '/v1/models 必须直读已发布最终响应')
assert.doesNotMatch(fixedResponses, /listProviderScopedModelCatalog/, '/v1/models 不得按供应商 fan-out 构建目录')

const customRepository = readFileSync(new URL('../../storage/custom-provider-models.repository.ts', import.meta.url), 'utf8')
assert.match(customRepository, /catalogVisible/, '自定义模型 repository 必须读写发布开关')

console.log('已发布模型目录持久化快照契约回归通过')
