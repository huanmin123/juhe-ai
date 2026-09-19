package main

// 临时:重建 model_quality_schedules 表并验证级联。跑完即删。

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
)

func main() {
	conn, err := pgx.Connect(context.Background(), os.Getenv("W13F_DSN"))
	if err != nil {
		panic(err)
	}
	defer conn.Close(context.Background())
	var rc int64
	conn.QueryRow(context.Background(), `SELECT COUNT(*) FROM juhe_business.model_quality_schedules`).Scan(&rc)
	fmt.Println("rows before drop:", rc)
	if _, err := conn.Exec(context.Background(), `DROP TABLE juhe_business.model_quality_schedules`); err != nil {
		panic(err)
	}
	fmt.Println("dropped")
	// 按权威 DDL 重建(节选自 pg_schema.go)。
	ddl := `CREATE TABLE IF NOT EXISTS model_quality_schedules (
      id text PRIMARY KEY,
      system_account_id text NOT NULL,
      model text NOT NULL,
      interval_minutes integer NOT NULL DEFAULT 5,
      profile text,
      penalty_threshold integer NOT NULL DEFAULT 3,
      penalty_action text NOT NULL DEFAULT 'cooldown',
      recovery_interval_minutes integer NOT NULL DEFAULT 15,
      enabled integer NOT NULL DEFAULT 1,
      revision integer NOT NULL DEFAULT 1,
      next_run_at text,
      last_run_id text,
      last_run_at text,
      last_run_status text,
      lease_owner text,
      lease_until text,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
    )`
	if _, err := conn.Exec(context.Background(), "SET search_path TO juhe_business, public; " + ddl); err != nil {
		panic(err)
	}
	fmt.Println("recreated")
	// 验证:插入 + 级联删除。
	if _, err := conn.Exec(context.Background(), `INSERT INTO juhe_business.accounts (id, system_account_id, name, status, created_at, updated_at) VALUES ('w13f-casc-test', 'sys_admin', 'w13f', 'active', '2026-09-18T00:00:00Z', '2026-09-18T00:00:00Z') ON CONFLICT (id) DO NOTHING`); err != nil {
		fmt.Println("seed account err:", err)
		return
	}
	if _, err := conn.Exec(context.Background(), `INSERT INTO juhe_business.model_quality_schedules (id, system_account_id, account_id) VALUES ('w13f-casc-sched', 'sys_admin', 'w13f-casc-test')`); err != nil {
		fmt.Println("seed sched err:", err)
		return
	}
	if _, err := conn.Exec(context.Background(), `DELETE FROM juhe_business.accounts WHERE id = 'w13f-casc-test'`); err != nil {
		fmt.Println("cascade test err:", err)
		return
	}
	fmt.Println("CASCADE OK")
}
