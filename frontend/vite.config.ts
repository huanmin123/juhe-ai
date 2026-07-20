import { execFileSync } from 'node:child_process'
import { fileURLToPath, URL } from 'node:url'
import vue from '@vitejs/plugin-vue'
import { defineConfig, loadEnv, type Plugin } from 'vite'
import Components from 'unplugin-vue-components/vite'
import { AntDesignVueResolver } from 'unplugin-vue-components/resolvers'

const repositoryRoot = fileURLToPath(new URL('..', import.meta.url))
const frontendBuildIdPattern = /^[0-9a-f]{40}$/

function normalizeBuildConfigId(value: string): string | undefined {
  const normalized = value.trim().toLowerCase()
  return frontendBuildIdPattern.test(normalized) ? normalized : undefined
}

function resolveFrontendBuildId(explicitBuildId: string | undefined): string {
  if (explicitBuildId?.trim()) {
    const normalizedBuildId = normalizeBuildConfigId(explicitBuildId)
    if (!normalizedBuildId) throw new Error('VITE_JUHE_AI_BUILD_ID 必须是完整的 40 位 Git commit')
    return normalizedBuildId
  }

  const gitBuildId = execFileSync('git', ['rev-parse', 'HEAD'], {
    cwd: repositoryRoot,
    encoding: 'utf8'
  })
  const normalizedBuildId = normalizeBuildConfigId(gitBuildId)
  if (!normalizedBuildId) throw new Error('无法从 Git HEAD 解析完整的前端 Build ID')
  return normalizedBuildId
}

function frontendBuildInfoPlugin(buildId: string): Plugin {
  return {
    name: 'juhe-ai-frontend-build-info',
    generateBundle() {
      this.emitFile({
        type: 'asset',
        fileName: 'build-info.json',
        source: `${JSON.stringify({ buildId })}\n`
      })
    }
  }
}

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, fileURLToPath(new URL('.', import.meta.url)), '')
  const backendTarget = env.VITE_JUHE_AI_BACKEND_TARGET || 'http://127.0.0.1:3000'
  const buildId = resolveFrontendBuildId(env.VITE_JUHE_AI_BUILD_ID)

  return {
    base: '/__aisys__/',
    define: {
      __JUHE_AI_FRONTEND_BUILD_ID__: JSON.stringify(buildId)
    },
    plugins: [
      vue(),
      frontendBuildInfoPlugin(buildId),
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
            if (id.includes('node_modules/@codemirror') || id.includes('node_modules/@lezer') || id.includes('node_modules/style-mod') || id.includes('node_modules/crelt') || id.includes('node_modules/w3c-keyname')) {
              return 'codemirror'
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
        '^/__aisys__/help(/|$)': backendTarget,
        '^/__aisys__/api(/|$)': backendTarget,
        '/v1': backendTarget
      }
    }
  }
})
