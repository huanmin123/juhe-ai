import 'ant-design-vue/dist/reset.css'
import './styles/global.css'

import { createApp, watch } from 'vue'

import App from './App.vue'
import { submitLockDirective } from './directives/submitLock'
import { router } from './router'
import { authState } from './composables/useAuth'
import { prewarmSelfUserReferenceData, syncUserReferenceDataAuthState } from './composables/useUserReferenceData'
import { chatGenerationRuntime } from './views/chat/chatGenerationRuntime'
import { activateChatConversationSyncAccount } from './views/chat/chatConversationSync'

const app = createApp(App)
let lastPrewarmedAuthSessionKey: string | undefined
let lastObservedUser: typeof authState.currentUser.value
let authStateObserved = false

watch(
  () => [authState.currentUser.value, authState.revision.value] as const,
  ([currentUser, authRevision]) => {
    const currentUserChanged = !authStateObserved || currentUser !== lastObservedUser
    authStateObserved = true
    lastObservedUser = currentUser
    syncUserReferenceDataAuthState(currentUser?.id, authRevision)
    if (!currentUser) {
      lastPrewarmedAuthSessionKey = undefined
      return
    }
    // Auth mutations advance revision before their request. Clear immediately, but only
    // prewarm after login/me or a successful mutation publishes a new user snapshot.
    if (!currentUserChanged) return
    const authSessionKey = `${currentUser.id}:${authRevision}`
    if (authSessionKey === lastPrewarmedAuthSessionKey) return
    lastPrewarmedAuthSessionKey = authSessionKey
    void prewarmSelfUserReferenceData().then((value) => {
      if (!value && lastPrewarmedAuthSessionKey === authSessionKey) {
        lastPrewarmedAuthSessionKey = undefined
      }
    })
  },
  { immediate: true, flush: 'sync' }
)

watch(
  () => authState.currentUser.value?.id,
  (systemAccountId) => {
    activateChatConversationSyncAccount(systemAccountId)
    chatGenerationRuntime.activateAccount(systemAccountId)
  },
  { immediate: true }
)

app.directive('submit-lock', submitLockDirective)
app.use(router).mount('#app')
