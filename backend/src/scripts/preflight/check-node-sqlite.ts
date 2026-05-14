import { spawnSync } from 'node:child_process'

const nodeSqliteSpecifier = 'node:sqlite'
const recommendedNodeVersion = 'Node.js 22.13.0+（22.x）或 23.4.0+，推荐使用最新 LTS'
const verifyCommand = 'node --input-type=module -e "import \'node:sqlite\'; console.log(\'node:sqlite ok\')"'

function formatCheckError(checkResult: ReturnType<typeof spawnSync>): string {
  if (checkResult.error) {
    const code = 'code' in checkResult.error ? String((checkResult.error as NodeJS.ErrnoException).code) : undefined
    return code
      ? `${checkResult.error.name} [${code}]: ${checkResult.error.message}`
      : `${checkResult.error.name}: ${checkResult.error.message}`
  }

  const stderr = String(checkResult.stderr ?? '').trim()
  const stdout = String(checkResult.stdout ?? '').trim()
  return stderr || stdout || `node 进程退出码 ${String(checkResult.status ?? 'unknown')}`
}

const checkResult = spawnSync(process.execPath, [
  '--no-warnings',
  '--input-type=module',
  '-e',
  `await import('${nodeSqliteSpecifier}')`
], {
  encoding: 'utf8'
})

if (checkResult.status !== 0 || checkResult.error) {
  console.error([
    '[juhe-ai] 当前 Node.js 不支持内置模块 node:sqlite，后端无法启动。',
    `当前版本：${process.version}`,
    `Node 路径：${process.execPath}`,
    `建议版本：${recommendedNodeVersion}`,
    '处理方式：升级或者重装 Node.js 后重新安装依赖并启动项目。',
    `验证命令：${verifyCommand}`,
    `原始错误：${formatCheckError(checkResult)}`
  ].join('\n'))
  process.exit(1)
}
