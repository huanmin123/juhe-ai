import { backgroundScheduledJobs, backgroundWorkerRegistry } from './background-job-registry.entries.js'

export { backgroundScheduledJobs, backgroundWorkerRegistry } from './background-job-registry.entries.js'

export type BackgroundJobKind =
  | 'sample'
  | 'ingest'
  | 'stats'
  | 'snapshot'
  | 'probe'
  | 'maintenance'
  | 'log'
  | 'control'

export type BackgroundJobLifecycle = 'persistent' | 'temporary' | 'hybrid'

export type BackgroundWorkerRole =
  | 'ingest-worker'
  | 'stats-worker'
  | 'ops-worker'
  | 'temporary-maintenance-worker'
  | 'worker-control'

export type BackgroundJobCategory =
  | 'scheduled'
  | 'ipc-queue'
  | 'control-ipc'
  | 'local-queue'
  | 'entrypoint'
  | 'maintenance-task'

export interface BackgroundJobRegistryEntry {
  jobName: string
  category: BackgroundJobCategory
  kind: BackgroundJobKind
  lifecycle: BackgroundJobLifecycle
  defaultRole: BackgroundWorkerRole
  hotspot: boolean
  singleOwner: boolean
  shardable: boolean
  leaseRequired: boolean
  blocksUserVisibleFreshness: boolean
  writes: readonly string[]
  notes?: string
}

export type BackgroundScheduledJobName = typeof backgroundScheduledJobs[number]['jobName']

export type BackgroundRegisteredJobName = typeof backgroundWorkerRegistry[number]['jobName']

const scheduledJobNames = new Set<string>(backgroundScheduledJobs.map((job) => job.jobName))
const registeredJobNames = new Set<string>(backgroundWorkerRegistry.map((job) => job.jobName))

export function backgroundScheduledJobName(name: BackgroundScheduledJobName): BackgroundScheduledJobName {
  if (!scheduledJobNames.has(name)) {
    throw new Error(`未登记的后台定时任务：${name}`)
  }
  return name
}

export function backgroundRegisteredJobName(name: BackgroundRegisteredJobName): BackgroundRegisteredJobName {
  if (!registeredJobNames.has(name)) {
    throw new Error(`未登记的后台任务或队列：${name}`)
  }
  return name
}

export function getBackgroundJobRegistryEntry(name: string): BackgroundJobRegistryEntry | undefined {
  return backgroundWorkerRegistry.find((job) => job.jobName === name)
}
