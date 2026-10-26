module github.com/huanminabc/juhe-ai/backend-go-jobs

go 1.26.0

require (
	github.com/alicebob/miniredis/v2 v2.38.0
	github.com/huanminabc/juhe-ai/backend-go-contracts v0.0.0
	github.com/huanminabc/juhe-ai/backend-go-platform v0.0.0
	github.com/jackc/pgx/v5 v5.10.0
	github.com/redis/go-redis/v9 v9.17.2
	golang.org/x/sys v0.47.0
	golang.org/x/text v0.41.0
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/client_golang v1.23.2 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.66.1 // indirect
	github.com/prometheus/procfs v0.16.1 // indirect
	github.com/yuin/gopher-lua v1.1.1 // indirect
	go.yaml.in/yaml/v2 v2.4.2 // indirect
	golang.org/x/sync v0.22.0 // indirect
	google.golang.org/protobuf v1.36.8 // indirect
)

replace github.com/huanminabc/juhe-ai/backend-go-contracts => ../../shared/contracts

replace github.com/huanminabc/juhe-ai/backend-go-platform => ../../shared/platform
