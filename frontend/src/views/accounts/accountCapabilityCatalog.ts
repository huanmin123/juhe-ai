import { shallowRef } from 'vue'
import type {
  AccountCapabilityCatalog,
  AccountCapabilityCatalogProfile,
  AccountCapabilityCatalogScenario
} from '@/types/domain'

// 能力目录是 B1.3 的唯一读来源：AccountsView 打开时拉取一次，表单矩阵优先使用
// 后端目录；未加载（测试/离线）时才回退本地常量矩阵。目录只读，不写库。
const catalogState = shallowRef<AccountCapabilityCatalog | null>(null)

export function setAccountCapabilityCatalog(catalog: AccountCapabilityCatalog | null): void {
  catalogState.value = catalog
}

export function getAccountCapabilityCatalog(): AccountCapabilityCatalog | null {
  return catalogState.value
}

export function catalogScenario(domain: string): AccountCapabilityCatalogScenario | undefined {
  return catalogState.value?.scenarios.find((scenario) => scenario.scenarioType === domain)
}

export function catalogProfiles(input: {
  capabilityDomain: string
  providerCode?: string
  profileId?: string
}): AccountCapabilityCatalogProfile[] {
  const scenario = catalogScenario(input.capabilityDomain)
  if (!scenario) return []
  return scenario.providers
    .filter((provider) => !input.providerCode || provider.providerCode === input.providerCode)
    .flatMap((provider) => provider.profiles)
    .filter((profile) => !input.profileId || profile.profileId === input.profileId)
}
