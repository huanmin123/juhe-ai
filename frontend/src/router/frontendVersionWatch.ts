import type { Router } from 'vue-router'

import { message } from '@/lib/antd'
import {
  classifyFrontendBuild,
  loadRemoteFrontendBuildId
} from './frontendBuildInfo'

// 主动版本监视：不再等懒加载 chunk 失败才提示"系统已更新"。每 5 分钟（以及
// 标签页重新可见时）低频拉取 build-info.json（no-cache，失败静默），发现新版
// 本只做轻提示；真正刷新延迟到下一次路由切换（整页加载到导航目标），绝不打断
// 用户进行中的操作。
const frontendVersionPollIntervalMs = 5 * 60 * 1000

let watchStarted = false
let newerVersionPending = false

export function hasPendingNewerVersion(): boolean {
  return newerVersionPending
}

export function startFrontendVersionWatch(router: Router): void {
  if (watchStarted) return
  watchStarted = true

  const checkForNewerVersion = async (): Promise<void> => {
    try {
      const status = await classifyFrontendBuild(
        __JUHE_AI_FRONTEND_BUILD_ID__,
        () => loadRemoteFrontendBuildId()
      )
      if (status !== 'changed' || newerVersionPending) return
      newerVersionPending = true
      message.info({
        content: '系统已更新，切换页面时将自动加载新版本',
        duration: 8
      })
    } catch {
      // 版本探测失败不影响使用，静默等待下一轮。
    }
  }

  router.beforeEach((to) => {
    if (!newerVersionPending) return true
    newerVersionPending = false
    // 整页加载到导航目标：浏览器重新拉取 index.html（no-cache）拿到新版本。
    window.location.assign(router.resolve(to.fullPath).href)
    return false
  })

  window.setInterval(() => {
    if (document.visibilityState === 'visible') void checkForNewerVersion()
  }, frontendVersionPollIntervalMs)
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible') void checkForNewerVersion()
  })
}
