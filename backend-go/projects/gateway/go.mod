module github.com/huanminabc/juhe-ai/backend-go-gateway

go 1.26.0

require (
	github.com/alicebob/miniredis/v2 v2.38.0
	github.com/andybalholm/brotli v1.2.0
	github.com/gorilla/websocket v1.5.3
	github.com/huanminabc/juhe-ai/backend-go-contracts v0.0.0
	github.com/huanminabc/juhe-ai/backend-go-maintenance v0.0.0
	github.com/huanminabc/juhe-ai/backend-go-platform v0.0.0
	github.com/jackc/pgx/v5 v5.10.0
	github.com/redis/go-redis/v9 v9.17.2
	github.com/tiktoken-go/tokenizer v0.8.0
	golang.org/x/image v0.45.0
	golang.org/x/sync v0.22.0
	golang.org/x/sys v0.47.0
	golang.org/x/text v0.41.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/dlclark/regexp2/v2 v2.1.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/client_golang v1.23.2 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.66.1 // indirect
	github.com/prometheus/procfs v0.16.1 // indirect
	github.com/rogpeppe/go-internal v1.16.0 // indirect
	github.com/yuin/gopher-lua v1.1.1 // indirect
	go.yaml.in/yaml/v2 v2.4.2 // indirect
	google.golang.org/protobuf v1.36.8 // indirect
)

replace github.com/huanminabc/juhe-ai/backend-go-contracts => ../../shared/contracts

replace github.com/huanminabc/juhe-ai/backend-go-platform => ../../shared/platform

// Test-only baseline amendment (originally 2026-09-04 X05 six-database sqlite
// preflight; 2026-10-24 存储形态收口后仅存测试用途): the gateway cmd-layer
// tests provision isolated pgtestkit databases through the maintenance
// bootstrap export surface (bootstrap.EnsurePostgres/SeedPostgres fresh DDL).
// Production code no longer imports this module (D11: the composition-root
// sqlite preflight was the only runtime consumer and is deleted).
replace github.com/huanminabc/juhe-ai/backend-go-maintenance => ../maintenance
