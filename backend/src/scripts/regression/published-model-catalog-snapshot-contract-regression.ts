import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const schema = readFileSync(new URL('../../storage/schema/business-schema.ts', import.meta.url), 'utf8')
assert.match(schema, /CREATE TABLE IF NOT EXISTS gateway_model_catalog_snapshots/, '业务 schema 必须持久化网关发布模型快照')
assert.match(schema, /PRIMARY KEY \(system_account_id, protocol, variant\)/, '发布快照必须按系统账户、协议和变体唯一')
assert.match(schema, /chat_list/, '业务 schema 必须允许独立聊天轻量列表快照')
assert.match(schema, /chat_model:/, '业务 schema 必须允许按模型 ID 定点保存聊天能力快照')
assert.match(schema, /custom_provider_models[\s\S]*catalog_visible INTEGER NOT NULL DEFAULT 1/, '自定义模型必须有独立发布开关')

const postgresSchema = readFileSync(new URL('../../../../backend-go/db/migrations/000059_w2_published_gateway_model_catalog_snapshots.sql', import.meta.url), 'utf8')
assert.match(postgresSchema, /gateway_model_catalog_snapshots/, 'PostgreSQL 当前 schema 必须包含发布快照表')
assert.match(postgresSchema, /chat_list:%/, 'PostgreSQL 当前 schema 必须允许供应商维度轻量聊天列表快照')
assert.match(postgresSchema, /chat_model:%/, 'PostgreSQL 当前 schema 必须允许按模型维度聊天能力快照')
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
assert.doesNotMatch(snapshotService, /snapshotInput\('openai', 'default'/, '发布快照不得继续生成网关 OpenAI default 响应')
assert.doesNotMatch(snapshotService, /snapshotInput\('openai', 'codex'/, '发布快照不得继续生成网关 Codex 响应')
assert.doesNotMatch(snapshotService, /snapshotInput\('anthropic', 'default'/, '发布快照不得继续生成网关 Anthropic 响应')
assert.doesNotMatch(snapshotService, /snapshotInput\('gemini', 'default'/, '发布快照不得继续生成网关 Gemini 响应')
assert.match(snapshotService, /pruneGatewayModelCatalogSnapshotsAsync/, '全量重建必须清理已停用或删除 owner 的旧快照')
assert.match(snapshotService, /rebuildPublishedModelCatalogSnapshotsBestEffortAsync/, '模型事实提交后快照失败必须有界后台重试，不能把已提交事实返回成失败')
assert.match(
  snapshotService,
  /rebuildPublishedModelCatalogSnapshotsAfterModelChangeAsync[\s\S]*enqueueSnapshotRebuild\(\(\) => rebuildPublishedModelCatalogSnapshotsAfterModelChangeInternalAsync\(\)\)/,
  '全量 prune 与 owner 重建必须整体进入同一串行队列'
)
assert.match(rebuildScript, /await closeRedisClients\(\)/, '模型目录离线重建完成后必须关闭 Redis 客户端，避免维护进程挂起')
assert.match(rebuildScript, /runtimeConfig\.processRole\s*=\s*'db-service'/, '模型目录离线重建必须使用业务库写角色，不能以 server 只读连接执行')
assert.match(rebuildScript, /closeSqliteReadWorkerPool/, '模型目录离线重建必须关闭 SQLite 读 worker 池，避免维护进程挂起')
assert.match(snapshotService, /await publishedModelCatalogCache\.clear\(\)[\s\S]*publishedModelCatalogLocalCache\.clear\(\)[\s\S]*replaceGatewayModelCatalogSnapshotsAsync/, '动态聊天快照必须在 durable 替换前统一失效，避免旧 variant 残留')
assert.match(snapshotService, /publishedModelCatalogLocalCache\.set\(publishedModelCatalogCacheKey\(snapshot\)/, 'Redis 或数据库回源后必须回填进程内快照')
assert.match(snapshotService, /publishedModelCatalogCache\.clear\(\)/, '全量模型重建必须清理旧模型能力缓存')
assert.match(
  snapshotService,
  /rebuildPublishedModelCatalogSnapshotsAfterModelChangeImplAsync[\s\S]*listGatewayModelCatalogSystemAccountIdsAsync[\s\S]*pruneGatewayModelCatalogSnapshotsAsync[\s\S]*rebuildPublishedModelCatalogSnapshotsForSystemAccountInternalAsync/,
  '串行全量重建必须在读取 active owner、prune 后直接执行内部 owner 重建，不能重新排队形成竞态或死锁'
)

const snapshotRepository = readFileSync(new URL('../../storage/gateway-model-catalog-snapshot.repository.ts', import.meta.url), 'utf8')
assert.match(snapshotRepository, /DELETE FROM \$\{snapshotTable\(tx\)\}[\s\S]*WHERE system_account_id = \?/, 'owner 快照替换前必须删除旧 variant')
assert.match(snapshotRepository, /WHERE system_account_id NOT IN/, '全量重建必须删除 inactive owner 残留快照')
assert.match(snapshotService, /snapshotInput\('openai', chatModelListSnapshotVariant\(providerCode\)/, '聊天下拉必须按供应商生成独立轻量列表快照')
assert.match(snapshotService, /chatModelSnapshotVariant\(providerCode, model\.id\)/, '聊天模型能力必须按供应商和 modelId 生成独立快照行')
assert.doesNotMatch(snapshotService, /snapshotInput\('openai', 'chat', \{ data: buildChatModelOptions/, '不得继续把全部聊天能力写进单个宽快照')

const fixedResponses = readFileSync(new URL('../../modules/gateway/response/fixed-responses.ts', import.meta.url), 'utf8')
assert.match(fixedResponses, /listClientModelCatalogAsync/, '/v1/models 必须动态聚合客户端可见供应商目录')
assert.match(fixedResponses, /buildOpenAIModelsResponse\(catalog, req\)/, 'OpenAI 与 Codex 模型响应必须从同一动态目录按请求形态构建')
assert.doesNotMatch(fixedResponses, /readPublishedModelCatalogResponseAsync/, '/v1/models 不得继续读取 default\/codex 发布响应快照')

const customRepository = readFileSync(new URL('../../storage/custom-provider-models.repository.ts', import.meta.url), 'utf8')
assert.match(customRepository, /catalogVisible/, '自定义模型 repository 必须读写发布开关')

console.log('已发布模型目录持久化快照契约回归通过')
