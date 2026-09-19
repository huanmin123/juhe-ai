package main

import (
	"database/sql"
	"fmt"
	"os"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	env := map[string]string{}
	data, _ := os.ReadFile(`F:/sub2api-lite/.local/project-resources/dev/env/shared.env`)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if i := strings.Index(line, "="); i > 0 {
			env[line[:i]] = line[i+1:]
		}
	}
	host := env["DEV_POSTGRES_HOST"]
	user := env["DEV_POSTGRES_ADMIN_USERNAME"]
	pw := env["DEV_POSTGRES_ADMIN_PASSWORD"]
	port := env["DEV_POSTGRES_DIRECT_PORT"]
	dbName := "juhe_ai_sub2api_dev_e2e0919m2"
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", user, pw, host, port, dbName)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		fmt.Println("open:", err)
		return
	}
	defer db.Close()

	var accID string
	err = db.QueryRow(`SELECT id FROM juhe_business.accounts WHERE name = 'e2e-quota账户'`).Scan(&accID)
	if err != nil {
		fmt.Println("account lookup:", err)
		return
	}
	fmt.Println("account:", accID)

	var status string
	var cooldown, obs, gen any
	err = db.QueryRow(`SELECT status, cooldown_until, cooldown_retest_observation_started_at, cooldown_retest_generation FROM juhe_business.accounts WHERE id = $1`, accID).Scan(&status, &cooldown, &obs, &gen)
	if err != nil {
		fmt.Println("account state:", err)
	} else {
		fmt.Printf("account status=%s cooldown=%v obs=%v gen=%v\n", status, cooldown, obs, gen)
	}

	rows, err := db.Query(`SELECT outcome, observed_at, error_code, error_message, dispatch_revision FROM juhe_jobs.account_health_outcomes WHERE account_id = $1 ORDER BY observed_at`, accID)
	if err != nil {
		fmt.Println("outcomes:", err)
	} else {
		defer rows.Close()
		for rows.Next() {
			var o, oa, ec, em, dr any
			_ = rows.Scan(&o, &oa, &ec, &em, &dr)
			fmt.Printf("outcome=%v at=%v code=%v msg=%.60s dr=%v\n", o, oa, ec, em, dr)
		}
	}

	var csIv, csCr, csDr any
	var csStatus string
	err = db.QueryRow(`SELECT input_version, config_revision, dispatch_revision, account_status FROM juhe_jobs.account_health_current_state WHERE account_id = $1`, accID).Scan(&csIv, &csCr, &csDr, &csStatus)
	if err != nil {
		fmt.Println("current_state:", err)
	} else {
		fmt.Printf("j1 current_state: iv=%v cr=%v dr=%v account_status=%s\n", csIv, csCr, csDr, csStatus)
	}

	var supN int
	var supDue any
	err = db.QueryRow(`SELECT count(*), max(next_due_at) FROM juhe_jobs.account_health_direct_input_suppressions WHERE account_id = $1`, accID).Scan(&supN, &supDue)
	if err != nil {
		fmt.Println("suppression:", err)
	} else {
		fmt.Printf("suppressions=%d next_due=%v\n", supN, supDue)
	}
}
