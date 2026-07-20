import { execFileSync } from 'node:child_process';
import { fileURLToPath, URL } from 'node:url';
import vue from '@vitejs/plugin-vue';
import { defineConfig, loadEnv } from 'vite';
import Components from 'unplugin-vue-components/vite';
import { AntDesignVueResolver } from 'unplugin-vue-components/resolvers';
var repositoryRoot = fileURLToPath(new URL('..', import.meta.url));
var frontendBuildIdPattern = /^[0-9a-f]{40}$/;
function normalizeBuildConfigId(value) {
    var normalized = value.trim().toLowerCase();
    return frontendBuildIdPattern.test(normalized) ? normalized : undefined;
}
function resolveFrontendBuildId(explicitBuildId) {
    if (explicitBuildId === null || explicitBuildId === void 0 ? void 0 : explicitBuildId.trim()) {
        var normalizedBuildId_1 = normalizeBuildConfigId(explicitBuildId);
        if (!normalizedBuildId_1)
            throw new Error('VITE_JUHE_AI_BUILD_ID 必须是完整的 40 位 Git commit');
        return normalizedBuildId_1;
    }
    var gitBuildId = execFileSync('git', ['rev-parse', 'HEAD'], {
        cwd: repositoryRoot,
        encoding: 'utf8'
    });
    var normalizedBuildId = normalizeBuildConfigId(gitBuildId);
    if (!normalizedBuildId)
        throw new Error('无法从 Git HEAD 解析完整的前端 Build ID');
    return normalizedBuildId;
}
function frontendBuildInfoPlugin(buildId) {
    return {
        name: 'juhe-ai-frontend-build-info',
        generateBundle: function () {
            this.emitFile({
                type: 'asset',
                fileName: 'build-info.json',
                source: "".concat(JSON.stringify({ buildId: buildId }), "\n")
            });
        }
    };
}
export default defineConfig(function (_a) {
    var mode = _a.mode;
    var env = loadEnv(mode, fileURLToPath(new URL('.', import.meta.url)), '');
    var backendTarget = env.VITE_JUHE_AI_BACKEND_TARGET || 'http://127.0.0.1:3000';
    var buildId = resolveFrontendBuildId(env.VITE_JUHE_AI_BUILD_ID);
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
                    manualChunks: function (id) {
                        if (id.includes('node_modules/@ant-design/icons-vue')) {
                            return 'ant-design-icons';
                        }
                        if (id.includes('node_modules/@codemirror') || id.includes('node_modules/@lezer') || id.includes('node_modules/style-mod') || id.includes('node_modules/crelt') || id.includes('node_modules/w3c-keyname')) {
                            return 'codemirror';
                        }
                        if (id.includes('node_modules/zrender')) {
                            return 'zrender';
                        }
                        if (id.includes('node_modules/echarts')) {
                            return 'echarts';
                        }
                        if (id.includes('node_modules/vue-router')) {
                            return 'vue-router';
                        }
                        if (id.includes('node_modules/@vue/') || id.includes('node_modules/vue/')) {
                            return 'vue';
                        }
                        if (id.includes('node_modules/dayjs')) {
                            return 'dayjs';
                        }
                        if (id.includes('node_modules/axios')) {
                            return 'axios';
                        }
                        return undefined;
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
    };
});
