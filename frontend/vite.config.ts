import { fileURLToPath, URL } from 'node:url'
import vue from '@vitejs/plugin-vue'
import { defineConfig, loadEnv } from 'vite'
import Components from 'unplugin-vue-components/vite'
import { AntDesignVueResolver } from 'unplugin-vue-components/resolvers'

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, fileURLToPath(new URL('.', import.meta.url)), '')
  const backendTarget = env.VITE_JUHE_AI_BACKEND_TARGET || 'http://127.0.0.1:3000'

  return {
    plugins: [
      vue(),
      Components({
        dts: false,
        resolvers: [
          AntDesignVueResolver({
            importStyle: false
          })
        ]
      })
    ],
    build: {
      rollupOptions: {
        output: {
          manualChunks(id) {
            if (id.includes('node_modules/@ant-design/icons-vue')) {
              return 'ant-design-icons'
            }
            if (id.includes('node_modules/zrender')) {
              return 'zrender'
            }
            if (id.includes('node_modules/echarts')) {
              return 'echarts'
            }
            if (id.includes('node_modules/vue-router')) {
              return 'vue-router'
            }
            if (id.includes('node_modules/@vue/') || id.includes('node_modules/vue/')) {
              return 'vue'
            }
            if (id.includes('node_modules/dayjs')) {
              return 'dayjs'
            }
            if (id.includes('node_modules/axios')) {
              return 'axios'
            }
            return undefined
          }
        }
      }
    },
    resolve: {
      alias: {
        '@': fileURLToPath(new URL('./src', import.meta.url))
      }
    },
    server: {
      port: 5173,
      proxy: {
        '^/api(/|$)': backendTarget,
        '/v1': backendTarget
      }
    }
  }
})
