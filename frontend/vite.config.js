import { fileURLToPath, URL } from 'node:url';
import vue from '@vitejs/plugin-vue';
import { defineConfig, loadEnv } from 'vite';
import Components from 'unplugin-vue-components/vite';
import { AntDesignVueResolver } from 'unplugin-vue-components/resolvers';
export default defineConfig(function (_a) {
    var mode = _a.mode;
    var env = loadEnv(mode, fileURLToPath(new URL('.', import.meta.url)), '');
    var backendTarget = env.VITE_JUHE_AI_BACKEND_TARGET || 'http://127.0.0.1:3000';
    return {
        base: '/__aisys__/',
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
