package idgen

import "testing"

func TestRandomHexLength(t *testing.T) {
	for _, n := range []int{4, 8, 16, 32} {
		got := RandomHex(n)
		if len(got) != n*2 {
			t.Fatalf("RandomHex(%d) 长度=%d want %d", n, len(got), n*2)
		}
	}
}

func TestRandomHexNonDeterministic(t *testing.T) {
	a := RandomHex(16)
	b := RandomHex(16)
	if a == b {
		t.Fatal("两次随机 hex 不应相同（概率性断言，16 字节碰撞可忽略）")
	}
}

func TestRandomHexZeroOrNegative(t *testing.T) {
	if got := RandomHex(0); got != "" {
		t.Fatalf("RandomHex(0)=%q want 空串", got)
	}
	if got := RandomHex(-1); got != "" {
		t.Fatalf("RandomHex(-1)=%q want 空串", got)
	}
}

func TestRandomHexFallsBackToTimeEntropyOnReadError(t *testing.T) {
	saved := readRandom
	readRandom = func(b []byte) (int, error) { return 0, errInjectedRead }
	t.Cleanup(func() { readRandom = saved })
	got := RandomHex(16)
	if len(got) != 32 {
		t.Fatalf("回退路径长度=%d want 32", len(got))
	}
	if got == "00000000000000000000000000000000" {
		t.Fatal("回退路径应写入时间熵，不应全零")
	}
}

var errInjectedRead = &readError{}

type readError struct{}

func (*readError) Error() string { return "injected read failure" }
