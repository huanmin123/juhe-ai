import { message } from '@/lib/antd'
import { ref } from 'vue'

import { api } from '@/api/client'
import type { RuntimeLogFacets } from '@/types/domain'

export function useRuntimeLogFacetsState() {
  const facets = ref<RuntimeLogFacets>()
  let facetsRequestSeq = 0

  async function loadRuntimeLogFacets(force = false): Promise<void> {
    if (facets.value && !force) return
    const requestSeq = ++facetsRequestSeq
    try {
      const nextFacets = await api.runtimeLogs.facets()
      if (requestSeq !== facetsRequestSeq) return
      facets.value = nextFacets
    } catch (error) {
      if (requestSeq !== facetsRequestSeq) return
      console.error(error)
      message.error('加载运行日志筛选项失败')
    }
  }

  function cancelRuntimeLogFacetsRequest(): void {
    facetsRequestSeq += 1
  }

  return {
    cancelRuntimeLogFacetsRequest,
    facets,
    loadRuntimeLogFacets
  }
}
