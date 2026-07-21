import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const schema = readFileSync(new URL('../../storage/schema/business-schema.ts', import.meta.url), 'utf8')
assert.match(schema, /CREATE TABLE IF NOT EXISTS gateway_model_catalog_snapshots/, '业务 schema 必须持久化网关发布模型快照')
assert.match(schema, /PRIMARY KEY \(system_account_id, protocol, variant\)/, '发布快照必须按系统账户、协议和变体唯一')
assert.match(schema, /custom_provider_models[\s\S]*catalog_visible INTEGER NOT NULL DEFAULT 1/, '自定义模型必须有独立发布开关')

const postgresSchema = readFileSync(new URL('../../../../backend-go/db/migrations/000059_w2_published_gateway_model_catalog_snapshots.sql', import.meta.url), 'utf8')
assert.match(postgresSchema, /gateway_model_catalog_snapshots/, 'PostgreSQL 当前 schema 必须包含发布快照表')
assert.match(postgresSchema, /custom_provider_models[\s\S]*catalog_visible boolean NOT NULL DEFAULT true/i, 'PostgreSQL 自定义模型必须有发布开关')

const snapshotService = readFileSync(new URL('../../modules/model-pricing/published-model-catalog.service.ts', import.meta.url), 'utf8')
const rebuildScript = readFileSync(new URL('../maintenance/rebuild-published-model-catalog-snapshots.ts', import.meta.url), 'utf8')
assert.match(snapshotService, /createSharedJsonCache<PublishedModelCatalogCacheEntry>/, '发布快照必须镜像到 Redis 单 key')
assert.match(snapshotService, /createProcessLocalResourceCache<string, PublishedModelCatalogCacheEntry>/, '发布快照必须保留进程内只读热路径')
assert.match(
  snapshotService,
  /const localCached = publishedModelCatalogLocalCache\.get\(cacheKey\)[\s\S]*if \(localCached\?\.payload\) return localCached\.payload[\s\S]*publishedModelCatalogCache\.get\(cacheKey\)/,
  '模型目录请求必须先命中进程内快照，再回退 Redis'
)
assert.match(snapshotService, /findGatewayModelCatalogSnapshotAsync/, 'Redis miss 只能读取一行持久化快照')
assert.doesNotMatch(snapshotService, /listCachedProviderModelCatalogAsync/, '请求读取服务不得调用运行态模型目录构建')
assert.match(snapshotService, /rebuildPublishedModelCatalogSnapshotsForSystemAccountAsync/, '写路径必须提供系统账户级快照重建入口')
assert.match(snapshotService, /OPENAI_COMPATIBLE_PROVIDER_CODE/, 'OpenAI 静态目录必须覆盖所有 OpenAI-compatible 供应商模型')
assert.match(snapshotService, /pruneGatewayModelCatalogSnapshotsAsync/, '全量重建必须清理已停用或删除 owner 的旧快照')
assert.match(snapshotService, /rebuildPublishedModelCatalogSnapshotsBestEffortAsync/, '模型事实提交后快照失败必须有界后台重试，不能把已提交事实返回成失败')
assert.match(
  snapshotService,
  /rebuildPublishedModelCatalogSnapshotsAfterModelChangeAsync[\s\S]*enqueueSnapshotRebuild\(\(\) => rebuildPublishedModelCatalogSnapshotsAfterModelChangeInternalAsync\(\)\)/,
  '全量 prune 与 owner 重建必须整体进入同一串行队列'
)
assert.match(rebuildScript, /await closeRedisClients\(\)/, '模型目录离线重建完成后必须关闭 Redis 客户端，避免维护进程挂起')
assert.match(snapshotService, /clearPublishedModelCatalogOwnerCacheAsync/, '单 owner 重建必须只失效该 owner 的固定快照键')
assert.match(snapshotService, /publishedModelCatalogCache\.delete\(cacheKey\)/, '单 owner 缓存失效必须使用精确 key delete')
assert.match(snapshotService, /publishedModelCatalogLocalCache\.delete\(cacheKey\)/, '单 owner 重建必须同步精确失效进程内快照')
assert.match(snapshotService, /publishedModelCatalogLocalCache\.set\(publishedModelCatalogCacheKey\(snapshot\)/, 'Redis 或数据库回源后必须回填进程内快照')
assert.equal(
  (snapshotService.match(/publishedModelCatalogCache\.clear\(\)/g) ?? []).length,
  1,
  '全量模型重建只能统一清空一次缓存，owner 并行重建不得互相驱逐'
)
assert.match(
  snapshotService,
  /rebuildPublishedModelCatalogSnapshotsAfterModelChangeImplAsync[\s\S]*listGatewayModelCatalogSystemAccountIdsAsync[\s\S]*pruneGatewayModelCatalogSnapshotsAsync[\s\S]*rebuildPublishedModelCatalogSnapshotsForSystemAccountInternalAsync/,
  '串行全量重建必须在读取 active owner、prune 后直接执行内部 owner 重建，不能重新排队形成竞态或死锁'
)

const snapshotRepository = readFileSync(new URL('../../storage/gateway-model-catalog-snapshot.repository.ts', import.meta.url), 'utf8')
assert.match(snapshotRepository, /DELETE FROM \$\{snapshotTable\(tx\)\}[\s\S]*WHERE system_account_id = \?/, 'owner 快照替换前必须删除旧 variant')
assert.match(snapshotRepository, /WHERE system_account_id NOT IN/, '全量重建必须删除 inactive owner 残留快照')

const fixedResponses = readFileSync(new URL('../../modules/gateway/response/fixed-responses.ts', import.meta.url), 'utf8')
assert.match(fixedResponses, /readPublishedModelCatalogResponseAsync/, '/v1/models 必须直读已发布最终响应')
assert.doesNotMatch(fixedResponses, /listProviderScopedModelCatalog/, '/v1/models 不得按供应商 fan-out 构建目录')

const customRepository = readFileSync(new URL('../../storage/custom-provider-models.repository.ts', import.meta.url), 'utf8')
assert.match(customRepository, /catalogVisible/, '自定义模型 repository 必须读写发布开关')

console.log('已发布模型目录持久化快照契约回归通过')
