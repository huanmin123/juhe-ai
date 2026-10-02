import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  accountNameFromBaseUrl,
  accountNameRandomSuffix,
  defaultAccountNameFromBaseUrl,
  followBaseUrlAccountName
} from '../../views/accounts/accountNameSuggestion'

assertEqual(accountNameFromBaseUrl('https://api.openai.com/v1'), 'api.openai.com', '应从标准 Base URL 提取域名')
assertEqual(accountNameFromBaseUrl(' https://Gateway.Example.com:8443/openai/v1 '), 'gateway.example.com', '域名应忽略端口和路径并规范化')
assertEqual(accountNameFromBaseUrl('http://127.0.0.1:3000/v1'), '127.0.0.1', '本地 Base URL 应提取主机名')
assertEqual(accountNameFromBaseUrl('api.example.com/v1'), '', '缺少协议的地址不应生成账户名称')
assertEqual(accountNameFromBaseUrl('ftp://api.example.com'), '', '非 HTTP(S) 地址不应生成账户名称')
assertEqual(accountNameFromBaseUrl(''), '', '空 Base URL 不应生成账户名称')

const suffix = accountNameRandomSuffix()
assertEqual(suffix.length, 6, '随机后缀默认长度应为 6')
assertMatch(suffix, /^[a-z0-9]{6}$/, '随机后缀只应包含小写字母与数字')
assertEqual(accountNameRandomSuffix() === accountNameRandomSuffix(), false, '两次生成的随机后缀应几乎必然不同')
assertEqual(accountNameRandomSuffix(8).length, 8, '随机后缀应支持自定义长度')

const defaultName = defaultAccountNameFromBaseUrl('https://api.openai.com/v1')
assertMatch(defaultName, /^api\.openai\.com-[a-z0-9]{6}$/, '默认名应为 域名-6 位随机后缀')
assertEqual(defaultAccountNameFromBaseUrl(''), '', 'Base URL 无效时不应生成默认名')

// Base URL 变化跟随状态机
assertMatch(
  followBaseUrlAccountName('', 'https://api.openai.com/v1'),
  /^api\.openai\.com-[a-z0-9]{6}$/,
  '名称为空时应生成新的默认名'
)
assertEqual(
  followBaseUrlAccountName('api.openai.com-abc123', 'https://newapi.example.com/v1'),
  'newapi.example.com-abc123',
  '自动默认名跟随 Base URL 变化时应只换域名、保留后缀'
)
assertEqual(
  followBaseUrlAccountName('api.openai.com-abc123', 'https://api.openai.com/v1'),
  '',
  '域名未变化时不应改写名称'
)
assertEqual(
  followBaseUrlAccountName('我的主力账户', 'https://newapi.example.com/v1'),
  '',
  '用户手动命名（非默认名形态）时不得覆盖'
)
assertEqual(
  followBaseUrlAccountName('my-bot-abcdef', 'https://newapi.example.com/v1'),
  '',
  '无点分域名前缀的名称应视为手动命名，不得覆盖'
)
assertEqual(
  followBaseUrlAccountName('api.openai.com-abc123', 'not-a-url'),
  '',
  'Base URL 无效时不得改写名称'
)

// 源码契约：名称为空/自动默认才跟随 Base URL（该 guard 曾在 0c0da3651 被反转、BUG-0253 回退时漏网，
// 此处锁定方向与触发绑定，防止再次反转或丢失键入跟随）。默认名生成点必须在 AccountEditModal 的
// open watch 内——弹窗 force-render 下子组件 onMounted 只在页面加载时跑一次，会错过打开时机。
const srcRoot = dirname(dirname(dirname(fileURLToPath(import.meta.url))))
const modalSource = readFileSync(resolve(srcRoot, 'views/accounts/AccountEditModal.vue'), 'utf8')
assertIncludes(modalSource, 'defaultAccountNameFromBaseUrl(props.form.baseUrl)', '新建表单打开时必须在弹窗层生成默认名')
assertIncludes(modalSource, 'if (!props.editing && !props.form.name.trim())', '默认名生成必须限定新建态且名称为空')

const apiKeySectionSource = readFileSync(resolve(srcRoot, 'views/accounts/AccountApiKeySection.vue'), 'utf8')
assertIncludes(apiKeySectionSource, 'followBaseUrlAccountName(props.form.name, value)', 'Base URL 名称跟随必须走统一状态机函数')
assertIncludes(apiKeySectionSource, '@paste="suggestAccountNameFromBaseUrlPaste"', 'Base URL 输入必须保留粘贴触发的名称跟随')
assertIncludes(apiKeySectionSource, '@change="suggestAccountNameFromBaseUrlChange"', 'Base URL 输入必须支持键入变更触发的名称跟随')

console.log('账户名称建议回归通过：域名提取、随机后缀、默认名生成与 Base URL 跟随状态机契约符合预期')

function assertEqual(actual: string | number | boolean, expected: string | number | boolean, message: string): void {
  if (actual !== expected) {
    throw new Error(`${message}，期望 ${expected}，实际 ${actual}`)
  }
}

function assertMatch(actual: string, pattern: RegExp, message: string): void {
  if (!pattern.test(actual)) {
    throw new Error(`${message}，实际 ${actual}`)
  }
}

function assertIncludes(source: string, needle: string, message: string): void {
  if (!source.includes(needle)) {
    throw new Error(`${message}，未在源码中找到 ${needle}`)
  }
}
