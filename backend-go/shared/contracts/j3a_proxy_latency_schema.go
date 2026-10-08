package contracts

// PostgresColumnSpec is the minimum structural contract needed by a runtime
// Store. Extra columns remain forward-compatible, but required columns may
// not change type or nullability.
type PostgresColumnSpec struct {
	DataType string
	UdtName  string
	Nullable bool
}

var J3AProxyLatencyTables = []string{
	"proxy_latency_owner_leases",
	"proxy_latency_proxy_leases",
	"proxy_latency_outcomes",
	"proxy_latency_input_versions",
	"proxy_latency_inputs",
	"proxy_latency_execution_claims",
}

var J3AProxyLatencyIndexes = map[string]string{
	"idx_proxy_latency_outcomes_proxy":  "on juhe_jobs.proxy_latency_outcomes using btree (proxy_id, observed_at)",
		"idx_proxy_latency_outcomes_cursor": "on juhe_jobs.proxy_latency_outcomes using btree (stored_at, outcome_id)",
		// BUG-0300：过期输入清理子查询（WHERE expires_at < cutoff LIMIT n）无索引
		// 时逐批全表扫宽表（payload jsonb），单条 DELETE 实测 21s 长事务引发跨组件
		// 锁风暴（F1/F4 lease 验证 55P03、J2/J3a 超时）。
		"idx_proxy_latency_inputs_expires_at": "on juhe_jobs.proxy_latency_inputs using btree (expires_at)",
	}

var J3AProxyLatencyColumns = map[string]map[string]PostgresColumnSpec{
	"proxy_latency_owner_leases": {
		"lease_key": {DataType: "text", UdtName: "text"}, "owner_id": {DataType: "text", UdtName: "text"}, "fence_token": {DataType: "bigint", UdtName: "int8"}, "lease_until": {DataType: "timestamp with time zone", UdtName: "timestamptz"}, "updated_at": {DataType: "timestamp with time zone", UdtName: "timestamptz"},
	},
	"proxy_latency_proxy_leases": {
		"proxy_id": {DataType: "text", UdtName: "text"}, "owner_id": {DataType: "text", UdtName: "text"}, "fence_token": {DataType: "bigint", UdtName: "int8"}, "lease_until": {DataType: "timestamp with time zone", UdtName: "timestamptz"}, "updated_at": {DataType: "timestamp with time zone", UdtName: "timestamptz"},
	},
	"proxy_latency_outcomes": {
		"outcome_id": {DataType: "text", UdtName: "text"}, "request_id": {DataType: "text", UdtName: "text"}, "proxy_id": {DataType: "text", UdtName: "text"}, "input_version": {DataType: "bigint", UdtName: "int8"}, "config_revision": {DataType: "text", UdtName: "text"}, "trigger": {DataType: "text", UdtName: "text"}, "owner_fence_token": {DataType: "bigint", UdtName: "int8"}, "proxy_fence_token": {DataType: "bigint", UdtName: "int8"}, "observed_at": {DataType: "timestamp with time zone", UdtName: "timestamptz"}, "stored_at": {DataType: "timestamp with time zone", UdtName: "timestamptz"}, "payload": {DataType: "jsonb", UdtName: "jsonb"}, "payload_digest": {DataType: "text", UdtName: "text"}, "committed": {DataType: "boolean", UdtName: "bool"},
	},
	"proxy_latency_input_versions": {
		"proxy_id": {DataType: "text", UdtName: "text"}, "next_version": {DataType: "bigint", UdtName: "int8"}, "updated_at": {DataType: "timestamp with time zone", UdtName: "timestamptz"},
	},
	"proxy_latency_inputs": {
		"request_id": {DataType: "text", UdtName: "text"}, "proxy_id": {DataType: "text", UdtName: "text"}, "input_version": {DataType: "bigint", UdtName: "int8"}, "config_revision": {DataType: "text", UdtName: "text"}, "trigger": {DataType: "text", UdtName: "text"}, "issued_at": {DataType: "timestamp with time zone", UdtName: "timestamptz"}, "expires_at": {DataType: "timestamp with time zone", UdtName: "timestamptz"}, "payload": {DataType: "jsonb", UdtName: "jsonb"}, "payload_digest": {DataType: "text", UdtName: "text"},
	},
	"proxy_latency_execution_claims": {
		"request_id": {DataType: "text", UdtName: "text"}, "claim_token": {DataType: "text", UdtName: "text"}, "outcome_id": {DataType: "text", UdtName: "text"}, "proxy_id": {DataType: "text", UdtName: "text"}, "input_version": {DataType: "bigint", UdtName: "int8"}, "config_revision": {DataType: "text", UdtName: "text"}, "trigger": {DataType: "text", UdtName: "text"}, "owner_id": {DataType: "text", UdtName: "text"}, "owner_fence_token": {DataType: "bigint", UdtName: "int8"}, "proxy_fence_token": {DataType: "bigint", UdtName: "int8"}, "input_digest": {DataType: "text", UdtName: "text"}, "claim_until": {DataType: "timestamp with time zone", UdtName: "timestamptz"}, "updated_at": {DataType: "timestamp with time zone", UdtName: "timestamptz"},
	},
}

var J3AProxyLatencyConstraints = map[string][]string{
	"proxy_latency_owner_leases":     {"primary key (lease_key)"},
	"proxy_latency_proxy_leases":     {"primary key (proxy_id)"},
	"proxy_latency_outcomes":         {"primary key (outcome_id)", "unique (request_id)"},
	"proxy_latency_input_versions":   {"primary key (proxy_id)"},
	"proxy_latency_inputs":           {"primary key (request_id)", "unique (proxy_id, input_version)"},
	"proxy_latency_execution_claims": {"primary key (request_id)"},
}
