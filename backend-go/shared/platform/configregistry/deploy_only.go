package configregistry

// deployOnlyNames 是发布/编排侧变量：只被 docker compose、deploy.sh 等
// 部署入口读取，不会进入 gateway/jobs/maintenance 进程，因此不登记为
// 应用配置，也不参与进程归属检查。`check-config --env-file` 在校验 .env
// 时把它们视为合法变量，而不是未知 JUHE_AI_* 残留。
var deployOnlyNames = map[string]struct{}{
	"JUHE_AI_IMAGE_TAG":     {},
	"JUHE_AI_DEPLOY_SERVER": {},
	// 旧出海隧道域名的 compose extra_hosts IP 钉死，仅部署面使用；
	// proxy_profiles.host 核对完成前随 compose 一起保留。
	"JUHE_AI_PROXY_IP_A": {},
	"JUHE_AI_PROXY_IP_B": {},
	// 集群模式部署面（docker/cluster/，2026-11-07）：镜像引用、共享存储、
	// 入口上游与实例标识全部由 compose 插值消费，不进入应用进程配置；
	// compose env_file 会把它们一并注入容器，登记为部署侧变量避免
	// check-config 误判为未知残留。
	"JUHE_AI_REGISTRY":          {},
	"JUHE_AI_GATEWAY_IMAGE":     {},
	"JUHE_AI_JOBS_IMAGE":        {},
	"JUHE_AI_MAINTENANCE_IMAGE": {},
	"JUHE_AI_SHARED_ROOT":       {},
	"JUHE_AI_JOBS_INSTANCE_ID":  {},
	"JUHE_AI_GATEWAY_BIND":      {},
	"JUHE_AI_EDGE_DOMAIN":       {},
	"JUHE_AI_GATEWAY_UPSTREAMS": {},
	"JUHE_AI_FRONTEND_UPSTREAM": {},
}

// DeployOnly 报告 name 是否为部署侧专用变量。
func DeployOnly(name string) bool {
	_, ok := deployOnlyNames[name]
	return ok
}

// DeployOnlyNames 返回全部部署侧变量（排序稳定）。
func DeployOnlyNames() []string {
	names := make([]string, 0, len(deployOnlyNames))
	for name := range deployOnlyNames {
		names = append(names, name)
	}
	sortStrings(names)
	return names
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
