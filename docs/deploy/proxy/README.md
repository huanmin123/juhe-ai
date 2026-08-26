# 网络代理部署目录

> 面向需要通过安全网络代理访问上游模型 API 的部署。
> 这里说明 sing-box 的安装、启动、本地代理端口和 juhe-ai 后台代理绑定方式。

## 文档索引

- [sing-box 网络代理部署指南](sing-box网络代理部署指南.md)：Linux、Windows、macOS 下安装 sing-box、配置本机 mixed 代理和接入 juhe-ai。
- [代理部署示例](代理部署示例.md)：一次本机 sing-box + juhe-ai 账号代理绑定示例。

## 适用边界

- juhe-ai 中转请求上游 API 时，推荐使用后台“代理管理”配置账号级代理。
- `JUHE_AI_OAUTH_PROXY_URL` 只作为 OpenAI OAuth token 换取 / 刷新的兜底代理，不是所有模型请求的全局代理。
- 系统安装依赖、Docker 拉镜像、npm registry 访问属于服务器自身网络代理，和账号级上游代理是两件事。
