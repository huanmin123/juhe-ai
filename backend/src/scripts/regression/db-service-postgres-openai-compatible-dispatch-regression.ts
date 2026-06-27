import assert from 'node:assert/strict'

import type { DbServiceOperation } from '../../modules/db-service/db-service-types.js'

process.env.JUHE_AI_RUNTIME_MODE = 'performance'
process.env.JUHE_AI_DATABASE_DRIVER = 'postgres'
process.env.JUHE_AI_CACHE_DRIVER = 'redis'
process.env.JUHE_AI_RUNTIME_STATE_DRIVER = 'redis'
process.env.JUHE_AI_QUEUE_DRIVER = 'redis_stream'
process.env.JUHE_AI_POSTGRES_URL = 'postgresql://juhe_ai:unused@127.0.0.1:1/juhe_ai'
process.env.JUHE_AI_REDIS_CACHE_URL = 'redis://127.0.0.1:6379/0'
process.env.JUHE_AI_REDIS_STATE_URL = 'redis://127.0.0.1:6379/1'
process.env.JUHE_AI_REDIS_QUEUE_URL = 'redis://127.0.0.1:6379/2'

const [{ handleDbServiceOperation }, { closePostgresPool }] = await Promise.all([
  import('../../modules/db-service/db-service-handlers.js'),
  import('../../storage/postgres-client.js')
])

const common = {
  systemAccountId: 'sys_admin',
  apiKeyId: 'ak_postgres_dispatch'
}

const operations: DbServiceOperation[] = [
  {
    type: 'create_openai_compatible_file',
    input: {
      id: 'file_postgres_dispatch',
      ...common,
      purpose: 'assistants',
      filename: 'dispatch.txt',
      bytes: 8,
      mediaType: 'text/plain',
      storageKey: 'dispatch/file_postgres_dispatch',
      sha256: '0'.repeat(64)
    }
  },
  {
    type: 'list_openai_compatible_files',
    options: common
  },
  {
    type: 'get_openai_compatible_file',
    fileId: 'file_postgres_dispatch',
    ...common
  },
  {
    type: 'delete_openai_compatible_file',
    fileId: 'file_postgres_dispatch',
    ...common
  },
  {
    type: 'create_openai_compatible_vector_store',
    input: {
      id: 'vs_postgres_dispatch',
      ...common,
      name: 'Postgres dispatch'
    }
  },
  {
    type: 'list_openai_compatible_vector_stores',
    options: common
  },
  {
    type: 'get_openai_compatible_vector_store',
    vectorStoreId: 'vs_postgres_dispatch',
    ...common
  },
  {
    type: 'delete_openai_compatible_vector_store',
    vectorStoreId: 'vs_postgres_dispatch',
    ...common
  },
  {
    type: 'create_openai_compatible_vector_store_file',
    input: {
      vectorStoreId: 'vs_postgres_dispatch',
      fileId: 'file_postgres_dispatch',
      ...common,
      status: 'completed'
    }
  },
  {
    type: 'list_openai_compatible_vector_store_files',
    options: {
      vectorStoreId: 'vs_postgres_dispatch',
      ...common
    }
  },
  {
    type: 'get_openai_compatible_vector_store_file',
    vectorStoreId: 'vs_postgres_dispatch',
    fileId: 'file_postgres_dispatch',
    ...common
  },
  {
    type: 'delete_openai_compatible_vector_store_file',
    vectorStoreId: 'vs_postgres_dispatch',
    fileId: 'file_postgres_dispatch',
    ...common
  },
  {
    type: 'search_openai_compatible_vector_store',
    options: {
      vectorStoreId: 'vs_postgres_dispatch',
      ...common,
      query: 'dispatch'
    }
  },
  {
    type: 'list_openai_compatible_vector_store_file_chunks',
    vectorStoreId: 'vs_postgres_dispatch',
    fileId: 'file_postgres_dispatch',
    ...common
  }
]

for (const operation of operations) {
  await assertRejectsAtPostgresConnection(operation)
  await closePostgresPool().catch(() => undefined)
}

console.log('DB service PostgreSQL OpenAI-compatible files/vector stores dispatch 回归通过：postgres 模式不再回落 SQLite')

async function assertRejectsAtPostgresConnection(operation: DbServiceOperation): Promise<void> {
  try {
    await handleDbServiceOperation(operation)
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error)
    assert.ok(
      !message.includes('JUHE_AI_DATABASE_DRIVER=postgres 不能回退写入 SQLite')
        && !message.includes('尚未接入 PostgreSQL 数据库 driver'),
      `${operation.type} 不应回落到 SQLite driver：${message}`
    )
    assert.ok(
      isExpectedPostgresConnectionError(message),
      `${operation.type} 应进入 PostgreSQL 查询路径，而不是在业务代码中提前失败：${message}`
    )
    return
  }
  assert.fail(`${operation.type} 使用不可用 PostgreSQL 端口时不应成功`)
}

function isExpectedPostgresConnectionError(message: string): boolean {
  return message.includes('ECONNREFUSED')
    || message.includes('connect')
    || message.includes('Connection terminated')
    || message.includes('timeout')
}
