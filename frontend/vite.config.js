import { fileURLToPath, URL } from 'node:url';
import vue from '@vitejs/plugin-vue';
import { defineConfig, loadEnv } from 'vite';
export default defineConfig(function (_a) {
    var mode = _a.mode;
    var env = loadEnv(mode, fileURLToPath(new URL('.', import.meta.url)), '');
    var backendTarget = env.VITE_JUHE_AI_BACKEND_TARGET || 'http://127.0.0.1:3000';
    return {
        plugins: [vue()],
        resolve: {
            alias: {
                '@': fileURLToPath(new URL('./src', import.meta.url))
            }
        },
        server: {
            port: 5173,
            proxy: {
                '/api': backendTarget,
                '/v1': backendTarget
            }
        }
    };
});
