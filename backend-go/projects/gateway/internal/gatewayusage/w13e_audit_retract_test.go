package gatewayusage

// w13e 波次测试：W13 幻影信封撤回。流式捕获在 StartAttempt 时写入
// in_progress 信封，请求中止路径（settleDispatchError 的 UpstreamRequestAborted
// 分支不 finalize，请求 finally 调 Cancel）会留下永久 in_progress 空壳。
// Cancel 现在通过可选 AuditLogRetractor 撤回该信封；已 finalize、非流式
// （从未写信封）或不具备撤回能力的 dispatcher 均不得触发撤回。

import (
	"sync"
	"testing"
)

// recordingRetractDispatcher 在 recordingAuditDispatcher 之上补充撤回记录，
// 用于断言 Cancel 是否发出了信封撤回。
type recordingRetractDispatcher struct {
	*recordingAuditDispatcher

	mu         sync.Mutex
	retractIDs []string
}

func (r *recordingRetractDispatcher) DispatchAuditLogRetraction(_ Ctx, auditLogID string) {
	r.mu.Lock()
	r.retractIDs = append(r.retractIDs, auditLogID)
	r.mu.Unlock()
}

func (r *recordingRetractDispatcher) retractions() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.retractIDs...)
}

func w13eCapture(dispatcher AuditDispatcher, stream bool) *AuditCaptureContext {
	ResetActiveAuditCaptureCountForTest()
	return captureInput(nil, func(input *AuditCaptureInput) {
		input.Dispatcher = dispatcher
		input.Stream = stream
	})
}

func TestW13ECancelRetractsInProgressEnvelope(t *testing.T) {
	dispatcher := &recordingRetractDispatcher{recordingAuditDispatcher: &recordingAuditDispatcher{}}
	capture := w13eCapture(dispatcher, true)
	capture.StartAttempt(StartAttemptInput{Account: testAccount(), AttemptIndex: 0, UpstreamURL: "https://upstream", Method: "POST"})
	envelopes := dispatcher.all()
	if len(envelopes) != 1 || envelopes[0].LifecycleStatus != AuditLifecycleInProgress {
		t.Fatalf("StartAttempt 必须先写 in_progress 信封: %+v", envelopes)
	}
	if got := dispatcher.retractions(); len(got) != 0 {
		t.Fatalf("撤回只应发生在 Cancel: %v", got)
	}
	capture.Cancel()
	retractions := dispatcher.retractions()
	if len(retractions) != 1 || retractions[0] != envelopes[0].ID {
		t.Fatalf("Cancel 必须按信封 id 撤回一次: %v", retractions)
	}
	if capture.AuditLogID() != envelopes[0].ID {
		t.Fatalf("撤回 id 必须等于捕获 id: %q vs %q", capture.AuditLogID(), envelopes[0].ID)
	}
}

func TestW13ECancelAfterFinalizeDoesNotRetract(t *testing.T) {
	dispatcher := &recordingRetractDispatcher{recordingAuditDispatcher: &recordingAuditDispatcher{}}
	capture := w13eCapture(dispatcher, true)
	capture.StartAttempt(StartAttemptInput{Account: testAccount(), AttemptIndex: 0, UpstreamURL: "https://upstream", Method: "POST"})
	if len(dispatcher.all()) != 1 {
		t.Fatal("信封必须已写入")
	}
	capture.Finalize(FinalizeAuditInput{Outcome: AuditOutcomeGatewaySucceeded, Success: true, StatusCode: intPointer(200)})
	finalized := dispatcher.all()
	if len(finalized) != 2 || finalized[1].LifecycleStatus == AuditLifecycleInProgress {
		t.Fatalf("Finalize 必须产出终态审计: %+v", finalized)
	}
	// finalize 后的 finally-path Cancel 是 no-op，不得撤回已终态的行。
	capture.Cancel()
	if got := dispatcher.retractions(); len(got) != 0 {
		t.Fatalf("已 finalize 的捕获不得撤回: %v", got)
	}
}

func TestW13ENonStreamCancelDoesNotRetract(t *testing.T) {
	dispatcher := &recordingRetractDispatcher{recordingAuditDispatcher: &recordingAuditDispatcher{}}
	capture := w13eCapture(dispatcher, false)
	capture.StartAttempt(StartAttemptInput{Account: testAccount(), AttemptIndex: 0, UpstreamURL: "https://upstream", Method: "POST"})
	// 非流式从不写 in_progress 信封。
	if got := dispatcher.all(); len(got) != 0 {
		t.Fatalf("非流式不得写信封: %+v", got)
	}
	capture.Cancel()
	if got := dispatcher.retractions(); len(got) != 0 {
		t.Fatalf("无信封不得撤回: %v", got)
	}
}

func TestW13ECancelWithPlainDispatcherIsNoop(t *testing.T) {
	// 不实现 AuditLogRetractor 的 dispatcher 只收到信封，Cancel 不得 panic。
	dispatcher := &recordingAuditDispatcher{}
	capture := w13eCapture(dispatcher, true)
	capture.StartAttempt(StartAttemptInput{Account: testAccount(), AttemptIndex: 0, UpstreamURL: "https://upstream", Method: "POST"})
	capture.Cancel()
	if len(dispatcher.all()) != 1 {
		t.Fatalf("只应有信封一次派发: %+v", dispatcher.all())
	}
}

func TestW13ECancelIsIdempotent(t *testing.T) {
	dispatcher := &recordingRetractDispatcher{recordingAuditDispatcher: &recordingAuditDispatcher{}}
	capture := w13eCapture(dispatcher, true)
	capture.StartAttempt(StartAttemptInput{Account: testAccount(), AttemptIndex: 0, UpstreamURL: "https://upstream", Method: "POST"})
	capture.Cancel()
	capture.Cancel()
	if got := dispatcher.retractions(); len(got) != 1 {
		t.Fatalf("重复 Cancel 只撤回一次: %v", got)
	}
}
