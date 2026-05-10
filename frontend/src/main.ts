import 'ant-design-vue/dist/reset.css'
import './styles/global.css'

import { createApp } from 'vue'

import App from './App.vue'
import { submitLockDirective } from './directives/submitLock'
import { router } from './router'

const app = createApp(App)

app.directive('submit-lock', submitLockDirective)
app.use(router).mount('#app')
