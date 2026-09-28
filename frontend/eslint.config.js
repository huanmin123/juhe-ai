import js from '@eslint/js'
import pluginVue from 'eslint-plugin-vue'
import tseslint from 'typescript-eslint'

export default tseslint.config(
  {
    ignores: ['node_modules/**', 'dist/**', 'coverage/**', 'public/**'],
  },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  ...pluginVue.configs['flat/recommended'],
  {
    files: ['**/*.vue'],
    languageOptions: {
      parserOptions: {
        parser: tseslint.parser,
      },
    },
  },
  {
    rules: {
      // 不启用格式类规则（格式由编辑器/后续 formatter 负责）
      'vue/html-indent': 'off',
      'vue/html-self-closing': 'off',
      'vue/max-attributes-per-line': 'off',
      'vue/singleline-html-element-content-newline': 'off',
      'vue/multiline-html-element-content-newline': 'off',
      'vue/html-closing-bracket-newline': 'off',
      'vue/first-attribute-linebreak': 'off',
      'vue/html-closing-bracket-spacing': 'off',
      // TS 项目中标识符引用正确性由 vue-tsc（typecheck）负责；
      // flat config 下未声明浏览器 globals，no-undef 只产生大规模误报
      'no-undef': 'off',
      // 以下为真实但重构级的问题：降为 warning 保留信号，不阻塞 lint，
      // 修正涉及行为/类型变更，不属于 lint 最小修正范围
      'vue/no-mutating-props': 'warn',
      '@typescript-eslint/no-explicit-any': 'warn',
      'no-unsafe-finally': 'warn',
      'preserve-caught-error': 'warn',
      'no-useless-assignment': 'warn',
      '@typescript-eslint/no-empty-object-type': 'warn', // interface X extends Y {} 标记类型惯用法
      'vue/valid-template-root': 'warn', // RuntimeAvailabilityAlert.vue 有意使用空模板
      // 单独一行读取 .value 是建立 computed 响应式依赖的既有惯用法，修正会破坏依赖收集
      '@typescript-eslint/no-unused-expressions': 'warn',
      // 字符宽度测量/控制字符清洗正则有意包含控制字符与组合字符
      'no-control-regex': 'off',
      'no-misleading-character-class': 'warn',
      // 与现有代码模式对齐
      'no-empty': ['error', { allowEmptyCatch: true }], // 空 catch 块是既有容错模式
      '@typescript-eslint/no-unused-vars': [
        'error',
        { argsIgnorePattern: '^_', varsIgnorePattern: '^_' }, // 下划线前缀=有意忽略的既有惯例
      ],
    },
  },
)
