import 'ant-design-vue/dist/reset.css'
import './styles/global.css'

import { createApp } from 'vue'

import App from './App.vue'
import { router } from './router'

createApp(App).use(router).mount('#app')
