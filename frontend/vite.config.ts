import { execFileSync } from 'node:child_process'
import { fileURLToPath, URL } from 'node:url'
import vue from '@vitejs/plugin-vue'
import { defineConfig, loadEnv, type Plugin } from 'vite'
import Components from 'unplugin-vue-components/vite'
import { AntDesignVueResolver } from 'unplugin-vue-components/resolvers'

const repositoryRoot = fileURLToPath(new URL('..', import.meta.url))
const frontendBuildIdPattern = /^[0-9a-f]{40}$/
const frontendDeployModes = ['node', 'go'] as const

type FrontendDeployMode = (typeof frontendDeployModes)[number]

// 前端后端连接模式：node（默认，与历史行为一致）或 go（dev 代理 / 构建声明指向
// backend-go gateway 主入口）。两种模式下 gateway 都监听同一默认端口约定
// （JUHE_AI_HOST:JUHE_AI_PORT，缺省 127.0.0.1:3000），因此默认 target 不随模式变化。
function resolveFrontendDeployMode(value: string | undefined): FrontendDeployMode {
  const normalized = value?.trim().toLowerCase() || 'node'
  if (!(frontendDeployModes as readonly string[]).includes(normalized)) {
    throw new Error('VITE_JUHE_AI_DEPLOY_MODE 必须是 node 或 go')
  }
  return normalized as FrontendDeployMode
}

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
  const deployMode = resolveFrontendDeployMode(env.VITE_JUHE_AI_DEPLOY_MODE)
  // 可选：J3b 模型检测由独立 Go J3b Gateway 管理 listener 提供
  // （JUHE_AI_J3B_MANAGEMENT_LISTEN_ADDRESS，缺省 127.0.0.1:3307）；设置该变量后，
  // dev 代理会把 model-checks / my-model-checks 前缀转发到该入口，供本地联调
  // J3b UI。未设置时（默认）不产生任何代理条目，行为与历史版本一致。
  const j3bBackendTarget = env.VITE_JUHE_AI_J3B_BACKEND_TARGET?.trim() || ''

  const devProxy: Record<string, string> = {}
  if (j3bBackendTarget) {
    devProxy['^/__aisys__/api/(my-)?model-checks(/|$)'] = j3bBackendTarget
  }
  devProxy['^/__aisys__/help(/|$)'] = backendTarget
  devProxy['^/__aisys__/api(/|$)'] = backendTarget
  devProxy['/v1'] = backendTarget

  if (deployMode === 'go') {
    console.log(`[juhe-ai-frontend] VITE_JUHE_AI_DEPLOY_MODE=go：dev 代理指向 Go gateway 主入口 ${backendTarget}`)
    if (j3bBackendTarget) {
      console.log(`[juhe-ai-frontend] J3b 模型检测 dev 代理指向 ${j3bBackendTarget}`)
    }
  }

  return {
    base: '/__aisys__/',
    define: {
      __JUHE_AI_FRONTEND_BUILD_ID__: JSON.stringify(buildId),
      __JUHE_AI_DEPLOY_MODE__: JSON.stringify(deployMode)
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
      proxy: devProxy
    }
  }
})
