import { normalizeFrontendBuildId } from '@/shared/frontendBuildId'

export type FrontendBuildStatus = 'changed' | 'same' | 'unknown'

export async function classifyFrontendBuild(
  currentBuildId: unknown,
  loadRemoteBuildId: () => Promise<unknown>
): Promise<FrontendBuildStatus> {
  const normalizedCurrentBuildId = normalizeFrontendBuildId(currentBuildId)
  if (!normalizedCurrentBuildId) return 'unknown'

  try {
    const remoteBuildId = normalizeFrontendBuildId(await loadRemoteBuildId())
    if (!remoteBuildId) return 'unknown'
    return remoteBuildId === normalizedCurrentBuildId ? 'same' : 'changed'
  } catch {
    return 'unknown'
  }
}
