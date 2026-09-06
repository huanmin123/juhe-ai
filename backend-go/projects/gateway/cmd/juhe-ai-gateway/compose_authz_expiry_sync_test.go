package main

// T6d gateway 侧消费面装配断言：
//  1. 组合根/入口源码断言——main.go 必须把 authz-expiry-runtime-sync 组件
//     追加进 supervisor（装配断线零容忍，复用源码断言先例）；
//  2. 组件冒烟——真实 authz store 上启动组件，ctx 取消后退出。

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
)

func TestMainWiresAuthzExpiryRuntimeSyncComponent(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if !strings.Contains(text, "components = append(components, newAuthzExpiryRuntimeSyncComponent(composed.AuthzStore))") {
		t.Fatal("main must append the authz expiry runtime sync component after composing the system api")
	}
	composeSource, err := os.ReadFile("compose.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(composeSource), "composed.AuthzStore = authzStore") {
		t.Fatal("compose root must retain the authz store for the expiry sync component")
	}
}

func TestAuthzExpiryRuntimeSyncComponentExitsOnCancel(t *testing.T) {
	db, err := sql.Open("sqlite", "file:authz-expiry-component?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// 组件只需要一个能执行 ReconcileExpiredGrants 扫描的 store；空库（无
	// grants 表）会让首轮失败并被组件吞掉（尽力而为语义），ctx 取消即退出。
	store, err := authz.NewStore(db, false, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	component := newAuthzExpiryRuntimeSyncComponent(store)
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- component.Run(runCtx) }()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("component must report the cancel cause on exit")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("component must exit promptly after ctx cancel")
	}
}
