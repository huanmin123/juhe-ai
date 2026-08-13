module github.com/huanminabc/juhe-ai/backend-go-gateway

go 1.26.0

require (
	github.com/huanminabc/juhe-ai/backend-go-contracts v0.0.0
	github.com/huanminabc/juhe-ai/backend-go-platform v0.0.0
	github.com/jackc/pgx/v5 v5.10.0
	golang.org/x/text v0.29.0
	modernc.org/sqlite v1.56.0
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	modernc.org/libc v1.74.4 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)

replace github.com/huanminabc/juhe-ai/backend-go-contracts => ../../shared/contracts

replace github.com/huanminabc/juhe-ai/backend-go-platform => ../../shared/platform
