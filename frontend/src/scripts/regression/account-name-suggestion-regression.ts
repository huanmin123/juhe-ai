import { accountNameFromBaseUrl } from '../../views/accounts/accountNameSuggestion'

assertEqual(accountNameFromBaseUrl('https://api.openai.com/v1'), 'api.openai.com', '应从标准 Base URL 提取域名')
assertEqual(accountNameFromBaseUrl(' https://Gateway.Example.com:8443/openai/v1 '), 'gateway.example.com', '域名应忽略端口和路径并规范化')
assertEqual(accountNameFromBaseUrl('http://127.0.0.1:3000/v1'), '127.0.0.1', '本地 Base URL 应提取主机名')
assertEqual(accountNameFromBaseUrl('api.example.com/v1'), '', '缺少协议的地址不应生成账户名称')
assertEqual(accountNameFromBaseUrl('ftp://api.example.com'), '', '非 HTTP(S) 地址不应生成账户名称')
assertEqual(accountNameFromBaseUrl(''), '', '空 Base URL 不应生成账户名称')

console.log('账户名称建议回归通过：Base URL 域名提取及无效输入处理符合预期')

function assertEqual(actual: string, expected: string, message: string): void {
  if (actual !== expected) {
    throw new Error(`${message}，期望 ${expected}，实际 ${actual}`)
  }
}
