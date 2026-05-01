import 'ant-design-vue/dist/reset.css'
import './styles/global.css'

import Antd from 'ant-design-vue'
import { createApp } from 'vue'

import App from './App.vue'
import { router } from './router'

createApp(App).use(router).use(Antd).mount('#app')

