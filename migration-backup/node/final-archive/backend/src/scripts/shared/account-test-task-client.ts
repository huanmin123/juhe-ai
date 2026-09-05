export interface AccountTestTask<T = unknown> {
  id: string
  status: 'queued' | 'running' | 'success' | 'failed' | 'canceled'
  message?: string
  result?: T
}

interface ApiEnvelope<T> {
  data: T
  message?: string
}

interface SubmitAccountTestOptions {
  baseUrl: string
  path: string
  cookie?: string
  body?: Record<string, unknown>
  timeoutMs?: number
  pollIntervalMs?: number
}

export async function submitAccountTestAndWait<T>(options: SubmitAccountTestOptions): Promise<T> {
  const task = await postEnvelope<AccountTestTask<T>>(options.baseUrl, options.path, options.cookie, options.body ?? {})
  return waitForAccountTestResult<T>({
    ...options,
    task
  })
}

async function waitForAccountTestResult<T>(
  options: SubmitAccountTestOptions & { task: AccountTestTask<T> }
): Promise<T> {
  const timeoutMs = Math.max(1, Math.trunc(options.timeoutMs ?? 30_000))
  const pollIntervalMs = Math.max(50, Math.trunc(options.pollIntervalMs ?? 100))
  const taskPath = accountTestTaskPath(options.path, options.task.id)
  const startedAt = Date.now()
  let task = options.task

  while (Date.now() - startedAt < timeoutMs) {
    if (task.status === 'success' || task.status === 'failed') {
      if (task.result !== undefined) {
        return task.result
      }
      throw new Error(`账号测试任务 ${task.id} 已结束但没有结果：${task.message ?? task.status}`)
    }
    if (task.status === 'canceled') {
      throw new Error(`账号测试任务 ${task.id} 已取消：${task.message ?? '已停止测试'}`)
    }
    await sleep(pollIntervalMs)
    const tasks = await getEnvelope<Array<AccountTestTask<T>>>(options.baseUrl, taskPath, options.cookie)
    const latestTask = tasks.find((item) => item.id === task.id)
    if (!latestTask) {
      throw new Error(`账号测试任务 ${task.id} 不存在或当前作用域不可见`)
    }
    task = latestTask
  }

  throw new Error(`账号测试任务 ${task.id} 等待超时：${task.status}`)
}

function accountTestTaskPath(accountTestPath: string, taskId: string): string {
  const match = /^(.*\/(?:my-accounts|accounts))\/[^/?]+\/test(\?.*)?$/.exec(accountTestPath)
  if (!match) {
    throw new Error(`无法从账号测试路径推导任务查询路径：${accountTestPath}`)
  }
  const scopeQuery = match[2] ? `&${match[2].slice(1)}` : ''
  return `${match[1]}/test-tasks?ids=${encodeURIComponent(taskId)}${scopeQuery}`
}

async function getEnvelope<T>(baseUrl: string, path: string, cookie?: string): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, cookie ? { headers: { cookie } } : undefined)
  return parseEnvelope<T>(path, response)
}

async function postEnvelope<T>(baseUrl: string, path: string, cookie: string | undefined, body: Record<string, unknown>): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'POST',
    headers: {
      ...(cookie ? { cookie } : {}),
      'content-type': 'application/json'
    },
    body: JSON.stringify(body)
  })
  return parseEnvelope<T>(path, response)
}

async function parseEnvelope<T>(path: string, response: Response): Promise<T> {
  const text = await response.text()
  if (!response.ok) {
    throw new Error(`${path} HTTP ${response.status}: ${text}`)
  }
  return (JSON.parse(text) as ApiEnvelope<T>).data
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolvePromise) => setTimeout(resolvePromise, ms))
}
