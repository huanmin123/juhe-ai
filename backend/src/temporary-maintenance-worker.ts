import { runTemporaryMaintenanceWorker } from './modules/record-maintenance/temporary-maintenance-worker-runner.js'

const runId = process.argv[2]?.trim()

if (!runId) {
  console.error('临时维护 worker 缺少 runId')
  process.exit(1)
}

const exitCode = await runTemporaryMaintenanceWorker(runId)
process.exit(exitCode)
