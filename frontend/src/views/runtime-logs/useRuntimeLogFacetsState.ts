import { message } from '@/lib/antd'
import { ref } from 'vue'

import { api } from '@/api/client'
import type { RuntimeLogFacets, RuntimeLogGrepRuntime, RuntimeLogRuntime } from '@/types/domain'

export function useRuntimeLogFacetsState() {
  const facets = ref<RuntimeLogFacets>()
  const grepRuntime = ref<RuntimeLogGrepRuntime>()
  const runtime = ref<RuntimeLogRuntime>()
  let facetsRequestSeq = 0
  let grepOptionsRequestSeq = 0
  let runtimeRequestSeq = 0

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

  async function loadRuntimeLogRuntime(force = false): Promise<void> {
    if (runtime.value && !force) return
    const requestSeq = ++runtimeRequestSeq
    try {
      const nextRuntime = await api.runtimeLogs.runtime()
      if (requestSeq !== runtimeRequestSeq) return
      runtime.value = nextRuntime
    } catch (error) {
      if (requestSeq !== runtimeRequestSeq) return
      console.error(error)
      runtime.value = unavailableRuntimeLogRuntime()
    }
  }

  function cancelRuntimeLogFacetsRequest(): void {
    facetsRequestSeq += 1
    grepOptionsRequestSeq += 1
    runtimeRequestSeq += 1
  }

  return {
    cancelRuntimeLogFacetsRequest,
    facets,
    grepRuntime,
    loadRuntimeLogFacets,
    loadRuntimeLogGrepOptions,
    loadRuntimeLogRuntime,
    runtime
  }
}

function unavailableRuntimeLogRuntime(): RuntimeLogRuntime {
  return {
    indexEnabled: true,
    runtimeAvailable: false,
    ingestWorkerAvailable: false,
    runtimeLogIndexQueueAvailable: false,
    dbService: {
      statusAvailable: false,
      stateAvailable: false
    },
    gatewayAccountSideEffectsAvailable: false
  }
}
