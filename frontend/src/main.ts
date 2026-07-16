import 'ant-design-vue/dist/reset.css'
import './styles/global.css'

import { createApp, watch } from 'vue'

import App from './App.vue'
import { submitLockDirective } from './directives/submitLock'
import { router } from './router'
import { authState } from './composables/useAuth'
import { chatGenerationRuntime } from './views/chat/chatGenerationRuntime'

const app = createApp(App)

watch(
  () => authState.currentUser.value?.id,
  (systemAccountId) => chatGenerationRuntime.activateAccount(systemAccountId),
  { immediate: true }
)

app.directive('submit-lock', submitLockDirective)
app.use(router).mount('#app')
