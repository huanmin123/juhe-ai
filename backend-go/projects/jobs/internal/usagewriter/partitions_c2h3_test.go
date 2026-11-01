package usagewriter

import (
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// C2-H3：分区备忘必须能被证伪——dataretention DETACH/DROP 过期分区后，
// 备忘命中的日期键不能继续短路 DDL，否则迟到记录撞 no partition 会把整批
// 正常记录拖成死信。

func TestC2H3BenignPartitionDDLConflict(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "duplicate_table", err: &pgconn.PgError{Code: "42P07"}, want: true},
		{name: "duplicate_object", err: &pgconn.PgError{Code: "42710"}, want: true},
		{name: "unique_violation", err: &pgconn.PgError{Code: "23505"}, want: true},
		{name: "other_pg_error", err: &pgconn.PgError{Code: "42P01"}, want: false},
		{name: "plain_error", err: errPlain("boom"), want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isBenignPartitionDDLConflict(tc.err); got != tc.want {
				t.Fatalf("isBenignPartitionDDLConflict(%v)=%v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestC2H3MissingUsageRecordPartition(t *testing.T) {
	missing := &pgconn.PgError{Code: "23514", Message: `no partition of relation "usage_records" found for row`}
	if !isMissingUsageRecordPartition(missing) {
		t.Fatal("23514 no-partition 错误必须识别为分区缺失")
	}
	otherCheck := &pgconn.PgError{Code: "23514", Message: "some other check violation"}
	if isMissingUsageRecordPartition(otherCheck) {
		t.Fatal("其它 check violation 不得误判为分区缺失")
	}
	if isMissingUsageRecordPartition(errPlain("boom")) {
		t.Fatal("普通错误不得误判为分区缺失")
	}
}

func TestC2H3ExistsQueryRebuildsDroppedPartition(t *testing.T) {
	recorder := &partitionDDLRecorder{}
	ensured := &ensuredPartitionDateKeys{}
	createdAts := []string{"2026-11-01T10:00:00.000Z"}

	present := true
	exists := func(ctx Ctx, partitionName string) (bool, error) {
		return present, nil
	}
	if err := ensurePostgresUsageRecordPartitionsChecked(t.Context(), recorder, ensured, createdAts, exists); err != nil {
		t.Fatal(err)
	}
	if recorder.count() != 0 {
		t.Fatalf("分区存在时不应执行 DDL，实际 %d 次", recorder.count())
	}

	// 保留策略 DROP 分区后，备忘仍记着该日期键；存在性复核必须让它失效。
	present = false
	if err := ensurePostgresUsageRecordPartitionsChecked(t.Context(), recorder, ensured, createdAts, exists); err != nil {
		t.Fatal(err)
	}
	if recorder.count() != 1 {
		t.Fatalf("分区被 DROP 后必须重建一次，实际 DDL %d 次", recorder.count())
	}
}

func TestC2H3ForgetForcesRecreate(t *testing.T) {
	recorder := &partitionDDLRecorder{}
	ensured := &ensuredPartitionDateKeys{}
	createdAts := []string{"2026-11-01T10:00:00.000Z"}

	if err := ensurePostgresUsageRecordPartitions(t.Context(), recorder, ensured, createdAts); err != nil {
		t.Fatal(err)
	}
	if recorder.count() != 1 {
		t.Fatalf("首次 ensure 应执行一次 DDL，实际 %d", recorder.count())
	}
	// 备忘命中：第二次不执行 DDL。
	if err := ensurePostgresUsageRecordPartitions(t.Context(), recorder, ensured, createdAts); err != nil {
		t.Fatal(err)
	}
	if recorder.count() != 1 {
		t.Fatalf("备忘命中不应重复 DDL，实际 %d", recorder.count())
	}
	// forget 后必须重新执行（INSERT 撞 no partition 的恢复路径）。
	ensured.forget("20261101")
	if err := ensurePostgresUsageRecordPartitions(t.Context(), recorder, ensured, createdAts); err != nil {
		t.Fatal(err)
	}
	if recorder.count() != 2 {
		t.Fatalf("forget 后应重新执行 DDL，实际 %d", recorder.count())
	}
}

type errPlain string

func (e errPlain) Error() string { return string(e) }
