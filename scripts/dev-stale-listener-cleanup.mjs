import { spawnSync } from 'node:child_process'

// dev 启动前残留清理：上一 dev 会话被强杀（关闭终端窗口、taskkill 父进程、
// 崩溃）时，juhe-ai-gateway/juhe-ai-jobs 进程可能残留并占住本次启动所需端口
// （主监听、gateway/jobs health、J3b alias），新会话 bind 失败 fail-fast。
// 这里对每个所需端口找 LISTENING 进程，仅当映像名属于 Go 三项目服务时强制
// 终止；端口被其他程序占用时不动，保留后续启动的自然报错路径，避免误杀
// 用户自己运行的其他进程（隔离测试实例按约定使用新端口，不会进入本清单）。
const goServiceImageNames = new Set(['juhe-ai-gateway', 'juhe-ai-jobs'])

const goDevListenerEnvDefaults = [
  ['JUHE_AI_PORT', '3000'],
  ['JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS', '127.0.0.1:3306'],
  ['JUHE_AI_J3B_MANAGEMENT_LISTEN_ADDRESS', '127.0.0.1:3307'],
  ['JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS', '127.0.0.1:3305']
]

// env 是 dev 启动器解析后的子进程环境（backend .env overlay 进程环境后的
// 结果）；output 注入 { log, error } 便于测试替换，默认 console。
export function clearStaleGoDevListeners(env, output = console) {
  const ports = new Set()
  for (const [envName, fallback] of goDevListenerEnvDefaults) {
    const port = portFromAddress(firstConfiguredValue(env?.[envName], fallback))
    if (port !== undefined) ports.add(port)
  }
  for (const port of ports) {
    for (const pid of findListeningPids(port)) {
      const imageName = processImageName(pid)
      if (!imageName) continue
      if (!goServiceImageNames.has(imageName.toLowerCase().replace(/\.exe$/, ''))) continue
      if (killProcessTree(pid)) {
        output.log(`[dev] 已强制清理残留的 ${imageName}（PID ${pid}，占用了本次启动所需的端口 ${port}）`)
      } else {
        output.error(`[dev] 清理残留的 ${imageName}（PID ${pid}，端口 ${port}）失败；若启动仍失败，请手动结束该进程后重试。`)
      }
    }
  }
}

function firstConfiguredValue(...values) {
  for (const value of values) {
    const trimmed = value?.trim()
    if (trimmed) return trimmed
  }
  return undefined
}

// portFromAddress 取地址末段端口，兼容 "127.0.0.1:3305"、"[::]:3305"、
// ":3305" 与纯端口 "3000" 四种写法。
function portFromAddress(value) {
  const match = /(\d+)\s*$/.exec(value?.trim() ?? '')
  return match ? Number(match[1]) : undefined
}

function findListeningPids(port) {
  return process.platform === 'win32'
    ? findListeningPidsWindows(port)
    : findListeningPidsPosix(port)
}

function findListeningPidsWindows(port) {
  const result = spawnSync('netstat', ['-ano'], { encoding: 'utf8' })
  const pids = new Set()
  for (const line of result.stdout?.split(/\r?\n/) ?? []) {
    if (!line.includes('LISTENING')) continue
    const columns = line.trim().split(/\s+/)
    if (columns.length < 4) continue
    const pid = Number(columns[columns.length - 1])
    if (portFromAddress(columns[1]) !== port || !Number.isInteger(pid) || pid <= 0) continue
    pids.add(pid)
  }
  return [...pids]
}

// lsof 缺失或不可用时返回空列表：预清理是尽力而为，不能阻塞正常启动。
function findListeningPidsPosix(port) {
  const result = spawnSync('lsof', ['-t', '-i', `tcp:${port}`, '-s', 'TCP:LISTEN'], { encoding: 'utf8' })
  if (result.error || !result.stdout) return []
  return result.stdout
    .split(/\s+/)
    .map(Number)
    .filter((pid) => Number.isInteger(pid) && pid > 0)
}

function processImageName(pid) {
  if (process.platform === 'win32') {
    const result = spawnSync('tasklist', ['/fi', `PID eq ${pid}`, '/fo', 'CSV', '/nh'], { encoding: 'utf8' })
    // PID 不存在时 tasklist 输出本地化提示行且非 CSV，只认以引号开头的行。
    const row = result.stdout?.split(/\r?\n/).find((line) => line.startsWith('"'))
    return /^"([^"]*)"/.exec(row ?? '')?.[1]
  }
  const result = spawnSync('ps', ['-p', String(pid), '-o', 'comm='], { encoding: 'utf8' })
  const comm = result.stdout?.trim()
  return comm ? comm.split('/').pop() : undefined
}

function killProcessTree(pid) {
  if (process.platform === 'win32') {
    return spawnSync('taskkill', ['/pid', String(pid), '/t', '/f'], { stdio: 'ignore' }).status === 0
  }
  try {
    process.kill(pid, 'SIGKILL')
    return true
  } catch {
    return false
  }
}
