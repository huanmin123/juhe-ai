module github.com/huanminabc/juhe-ai/backend-go-gateway

go 1.26.0

require (
	github.com/alicebob/miniredis/v2 v2.38.0
	github.com/huanminabc/juhe-ai/backend-go-contracts v0.0.0
	github.com/huanminabc/juhe-ai/backend-go-platform v0.0.0
	github.com/jackc/pgx/v5 v5.10.0
	github.com/redis/go-redis/v9 v9.17.2
	github.com/tiktoken-go/tokenizer v0.8.0
	golang.org/x/text v0.29.0
	modernc.org/sqlite v1.56.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/dlclark/regexp2/v2 v2.1.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/yuin/gopher-lua v1.1.1 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	modernc.org/libc v1.74.4 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)

replace github.com/huanminabc/juhe-ai/backend-go-contracts => ../../shared/contracts

replace github.com/huanminabc/juhe-ai/backend-go-platform => ../../shared/platform
