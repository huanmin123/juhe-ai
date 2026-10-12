import type { SchedulingPreferenceOptionSummary } from '@/types/domain'
import { rememberSelectOption, selectLabelForValue } from '@/shared/selectLabelCache'

const schedulingPreferenceCacheKey = 'scheduling-preferences'

export function rememberSchedulingPreferenceLabels(options: SchedulingPreferenceOptionSummary[], cacheKey = schedulingPreferenceCacheKey): void {
  for (const option of options) {
    rememberSelectOption(cacheKey, option.id, option.name)
  }
}

export function rememberSchedulingPreferenceSelectionLabel(
  id: string | null | undefined,
  name: string | undefined,
  cacheKey = schedulingPreferenceCacheKey
): void {
  rememberSelectOption(cacheKey, id ?? undefined, name)
}

export function schedulingPreferenceLabelForId(id: string | undefined, cacheKey = schedulingPreferenceCacheKey): string | undefined {
  return selectLabelForValue(cacheKey, id)
}
