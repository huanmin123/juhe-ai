import { countChatTextTokens } from '../../modules/chat/chat-token-count.js'

export const chatLongSessionControlledSeedMaxTokens = 180_000
export const chatLongSessionSemanticSeedMaxTurns = 50

export function buildChatLongSessionSemanticSeedPlan(
  turns: number,
  requestedTargetTokens: number,
  assertActive?: (label: string) => void
): { artifacts: string[]; totalBytes: number; totalTokens: number } {
  if (!Number.isSafeInteger(turns) || turns < 0 || turns > chatLongSessionSemanticSeedMaxTurns) {
    throw new Error('chat_long_session_semantic_seed_turns_invalid')
  }
  if (!Number.isSafeInteger(requestedTargetTokens) || requestedTargetTokens < 0) {
    throw new Error('chat_long_session_semantic_seed_target_tokens_invalid')
  }
  if (turns === 0 && requestedTargetTokens !== 0) {
    throw new Error('chat_long_session_semantic_seed_zero_turns_requires_zero_target')
  }
  const targetTokens = Math.min(Math.max(0, requestedTargetTokens), chatLongSessionControlledSeedMaxTokens)
  const perArtifactTarget = turns > 0 ? Math.ceil(targetTokens / turns) : 0
  const perArtifactByteLimit = turns > 0 ? Math.min(191 * 1024, Math.floor(3_800_000 / turns)) : 0
  const artifacts = Array.from({ length: turns }, (_, index) => seededArtifact(index + 1, 1))
  const artifactTokens = artifacts.map((artifact) => countChatTextTokens(artifact))
  let totalTokens = artifactTokens.reduce((total, tokens) => total + tokens, 0)
  if (totalTokens > chatLongSessionControlledSeedMaxTokens) {
    throw new Error('chat_long_session_semantic_seed_baseline_exceeds_cap')
  }

  for (let index = 0; index < turns; index += 1) {
    const version = index + 1
    assertActive?.(`semantic_seed_${version}`)
    let modules = 1
    while (artifactTokens[index]! < perArtifactTarget) {
      assertActive?.(`semantic_seed_${version}_${modules}`)
      const candidate = seededArtifact(version, modules + 8)
      if (Buffer.byteLength(candidate, 'utf8') >= perArtifactByteLimit) break
      const candidateTokens = countChatTextTokens(candidate)
      const nextTotalTokens = totalTokens - artifactTokens[index]! + candidateTokens
      if (nextTotalTokens > chatLongSessionControlledSeedMaxTokens) break
      modules += 8
      artifacts[index] = candidate
      totalTokens = nextTotalTokens
      artifactTokens[index] = candidateTokens
    }
  }

  return {
    artifacts,
    totalBytes: artifacts.reduce((total, artifact) => total + Buffer.byteLength(artifact, 'utf8'), 0),
    totalTokens
  }
}

function seededArtifact(version: number, moduleCount: number): string {
  const requirements = Array.from({ length: version }, (_, index) => `REQ-C${String(index + 1).padStart(2, '0')}`).join(' ')
  const styles = Array.from({ length: moduleCount }, (_, index) => `.project-module-${version}-${index}{display:grid;grid-template-columns:minmax(0,1fr) auto;gap:var(--space-${index % 8});padding:${(index % 12) + 4}px;border:1px solid var(--line);container-type:inline-size}`).join('\n')
  const modules = Array.from({ length: moduleCount }, (_, index) => `<section class="project-module-${version}-${index}" aria-labelledby="module-${version}-${index}"><h2 id="module-${version}-${index}">运营模块 ${index + 1}</h2><p>版本 ${version} 的连续项目模块，保留筛选、状态、负责人、更新时间和响应式布局决策。</p><button type="button" aria-label="打开模块 ${index + 1}">查看</button></section>`).join('\n')
  return `<!doctype html><html lang="zh-CN" data-requirements="${requirements}"><head><meta charset="utf-8"><style>:root{--surface:#fff;--ink:#17202a;--line:#d7dce2;${Array.from({ length: 8 }, (_, index) => `--space-${index}:${index + 4}px`).join(';')}}body{font-family:Arial,sans-serif}.app-shell{display:grid;grid-template-columns:18rem minmax(0,1fr)}${styles}@media(max-width:720px){.app-shell{grid-template-columns:1fr}}</style></head><body><header><nav aria-label="主导航">Aurora Dashboard</nav></header><div class="app-shell"><aside>项目导航</aside><main id="aurora-dashboard"><h1>PROJECT-AURORA-FOUNDATION 版本 ${version}</h1>${modules}</main></div><footer>连续项目版本 ${version}</footer></body></html>`
}
