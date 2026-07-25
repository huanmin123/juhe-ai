import { spawn } from 'node:child_process'

const defaultRepeatCount = 3
const maximumRepeatCount = 10
const repeatCount = parseRepeatCount(process.env.JUHE_GATEWAY_CHAOS_REPEAT_COUNT)

for (let index = 1; index <= repeatCount; index += 1) {
  console.log(`[gateway-account-chaos] stability run ${index}/${repeatCount}`)
  await runAggregate(index)
}

console.log(`gateway account chaos stability regression passed: ${repeatCount} consecutive runs`)

function parseRepeatCount(raw: string | undefined): number {
  if (raw === undefined || raw.trim() === '') return defaultRepeatCount
  const parsed = Number(raw)
  if (!Number.isInteger(parsed) || parsed < 1 || parsed > maximumRepeatCount) {
    throw new Error(`JUHE_GATEWAY_CHAOS_REPEAT_COUNT 必须是 1-${maximumRepeatCount} 的整数`)
  }
  return parsed
}

async function runAggregate(index: number): Promise<void> {
  const executable = process.platform === 'win32' ? 'pnpm.cmd' : 'pnpm'
  await new Promise<void>((resolve, reject) => {
    const child = spawn(executable, ['run', 'test:gateway-account-chaos-mock-ai'], {
      cwd: process.cwd(),
      env: {
        ...process.env,
        CI: '1',
        JUHE_GATEWAY_CHAOS_REPEAT_INDEX: String(index)
      },
      stdio: 'inherit',
      shell: process.platform === 'win32'
    })
    child.once('error', reject)
    child.once('exit', (code, signal) => {
      if (code === 0) {
        resolve()
        return
      }
      reject(new Error(`gateway account chaos 第 ${index} 轮失败：exit=${String(code)} signal=${String(signal)}`))
    })
  })
}
