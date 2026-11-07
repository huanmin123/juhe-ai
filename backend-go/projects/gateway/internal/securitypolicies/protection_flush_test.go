package securitypolicies

// 防护次数刷写任务单元测试（接口级 fake：ProtectionRedisClient + protection
// FlushDB 的进程内假实现，不依赖外部 Redis / PG）：GETDEL→UPDATE 求和、SCAN
// 单轮有界、失败下轮继续。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"testing"

	redis "github.com/redis/go-redis/v9"
)

// flushFakeRedis 是 ProtectionRedisClient 的最小进程内假实现：STRING 数据面
// + SCAN 记录，GetDel 即取即删（原子语义按单线程测试约定）。SCAN 在 cursor=0
// 时对当次匹配集做快照分页（页间 GetDel 删除不影响快照——近似真 SCAN 的稳
// 定遍历视图），cursor>0 时继续供快照后续页。
type flushFakeRedis struct {
	data      map[string]string
	scanMatch string
	scanCount int64
	scanErr   error
	getDelErr map[string]error
	snapKeys  []string
}

func newFlushFakeRedis() *flushFakeRedis {
	return &flushFakeRedis{data: map[string]string{}, getDelErr: map[string]error{}}
}

func (f *flushFakeRedis) IncrBy(_ context.Context, key string, value int64) *redis.IntCmd {
	cmd := redis.NewIntCmd(context.Background())
	f.data[key] = strconv.FormatInt(value, 10)
	cmd.SetVal(value)
	return cmd
}

func (f *flushFakeRedis) Del(_ context.Context, keys ...string) *redis.IntCmd {
	cmd := redis.NewIntCmd(context.Background())
	removed := int64(0)
	for _, key := range keys {
		if _, ok := f.data[key]; ok {
			delete(f.data, key)
			removed++
		}
	}
	cmd.SetVal(removed)
	return cmd
}

func (f *flushFakeRedis) Scan(_ context.Context, cursor uint64, match string, count int64) *redis.ScanCmd {
	cmd := redis.NewScanCmd(context.Background(), nil)
	f.scanMatch, f.scanCount = match, count
	if f.scanErr != nil {
		cmd.SetErr(f.scanErr)
		return cmd
	}
	if cursor == 0 {
		f.snapKeys = nil
		for key := range f.data {
			if matched, _ := path.Match(match, key); matched {
				f.snapKeys = append(f.snapKeys, key)
			}
		}
		sortKeys(f.snapKeys)
	}
	pageSize := int(count)
	if pageSize <= 0 {
		pageSize = len(f.snapKeys)
	}
	page := int(cursor)
	start := page * pageSize
	if start >= len(f.snapKeys) {
		cmd.SetVal(nil, 0)
		return cmd
	}
	end := start + pageSize
	if end > len(f.snapKeys) {
		end = len(f.snapKeys)
	}
	next := uint64(0)
	if end < len(f.snapKeys) {
		next = uint64(page + 1)
	}
	// 只返回快照中仍存在的键（页间可能已被 GetDel 取走）。
	pageKeys := make([]string, 0, end-start)
	for _, key := range f.snapKeys[start:end] {
		if _, ok := f.data[key]; ok {
			pageKeys = append(pageKeys, key)
		}
	}
	cmd.SetVal(pageKeys, next)
	return cmd
}

func (f *flushFakeRedis) GetDel(_ context.Context, key string) *redis.StringCmd {
	cmd := redis.NewStringCmd(context.Background())
	if err, ok := f.getDelErr[key]; ok {
		cmd.SetErr(err)
		return cmd
	}
	value, ok := f.data[key]
	if !ok {
		cmd.SetErr(redis.Nil)
		return cmd
	}
	delete(f.data, key)
	cmd.SetVal(value)
	return cmd
}

// flushFakeDB 是 protectionFlushDB 的记录式假实现。
type flushFakeDB struct {
	updates []flushUpdate
	errOn   map[string]error
}

type flushUpdate struct {
	delta  int64
	policy string
}

func (f *flushFakeDB) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	if len(args) != 2 {
		return nil, fmt.Errorf("参数数不符: %d", len(args))
	}
	delta, _ := args[0].(int64)
	policy, _ := args[1].(string)
	if err, ok := f.errOn[policy]; ok {
		return nil, err
	}
	f.updates = append(f.updates, flushUpdate{delta: delta, policy: policy})
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("空查询")
	}
	return rowsAffectedResult{n: 1}, nil
}

type rowsAffectedResult struct{ n int64 }

func (r rowsAffectedResult) LastInsertId() (int64, error) { return 0, nil }
func (r rowsAffectedResult) RowsAffected() (int64, error) { return r.n, nil }

func sortKeys(keys []string) {
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
}

func TestProtectionFlusherMergesDeltas(t *testing.T) {
	redisFake := newFlushFakeRedis()
	db := &flushFakeDB{}
	flusher := NewProtectionFlusher(redisFake, "dev:routestate", db)
	if flusher == nil {
		t.Fatal("flusher 不应为 nil")
	}
	// Increment 经同一键拼装写入（写侧与刷写共用键来源）。
	ctx := context.Background()
	store := NewProtectionCountStore(redisFake, "dev:routestate")
	if err := store.Increment(ctx, "secpol_a", 3, "t1"); err != nil {
		t.Fatalf("Increment: %v", err)
	}
	if err := store.Increment(ctx, "secpol_b", 2, "t2"); err != nil {
		t.Fatalf("Increment: %v", err)
	}
	// 非防护键（前缀不符）不进 MATCH 空间。
	redisFake.data["juhe-ai:{dev:routestate}:state:other:v1:x"] = "9"

	flushed, err := flusher.FlushOnce(ctx)
	if err != nil {
		t.Fatalf("FlushOnce: %v", err)
	}
	if flushed != 2 {
		t.Fatalf("flushed 应为 2，得到 %d", flushed)
	}
	if len(db.updates) != 2 {
		t.Fatalf("UPDATE 次数应为 2，得到 %d", len(db.updates))
	}
	sums := map[string]int64{}
	for _, update := range db.updates {
		sums[update.policy] += update.delta
	}
	if sums["secpol_a"] != 3 || sums["secpol_b"] != 2 {
		t.Fatalf("UPDATE 求和不符: %v", sums)
	}
	if len(redisFake.data) != 1 {
		t.Fatalf("GETDEL 后计数键应全部取走，剩余 %v", redisFake.data)
	}
	for key := range redisFake.data {
		if strings.Contains(key, "security-policy-protection") {
			t.Fatalf("防护键未被取走: %s", key)
		}
	}
}

func TestProtectionFlusherScanBounded(t *testing.T) {
	redisFake := newFlushFakeRedis()
	db := &flushFakeDB{}
	flusher := NewProtectionFlusher(redisFake, "ns", db)
	prefix := protectionCountKeyPrefix("ns")
	for i := 0; i < protectionFlushMaxKeysPerRound+50; i++ {
		redisFake.data[fmt.Sprintf("%ssecpol_%03d", prefix, i)] = "1"
	}
	flushed, err := flusher.FlushOnce(context.Background())
	if err != nil {
		t.Fatalf("FlushOnce: %v", err)
	}
	if flushed != protectionFlushMaxKeysPerRound {
		t.Fatalf("单轮上限应为 %d，得到 %d", protectionFlushMaxKeysPerRound, flushed)
	}
	if redisFake.scanCount != protectionFlushScanCount {
		t.Fatalf("SCAN COUNT 提示应为 %d，得到 %d", protectionFlushScanCount, redisFake.scanCount)
	}
	// 剩余增量留在 Redis，下一轮继续取走（不丢）。
	flushed2, err := flusher.FlushOnce(context.Background())
	if err != nil {
		t.Fatalf("FlushOnce 第二轮: %v", err)
	}
	if flushed2 != 50 {
		t.Fatalf("第二轮应取走剩余 50 键，得到 %d", flushed2)
	}
}

func TestProtectionFlusherFailuresContinue(t *testing.T) {
	t.Run("单键 UPDATE 失败记 WARN 并继续", func(t *testing.T) {
		redisFake := newFlushFakeRedis()
		db := &flushFakeDB{errOn: map[string]error{"secpol_bad": errors.New("db down")}}
		flusher := NewProtectionFlusher(redisFake, "ns", db)
		prefix := protectionCountKeyPrefix("ns")
		redisFake.data[prefix+"secpol_bad"] = "5"
		redisFake.data[prefix+"secpol_good"] = "7"
		flushed, err := flusher.FlushOnce(context.Background())
		if err != nil {
			t.Fatalf("单键失败不应上抛: %v", err)
		}
		if flushed != 1 {
			t.Fatalf("失败键不计入 flushed（应为 1），得到 %d", flushed)
		}
		if len(db.updates) != 1 || db.updates[0].policy != "secpol_good" || db.updates[0].delta != 7 {
			t.Fatalf("成功键应照常合并: %v", db.updates)
		}
		// 失败键的增量已 GETDEL 取走、不回灌（丢失可接受，绝不重复计数）。
		if _, ok := redisFake.data[prefix+"secpol_bad"]; ok {
			t.Fatal("失败键不应回灌 Redis")
		}
	})
	t.Run("SCAN 失败上抛下轮整轮重试", func(t *testing.T) {
		redisFake := newFlushFakeRedis()
		redisFake.scanErr = errors.New("redis down")
		flusher := NewProtectionFlusher(redisFake, "ns", &flushFakeDB{})
		if _, err := flusher.FlushOnce(context.Background()); err == nil {
			t.Fatal("SCAN 失败应上抛错误")
		}
	})
	t.Run("GETDEL redis.Nil 按竞态跳过", func(t *testing.T) {
		redisFake := newFlushFakeRedis()
		db := &flushFakeDB{}
		flusher := NewProtectionFlusher(redisFake, "ns", db)
		prefix := protectionCountKeyPrefix("ns")
		redisFake.data[prefix+"secpol_ghost"] = "1"
		redisFake.getDelErr[prefix+"secpol_ghost"] = redis.Nil
		flushed, err := flusher.FlushOnce(context.Background())
		if err != nil || flushed != 0 {
			t.Fatalf("redis.Nil 应静默跳过: flushed=%d err=%v", flushed, err)
		}
	})
	t.Run("零值与损坏值丢弃", func(t *testing.T) {
		redisFake := newFlushFakeRedis()
		db := &flushFakeDB{}
		flusher := NewProtectionFlusher(redisFake, "ns", db)
		prefix := protectionCountKeyPrefix("ns")
		redisFake.data[prefix+"secpol_zero"] = "0"
		redisFake.data[prefix+"secpol_junk"] = "abc"
		flushed, err := flusher.FlushOnce(context.Background())
		if err != nil {
			t.Fatalf("FlushOnce: %v", err)
		}
		if flushed != 0 || len(db.updates) != 0 {
			t.Fatalf("零值/损坏值不得产生 UPDATE: flushed=%d updates=%v", flushed, db.updates)
		}
	})
}

func TestProtectionFlusherUnassembled(t *testing.T) {
	if NewProtectionFlusher(nil, "ns", &flushFakeDB{}) != nil {
		t.Fatal("无 Redis 客户端应返回 nil")
	}
	var nilFlusher *ProtectionFlusher
	flushed, err := nilFlusher.FlushOnce(context.Background())
	if err != nil || flushed != 0 {
		t.Fatalf("nil flusher 应安全无操作: %d %v", flushed, err)
	}
}
