import { dispatchAccountTestTasks } from '../accounts/account-test-task-queue.service.js'

export function dispatchAccountTestTask(taskId: string): boolean {
  const normalizedId = taskId.trim()
  if (!normalizedId) return false
  return dispatchAccountTestTasks([normalizedId])
}
