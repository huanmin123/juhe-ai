package j3creadonly

// w11b 波次：Read 的 nil reader / 空 statHour / 空 account+hour 组合与
// trim 后的作用域传递。

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckowner"
)

func factFixtureForW11B() modelcheckowner.HealthFact {
	return modelcheckowner.HealthFact{
		AccountID: "acct-1", SystemAccountID: "sys-1", StatHour: "2026-08-28T10:00:00Z", RunID: "run-1",
		ProviderCode: "openai", Model: "gpt-5.6", Profile: "full", Level: "success",
		ObservedAt: time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC), Score: 92, Threshold: 70,
	}
}

func TestW11BReadNilAndBlankScopeArms(t *testing.T) {
	// nil reader：fail closed。
	var reader *Reader
	if _, _, err := reader.Read(context.Background(), "acct-1", "2026-08-28T10:00:00Z"); err == nil {
		t.Fatal("nil reader 必须报错")
	}
	// 空 statHour。
	built, err := New(&fakeSource{})
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := built.Read(context.Background(), "acct-1", "   "); err == nil || found {
		t.Fatalf("blank statHour found=%v err=%v", found, err)
	}
	if _, found, err := built.Read(context.Background(), "  ", ""); err == nil || found {
		t.Fatalf("blank both found=%v err=%v", found, err)
	}
	// trim 后的作用域匹配：前后空白仍能命中。
	source := &fakeSource{found: true, fact: factFixtureForW11B()}
	scoped, err := New(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := scoped.Read(context.Background(), "  acct-1  ", " "+source.fact.StatHour+" "); err != nil || !found {
		t.Fatalf("trimmed scope found=%v err=%v", found, err)
	}
	// 源读取错误透传。
	failing := &fakeSource{found: true, force: true, err: errors.New("w11b source down"), fact: factFixtureForW11B()}
	failingReader, err := New(failing)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := failingReader.Read(context.Background(), "acct-1", failing.fact.StatHour); err == nil {
		t.Fatal("源错误必须透传")
	}
}
