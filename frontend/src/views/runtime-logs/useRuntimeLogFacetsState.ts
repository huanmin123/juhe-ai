import { message } from '@/lib/antd'
import { ref } from 'vue'

import { api } from '@/api/client'
import type { RuntimeLogFacets, RuntimeLogGrepRuntime } from '@/types/domain'

export function useRuntimeLogFacetsState() {
  const facets = ref<RuntimeLogFacets>()
  const grepRuntime = ref<RuntimeLogGrepRuntime>()
  let facetsRequestSeq = 0
  let grepOptionsRequestSeq = 0

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

  async function loadRuntimeLogGrepOptions(force = false): Promise<void> {
    if (grepRuntime.value && !force) return
    const requestSeq = ++grepOptionsRequestSeq
    try {
      const nextGrepRuntime = await api.runtimeLogs.grepOptions()
      if (requestSeq !== grepOptionsRequestSeq) return
      grepRuntime.value = nextGrepRuntime
    } catch (error) {
      if (requestSeq !== grepOptionsRequestSeq) return
      console.error(error)
      message.error('加载 grep 文件范围失败')
    }
  }

  function cancelRuntimeLogFacetsRequest(): void {
    facetsRequestSeq += 1
    grepOptionsRequestSeq += 1
  }

  return {
    cancelRuntimeLogFacetsRequest,
    facets,
    grepRuntime,
    loadRuntimeLogFacets,
    loadRuntimeLogGrepOptions
  }
}
