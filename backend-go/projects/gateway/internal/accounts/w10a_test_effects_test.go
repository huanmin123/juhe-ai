package accounts

// w10a 第五批：TestDispatchEffects 端口存取与 Deps.wireTestEffects 装配。

import "testing"

func TestW10ATestDispatchEffectsPort(t *testing.T) {
	var nilStore *Store
	if nilStore.TestDispatchEffects() != nil {
		t.Fatal("nil store 应返回 nil 端口")
	}
	env := newTestEnv(t)
	if env.store.TestDispatchEffects() != nil {
		t.Fatal("未装配时应返回 nil")
	}
	fake := &fakeTestEffects{accept: true}
	env.store.SetTestDispatchEffects(fake)
	if env.store.TestDispatchEffects() != fake {
		t.Fatal("装配后应返回同一端口")
	}
}

func TestW10AWireTestEffects(t *testing.T) {
	env := newTestEnv(t)
	fake := &fakeTestEffects{accept: false}
	// TestDispatch 非 nil → 装配。
	deps := &Deps{Store: env.store, TestDispatch: fake}
	deps.wireTestEffects()
	if env.store.TestDispatchEffects() != fake {
		t.Fatal("wireTestEffects 未装配非 nil 端口")
	}
	// TestDispatch nil → 保持空。
	env2 := newTestEnv(t)
	(&Deps{Store: env2.store}).wireTestEffects()
	if env2.store.TestDispatchEffects() != nil {
		t.Fatal("TestDispatch nil 不应装配")
	}
	// nil Store 的 Deps 不 panic。
	(&Deps{}).wireTestEffects()
}
