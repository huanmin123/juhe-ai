import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vitest/config'

// 前端逻辑域单测配置：
// - 覆盖对象是纯逻辑代码（api/shared/composables/lib/utils/config/directives），
//   不做页面交互测试；src/scripts/**（tsx 回归脚本）与 views/components 不纳入。
// - environment 用 happy-dom：composables/directives 会触碰 DOM 接口。
export default defineConfig({
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url))
    }
  },
  test: {
    environment: 'happy-dom',
    include: ['src/**/*.test.ts'],
    exclude: ['src/scripts/**', 'node_modules/**', 'dist/**'],
    coverage: {
      provider: 'v8',
      reportsDirectory: 'coverage',
      reporter: ['text', 'text-summary', 'html', 'json-summary'],
      include: [
        'src/api/**',
        'src/shared/**',
        'src/composables/**',
        'src/lib/**',
        'src/utils/**',
        'src/config/**',
        'src/directives/**'
      ],
      exclude: ['src/**/*.test.ts', 'src/**/*.d.ts'],
      // 门槛取 2026-09 基线（stmts 98.81 / branch 95.31 / funcs 99.14）下方留缓冲，
      // 防覆盖率回退；抬门槛时先补测试再改数字。
      thresholds: {
        statements: 95,
        branches: 90,
        functions: 95,
        lines: 95
      }
    }
  }
})
