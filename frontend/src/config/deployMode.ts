export type FrontendDeployMode = 'node' | 'go'

/**
 * 解析前端后端连接模式。
 *
 * 构建期由 vite `define` 注入 `__JUHE_AI_DEPLOY_MODE__`（来自
 * `VITE_JUHE_AI_DEPLOY_MODE`）；dev 下的 tsx 回归脚本或未注入场景回退到
 * `import.meta.env`，再回退到默认 `node`，保证默认行为与历史版本一致。
 */
function resolveFrontendDeployMode(): FrontendDeployMode {
  const defineTimeMode = typeof __JUHE_AI_DEPLOY_MODE__ === 'string' ? __JUHE_AI_DEPLOY_MODE__ : ''
  const envSource = import.meta.env as { VITE_JUHE_AI_DEPLOY_MODE?: string } | undefined
  const rawMode = (defineTimeMode || envSource?.VITE_JUHE_AI_DEPLOY_MODE || 'node').trim().toLowerCase()
  return rawMode === 'go' ? 'go' : 'node'
}

/** 当前构建固定的后端连接模式：`node`（默认）或 `go`。 */
export const frontendDeployMode: FrontendDeployMode = resolveFrontendDeployMode()

/** 是否指向 backend-go gateway 主入口（`VITE_JUHE_AI_DEPLOY_MODE=go` 构建）。 */
export const isGoBackendMode = frontendDeployMode === 'go'
