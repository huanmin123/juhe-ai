package main

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestIsUndefinedRelationOrSchema 钉住 fresh 空库语义：upgrade 在
// EnsurePostgres 之前执行步骤谓词，限定 juhe_business schema 的查询在
// schema 尚未创建时返回 3F000，必须与 42P01（表缺失）一样按 0 行处理。
func TestIsUndefinedRelationOrSchema(t *testing.T) {
	for _, code := range []string{"42P01", "3F000"} {
		if !isUndefinedRelationOrSchema(&pgconn.PgError{Code: code}) {
			t.Fatalf("PG %s must be treated as missing relation/schema", code)
		}
	}
	if isUndefinedRelationOrSchema(errors.New("other")) ||
		isUndefinedRelationOrSchema(&pgconn.PgError{Code: "08006"}) {
		t.Fatal("non-missing-schema errors must stay fatal")
	}
}
