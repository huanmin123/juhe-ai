import { closePostgresPool } from '../../storage/postgres-client.js'
import {
  runAccountHealthCheckEndpointModeMigration,
  type AccountHealthCheckEndpointModeMigrationOptions
} from './account-health-check-endpoint-mode-migration.js'

const options = parseOptions(process.argv.slice(2))

if (options.mode === 'execute') {
  const confirmed = process.env.JUHE_AI_OFFLINE_MAINTENANCE_CONFIRMED?.trim().toLowerCase()
  if (!confirmed || !['1', 'true', 'yes', 'on'].includes(confirmed)) {
    throw new Error('正式迁移必须先停止主服务和 worker，并设置 JUHE_AI_OFFLINE_MAINTENANCE_CONFIRMED=1')
  }
}

try {
  const stats = await runAccountHealthCheckEndpointModeMigration(options)
  process.stdout.write(`${JSON.stringify({ event: 'account_health_check_endpoint_mode_migration_completed', phase: options.mode, ...stats })}\n`)
} finally {
  await closePostgresPool()
}

function parseOptions(args: string[]): AccountHealthCheckEndpointModeMigrationOptions {
  const execute = args.includes('--execute')
  const verify = args.includes('--verify')
  if (execute && verify) throw new Error('--execute 与 --verify 不能同时使用')
  const batchArg = args.find((arg) => arg.startsWith('--batch-size='))
  const batchSize = batchArg ? Number(batchArg.slice('--batch-size='.length)) : 100
  if (!Number.isInteger(batchSize) || batchSize < 1 || batchSize > 1000) {
    throw new Error('--batch-size 必须是 1 到 1000 的整数')
  }
  return {
    mode: execute ? 'execute' : verify ? 'verify' : 'dry-run',
    batchSize
  }
}
