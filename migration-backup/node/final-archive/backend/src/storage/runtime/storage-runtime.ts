export type StorageRuntimeMode = 'standalone' | 'performance'

export type StorageAdapterStatus = 'planned' | 'wrapping-existing-repositories'

export interface StoragePortDescriptor {
  readonly name: string
  readonly status: StorageAdapterStatus
  readonly description: string
}

export interface StorageDriverDescriptor {
  readonly database: 'sqlite' | 'postgres'
  readonly cache: 'memory' | 'redis'
  readonly runtimeState: 'memory' | 'redis'
  readonly queue: 'memory' | 'redis_stream'
}

export interface StorageRuntime {
  readonly mode: StorageRuntimeMode
  readonly drivers: StorageDriverDescriptor
  readonly ports: Readonly<Record<string, StoragePortDescriptor>>
}

export const storagePortDescriptors = {
  systemAccounts: plannedPort('systemAccounts', '系统账户、登录凭据和会话'),
  providers: plannedPort('providers', '供应商、协议档案和模型目录'),
  accounts: plannedPort('accounts', 'AI 账户、凭据、标签、模型能力和账号运行态事实'),
  groups: plannedPort('groups', '分组、分组账号绑定和策略路由绑定读取'),
  apiKeys: plannedPort('apiKeys', 'API Key 管理、密钥校验和路由绑定'),
  authorizations: plannedPort('authorizations', '统一授权、团队授权、授权实例和额度配置'),
  gatewayRuntime: plannedPort('gatewayRuntime', '网关运行时 Key、分组、账号、策略和响应检查快照'),
  usageRecords: plannedPort('usageRecords', '使用记录写入、列表、详情和 usage catalog'),
  auditLogs: plannedPort('auditLogs', '原始审计、payload blob、错误聚合和保留清理'),
  stats: plannedPort('stats', '统计桶、范围窗口、TopN、额度窗口和系统指标'),
  maintenance: plannedPort('maintenance', '删除记录清理、保留期清理和后台游标'),
  cache: plannedPort('cache', '可丢弃 shared cache / read-through cache'),
  runtimeState: plannedPort('runtimeState', '短 TTL 运行态、限流、并发槽、锁和版本广播'),
  queue: plannedPort('queue', 'usage、audit、operation、runtime log 和 record maintenance 队列')
} as const satisfies Readonly<Record<string, StoragePortDescriptor>>

function plannedPort(name: string, description: string): StoragePortDescriptor {
  return {
    name,
    status: 'planned',
    description
  }
}
