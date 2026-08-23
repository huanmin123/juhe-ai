import { dispatchAccountTestTasks } from '../accounts/account-test-task-queue.service.js'
import { requestBackgroundWorkerDbService } from '../background/background-ipc.js'

export async function dispatchAccountTestTask(taskId: string): Promise<boolean> {
  const normalizedId = taskId.trim()
  if (!normalizedId) return false
  const accepted = await dispatchAccountTestTasks([normalizedId])
  if (!accepted) {
    await requestBackgroundWorkerDbService({
      type: 'fail_account_test_task',
      taskId: normalizedId,
      message: '后台 worker 暂不可用，账号测试任务未能投递'
    })
  }
  return accepted
}
