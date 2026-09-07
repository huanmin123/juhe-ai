const runtimeLogInstanceIdPattern = '[A-Za-z0-9][A-Za-z0-9._-]{0,63}'
const runtimeLogWorkerRolePattern = 'worker|db-service|ingest-worker|usage-worker|log-worker|stats-worker|ops-worker|temporary-maintenance-worker'
const runtimeLogRotationSuffixPattern = /^(.*)\.(\d{8}T\d{6}Z)\.([0-9a-f-]+)\.log$/i
const runtimeLogWorkerInstancePattern = new RegExp(`^juhe-ai\.(${runtimeLogWorkerRolePattern})\.(${runtimeLogInstanceIdPattern})\.log$`)
const runtimeLogServerInstancePattern = new RegExp(`^juhe-ai\.(${runtimeLogInstanceIdPattern})\.log$`)

const runtimeLogLegacyCurrentRoles: Readonly<Record<string, string>> = {
  'juhe-ai.log': 'server',
  'juhe-ai.worker.log': 'worker',
  'juhe-ai.db-service.log': 'db-service',
  'juhe-ai.ingest-worker.log': 'ingest-worker',
  'juhe-ai.usage-worker.log': 'usage-worker',
  'juhe-ai.log-worker.log': 'log-worker',
  'juhe-ai.stats-worker.log': 'stats-worker',
  'juhe-ai.ops-worker.log': 'ops-worker',
  'juhe-ai.temporary-maintenance-worker.log': 'temporary-maintenance-worker'
}

export interface RuntimeLogFileNameMatch {
  role: string
  kind: 'current' | 'rotated'
  currentFileName: string
}

export function parseRuntimeLogFileName(fileName: string): RuntimeLogFileNameMatch | undefined {
  const rotatedMatch = runtimeLogRotationSuffixPattern.exec(fileName)
  if (rotatedMatch) {
    const currentFileName = `${rotatedMatch[1]}.log`
    const rotatedRole = runtimeLogCurrentFileRole(currentFileName)
    if (!rotatedRole) return undefined
    return {
      role: rotatedRole,
      kind: 'rotated',
      currentFileName
    }
  }

  const currentRole = runtimeLogCurrentFileRole(fileName)
  return currentRole
    ? { role: currentRole, kind: 'current', currentFileName: fileName }
    : undefined
}

export function isCurrentRuntimeLogFileName(fileName: string): boolean {
  return parseRuntimeLogFileName(fileName)?.kind === 'current'
}

export function isRotatedRuntimeLogFileName(fileName: string): boolean {
  return parseRuntimeLogFileName(fileName)?.kind === 'rotated'
}

function runtimeLogCurrentFileRole(fileName: string): string | undefined {
  const legacyRole = runtimeLogLegacyCurrentRoles[fileName]
  if (legacyRole) return legacyRole

  const workerMatch = runtimeLogWorkerInstancePattern.exec(fileName)
  if (workerMatch) return `${workerMatch[1]}:${workerMatch[2]}`

  const serverMatch = runtimeLogServerInstancePattern.exec(fileName)
  return serverMatch ? `server:${serverMatch[1]}` : undefined
}
