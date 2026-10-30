// Package version 承载三项目共用的构建版本标识（发布身份单一事实源）。
//
// 注入方式：发布构建经 -ldflags -X 注入三项（Version/Commit/BuildTime），
// 源码直建保留默认值。消费点：三项目 --version、/health 增键
// （version/commit/schemaRevision）、常驻进程启动日志首行、（P1 起）升级
// 账本 app_version 与 schema_state。
//
// 脏工作树防线归发布脚本承担（deploy.sh / package-release.*：命中 tag 且
// git status 非空时拒绝注入 tag 版本号），本包不做任何检测。
//
// 契约：《升级兼容与迁移账本设计》§6.2/§6.3。
package version

// 默认值即「未注入」的可辨识形态：Version=dev、Commit=none、BuildTime=unknown。
var (
	Version   = "dev"
	Commit    = "none"
	BuildTime = "unknown"
)
