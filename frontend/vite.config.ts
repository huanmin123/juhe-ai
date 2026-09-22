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

// 帮助页是 public/help 下的纯静态目录，dev 下需要显式目录索引：
// 否则 /__aisys__/help/<面>/ 会绕过 public 直服、落入 SPA fallback，
// 返回主应用 index.html。语义对齐 gateway helpweb 的 express.static。
function helpPageDirectoryIndexPlugin(): Plugin {
  return {
    name: 'juhe-ai-help-page-directory-index',
    configureServer(server) {
      server.middlewares.use((req, _res, next) => {
        const raw = req.url || ''
        const queryIndex = raw.indexOf('?')
        const pathname = queryIndex === -1 ? raw : raw.slice(0, queryIndex)
        const search = queryIndex === -1 ? '' : raw.slice(queryIndex)
        if (pathname === '/__aisys__/help') {
          req.url = '/__aisys__/help/' + search
        } else if (pathname.startsWith('/__aisys__/help/') && pathname.endsWith('/')) {
          req.url = pathname + 'index.html' + search
        }
        next()
      })
    }
  }
}

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, fileURLToPath(new URL('.', import.meta.url)), '')
  const backendTarget = env.VITE_JUHE_AI_BACKEND_TARGET || 'http://127.0.0.1:3000'
  const buildId = resolveFrontendBuildId(env.VITE_JUHE_AI_BUILD_ID)
  // 可选：J3b 模型检测由独立 Go J3b Gateway 管理 listener 提供
  // （JUHE_AI_J3B_MANAGEMENT_LISTEN_ADDRESS，缺省 127.0.0.1:3307）；设置该变量后，
  // dev 代理会把 model-checks / my-model-checks 前缀转发到该入口，供本地联调
  // J3b UI。未设置时（默认）不产生任何代理条目，行为与历史版本一致。
  const j3bBackendTarget = env.VITE_JUHE_AI_J3B_BACKEND_TARGET?.trim() || ''

  const devProxy: Record<string, string> = {}
  if (j3bBackendTarget) {
    devProxy['^/__aisys__/api/(my-)?model-checks(/|$)'] = j3bBackendTarget
  }
  // 帮助页（/__aisys__/help/**）不走 gateway 代理：dev 下由 Vite 从 public/help
  // 按 base 前缀直服，改完即生效；转发 gateway 时若未配置
  // JUHE_AI_FRONTEND_DIST_PATH，help 面不挂载，会得到 404（资源不存在）。
  devProxy['^/__aisys__/api(/|$)'] = backendTarget
  devProxy['/v1'] = backendTarget

  console.log(`[juhe-ai-frontend] dev 代理指向 Go gateway 主入口 ${backendTarget}`)
  if (j3bBackendTarget) {
    console.log(`[juhe-ai-frontend] J3b 模型检测 dev 代理指向 ${j3bBackendTarget}`)
  }

  return {
    base: '/__aisys__/',
    define: {
      __JUHE_AI_FRONTEND_BUILD_ID__: JSON.stringify(buildId)
    },
    plugins: [
      vue(),
      helpPageDirectoryIndexPlugin(),
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
