// Package pgcfg 承载 PostgreSQL 进程级环境归一。
package pgcfg

import "os"

// PrepareProcessEnv 把 compose 的 POSTGRES_PASSWORD 映射为 libpq/pgx 原生
// 读取的 PGPASSWORD，并设置进程级 application_name（PGAPPNAME）。
//
// 连接串自带密码时 URL 优先，PGPASSWORD 不会覆盖它；显式设置的
// PGPASSWORD/PGAPPNAME 不会被覆盖。
func PrepareProcessEnv(applicationName string) {
	if os.Getenv("PGPASSWORD") == "" {
		if password := os.Getenv("POSTGRES_PASSWORD"); password != "" {
			_ = os.Setenv("PGPASSWORD", password)
		}
	}
	if os.Getenv("PGAPPNAME") == "" && applicationName != "" {
		_ = os.Setenv("PGAPPNAME", applicationName)
	}
}
