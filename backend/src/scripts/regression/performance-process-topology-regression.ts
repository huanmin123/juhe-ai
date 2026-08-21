import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { resolveRuntimeReadiness } from '../../shared/runtime-readiness.js'

const childMode = process.env.JUHE_AI_PERFORMANCE_TOPOLOGY_CHILD

if (childMode) {
  const { runtimeConfig } = await import('../../config/runtime.js')
  const { getBackgroundWorkerSupervisorRuntime } = await import('../../modules/background/background-worker-supervisor.js')
  const { currentProcessEventLoopRole } = await import('../../shared/process-event-loop-monitor.js')
  const processes = getBackgroundWorkerSupervisorRuntime()

  if (childMode === 'standalone') {
    assert.equal(runtimeConfig.runtimeMode, 'standalone')
    assert.equal(runtimeConfig.performanceNodeRole, 'combined')
    assert.equal(runtimeConfig.topology.gatewayReplicas, 1)
    assert.equal(runtimeConfig.topology.backgroundWorkerSupervisorEnabled, true)
    assert.equal(currentProcessEventLoopRole(), 'server')
    assert.deepEqual(processes.map((item) => `${item.role}:${item.replicaIndex}`), [
      'ingest-worker:0',
      'stats-worker:0',
      'ops-worker:0'
    ])
  } else if (childMode === 'performance-default') {
    assert.equal(runtimeConfig.runtimeMode, 'performance')
    assert.equal(runtimeConfig.performanceNodeRole, 'combined')
    assert.deepEqual(runtimeConfig.topology, {
      backgroundWorkerSupervisorEnabled: true,
      controlReplicas: 1,
      gatewayReplicas: 3,
      usageWorkerReplicas: 2,
      logWorkerReplicas: 2,
      statsWorkerReplicas: 1,
      opsWorkerReplicas: 1
    })
    assert.match(currentProcessEventLoopRole(), /^control:process-\d+$/)
    assert.deepEqual(processes.map((item) => `${item.role}:${item.replicaIndex}`), [
      'usage-worker:0',
      'usage-worker:1',
      'log-worker:0',
      'log-worker:1',
      'stats-worker:0',
      'ops-worker:0'
    ])
  } else if (childMode === 'performance-gateway') {
    assert.equal(runtimeConfig.performanceNodeRole, 'gateway')
    assert.equal(runtimeConfig.instanceId, 'gateway-2')
    assert.equal(runtimeConfig.topology.backgroundWorkerSupervisorEnabled, false)
    assert.equal(runtimeConfig.topology.gatewayReplicas, 5)
    assert.equal(runtimeConfig.topology.usageWorkerReplicas, 4)
    assert.equal(runtimeConfig.topology.logWorkerReplicas, 3)
    assert.equal(currentProcessEventLoopRole(), 'gateway:gateway-2')
    assert.deepEqual(processes, [])
  } else if (childMode === 'performance-control-replica') {
    assert.equal(runtimeConfig.performanceNodeRole, 'control-replica')
    assert.equal(runtimeConfig.instanceId, 'control-2')
    assert.equal(runtimeConfig.topology.backgroundWorkerSupervisorEnabled, false)
    assert.equal(runtimeConfig.topology.controlReplicas, 3)
    assert.equal(currentProcessEventLoopRole(), 'control-replica:control-2')
    assert.deepEqual(processes, [])
  } else if (childMode === 'performance-control-primary-with-read-replicas') {
    assert.equal(runtimeConfig.performanceNodeRole, 'control')
    assert.equal(runtimeConfig.topology.controlReplicas, 3)
    assert.deepEqual(runtimeConfig.controlReadReplicaOrigins, ['http://127.0.0.1:3201', 'http://127.0.0.1:3202'])
    assert.equal(runtimeConfig.topology.backgroundWorkerSupervisorEnabled, true)
  } else {
    assert.equal(runtimeConfig.processRole, 'worker')
    assert.equal(runtimeConfig.workerRole, 'usage-worker')
    assert.equal(runtimeConfig.workerReplicaIndex, 1)
    assert.equal(currentProcessEventLoopRole(), 'usage-worker:2')
  }
  process.exit(0)
}

const serverSource = readFileSync(new URL('../../server.ts', import.meta.url), 'utf8')
const supervisorSource = readFileSync(new URL('../../modules/background/background-worker-supervisor.ts', import.meta.url), 'utf8')
const metricsRegistrySource = readFileSync(new URL('../../shared/performance-process-metrics-registry.ts', import.meta.url), 'utf8')
const backgroundJobsSource = readFileSync(new URL('../../modules/background/background-jobs.ts', import.meta.url), 'utf8')
assert.deepEqual(
  resolveRuntimeReadiness({
    dbServiceReady: true,
    workerTopologyReady: false,
    topologyGatesHealth: true
  }),
  {
    statusCode: 503,
    status: 'starting',
    dbServiceReady: true,
    workerTopologyReady: false,
    blockers: ['worker_topology_not_ready']
  },
  'performance control 必须等待全部 worker ready 后才返回 200 health'
)
assert.deepEqual(
  resolveRuntimeReadiness({
    dbServiceReady: true,
    workerTopologyReady: false,
    topologyGatesHealth: false
  }),
  {
    statusCode: 200,
    status: 'ok',
    dbServiceReady: true,
    workerTopologyReady: false,
    blockers: []
  },
  '不负责 worker 拓扑的运行时不得被 worker 未就绪阻塞 health'
)
assert.match(metricsRegistrySource, /runtimeMode === 'performance'[\s\S]*cacheDriver === 'redis'/, '进程指标注册表只能在高性能 Redis 模式启用')
assert.match(metricsRegistrySource, /registryTtlSeconds = 20/, '进程指标注册必须使用短 TTL 避免退出节点残留')
assert.match(backgroundJobsSource, /readPerformanceProcessEventLoopSamples\(\)[\s\S]*回退 IPC 采样/, 'Stats Worker 必须优先汇总注册表并在失败时回退 IPC')
assert.match(
  backgroundJobsSource,
  /performanceProcessMetricsTopologyComplete\(registryProcessSamples, runtimeConfig\.topology\)/,
  'Stats Worker 必须按拓扑角色和副本核验完整性，不能只比较样本条数'
)
assert.doesNotMatch(backgroundJobsSource, /registryProcessSamples\.length\s*>?=/, '重复旧实例不得用数量掩盖当前角色缺失')
assert.match(serverSource, /startInternalGatewayRegistryWhenReady[\s\S]*startInternalGatewayRegistry\(\)/, 'Gateway 实例必须在监听和 DB service ready 后注册内部调度端点')
assert.match(
  serverSource,
  /app\.use\(accountTestDispatchInternalPrefix, controlReadReplicaPrimaryOnlyRequestGuard\)[\s\S]*app\.use\(accountHealthCheckDispatchInternalPrefix, controlReadReplicaPrimaryOnlyRequestGuard\)/,
  'control-replica 必须拒绝所有直连内部派发写接口'
)
assert.match(
  supervisorSource,
  /attachBackgroundAuxiliaryWorkerProcess\(child/,
  '非 primary Usage / Log worker ready 后必须保留父进程 IPC 桥'
)

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-ai-performance-topology-'))
try {
  runChild('standalone', {
    JUHE_AI_RUNTIME_MODE: 'standalone',
    JUHE_AI_DATABASE_DRIVER: 'sqlite',
    JUHE_AI_CACHE_DRIVER: 'memory',
    JUHE_AI_RUNTIME_STATE_DRIVER: 'memory',
    JUHE_AI_QUEUE_DRIVER: 'memory',
    JUHE_AI_GATEWAY_REPLICAS: 'not-a-performance-value',
    JUHE_AI_USAGE_WORKER_REPLICAS: '0',
    JUHE_AI_LOG_WORKER_REPLICAS: '999'
  })
  runChild('performance-default', performanceEnv())
  runChildFailure('performance-production-missing-instance-id', {
    ...performanceEnv(),
    NODE_ENV: 'production',
    JUHE_AI_INSTANCE_ID: ''
  }, 'JUHE_AI_INSTANCE_ID')
  runChild('performance-gateway', {
    ...performanceEnv(),
    JUHE_AI_PERFORMANCE_NODE_ROLE: 'gateway',
    JUHE_AI_ACCOUNT_HEALTH_CHECK_DISPATCH_URL: 'http://127.0.0.1:65535',
    JUHE_AI_INSTANCE_ID: 'gateway-2',
    JUHE_AI_GATEWAY_REPLICAS: '5',
    JUHE_AI_USAGE_WORKER_REPLICAS: '4',
    JUHE_AI_LOG_WORKER_REPLICAS: '3'
  })
  runChild('performance-control-replica', {
    ...performanceEnv(),
    JUHE_AI_PERFORMANCE_NODE_ROLE: 'control-replica',
    JUHE_AI_ACCOUNT_HEALTH_CHECK_DISPATCH_URL: 'http://127.0.0.1:3200',
    JUHE_AI_INSTANCE_ID: 'control-2',
    JUHE_AI_CONTROL_REPLICAS: '3',
    JUHE_AI_GATEWAY_REPLICAS: '5'
  })
  runChildFailure('performance-control-replica-missing-health-dispatch', {
    ...performanceEnv(),
    JUHE_AI_PERFORMANCE_NODE_ROLE: 'control-replica',
    JUHE_AI_ACCOUNT_HEALTH_CHECK_DISPATCH_URL: ''
  }, 'JUHE_AI_ACCOUNT_HEALTH_CHECK_DISPATCH_URL')
  runChild('performance-control-primary-with-read-replicas', {
    ...performanceEnv(),
    JUHE_AI_PERFORMANCE_NODE_ROLE: 'control',
    JUHE_AI_INSTANCE_ID: 'control-1',
    JUHE_AI_CONTROL_REPLICAS: '3',
    JUHE_AI_CONTROL_READ_REPLICA_ORIGINS: 'http://127.0.0.1:3201,http://127.0.0.1:3202'
  })
  runChildFailure('performance-control-primary-missing-read-replicas', {
    ...performanceEnv(),
    JUHE_AI_PERFORMANCE_NODE_ROLE: 'control',
    JUHE_AI_INSTANCE_ID: 'control-1',
    JUHE_AI_CONTROL_REPLICAS: '3',
    JUHE_AI_CONTROL_READ_REPLICA_ORIGINS: ''
  }, 'JUHE_AI_CONTROL_READ_REPLICA_ORIGINS')
  runChildFailure('performance-gateway-missing-health-dispatch', {
    ...performanceEnv(),
    JUHE_AI_PERFORMANCE_NODE_ROLE: 'gateway',
    JUHE_AI_ACCOUNT_HEALTH_CHECK_DISPATCH_URL: ''
  }, 'JUHE_AI_ACCOUNT_HEALTH_CHECK_DISPATCH_URL')
  runChildFailure('performance-gateway-remote-health-dispatch', {
    ...performanceEnv(),
    JUHE_AI_PERFORMANCE_NODE_ROLE: 'gateway',
    JUHE_AI_ACCOUNT_HEALTH_CHECK_DISPATCH_URL: 'http://192.0.2.1:3200'
  }, 'JUHE_AI_ACCOUNT_HEALTH_CHECK_DISPATCH_URL')
  runChildFailure('performance-gateway-path-health-dispatch', {
    ...performanceEnv(),
    JUHE_AI_PERFORMANCE_NODE_ROLE: 'gateway',
    JUHE_AI_ACCOUNT_HEALTH_CHECK_DISPATCH_URL: 'http://127.0.0.1:3200/custom-path'
  }, 'JUHE_AI_ACCOUNT_HEALTH_CHECK_DISPATCH_URL')
  runChild('performance-worker', {
    ...performanceEnv(),
    JUHE_AI_PROCESS_ROLE: 'worker',
    JUHE_AI_WORKER_ROLE: 'usage-worker',
    JUHE_AI_WORKER_REPLICA_INDEX: '1',
    JUHE_AI_INSTANCE_ID: 'usage-worker-2'
  })
} finally {
  rmSync(tempRoot, { recursive: true, force: true })
}

console.log('高性能同机进程拓扑回归通过：standalone 三角色不变，performance 默认 3/2/2/1/1，gateway 节点不启动后台 worker')

function runChild(mode: string, overrides: NodeJS.ProcessEnv): void {
  const result = spawnSync(process.execPath, ['--import', 'tsx', fileURLToPath(import.meta.url)], {
    cwd: fileURLToPath(new URL('../../../', import.meta.url)),
    env: {
      ...process.env,
      NODE_ENV: '',
      JUHE_AI_PERFORMANCE_TOPOLOGY_CHILD: mode,
      JUHE_AI_ENV_FILE: '',
      JUHE_AI_LOG_FILE_ENABLED: 'true',
      JUHE_AI_LOG_DIR: tempRoot,
      JUHE_AI_LOG_CONSOLE_ENABLED: 'false',
      ...overrides
    },
    encoding: 'utf8'
  })
  assert.equal(result.status, 0, `topology child ${mode} failed\nstdout=${result.stdout}\nstderr=${result.stderr}`)
}

function runChildFailure(mode: string, overrides: NodeJS.ProcessEnv, expectedMessage: string): void {
  const result = spawnSync(process.execPath, ['--import', 'tsx', fileURLToPath(import.meta.url)], {
    cwd: fileURLToPath(new URL('../../../', import.meta.url)),
    env: {
      ...process.env,
      JUHE_AI_PERFORMANCE_TOPOLOGY_CHILD: mode,
      JUHE_AI_ENV_FILE: '',
      JUHE_AI_LOG_FILE_ENABLED: 'true',
      JUHE_AI_LOG_DIR: tempRoot,
      JUHE_AI_LOG_CONSOLE_ENABLED: 'false',
      ...overrides
    },
    encoding: 'utf8'
  })
  assert.notEqual(result.status, 0, `topology child ${mode} 应拒绝非法 performance 配置`)
  assert.match(`${result.stdout}\n${result.stderr}`, new RegExp(expectedMessage), `topology child ${mode} 应报告对应配置名`)
}

function performanceEnv(): NodeJS.ProcessEnv {
  return {
    JUHE_AI_RUNTIME_MODE: 'performance',
    JUHE_AI_DATABASE_DRIVER: 'postgres',
    JUHE_AI_CACHE_DRIVER: 'redis',
    JUHE_AI_RUNTIME_STATE_DRIVER: 'redis',
    JUHE_AI_QUEUE_DRIVER: 'redis_stream',
    JUHE_AI_POSTGRES_URL: 'postgres://juhe:secret@127.0.0.1:6432/juhe',
    JUHE_AI_REDIS_CACHE_URL: 'redis://127.0.0.1:6379/0',
    JUHE_AI_REDIS_STATE_URL: 'redis://127.0.0.1:6380/0',
    JUHE_AI_REDIS_QUEUE_URL: 'redis://127.0.0.1:6381/0',
    JUHE_AI_REDIS_NAMESPACE: 'topology-regression',
    JUHE_AI_SECRET: 'performance-process-topology-regression-secret'
  }
}
