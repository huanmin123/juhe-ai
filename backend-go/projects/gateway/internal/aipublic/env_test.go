// Test fixtures for the aipublic slice: the recording operation-log sink and
// the test kernel. The PG schema lives in pgkit_test.go (pgtestkit 隔离库 +
// 生产对齐夹具 schema，T65 迁移后 SQLite 内存载具已退役).
package aipublic

import (
	"net/http"
	"sync"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

func kernelForTest() *kernel.Kernel {
	return kernel.New(kernel.Options{CompressionDisabled: true})
}

// recordingAIPublicSink captures the account write logs for assertions.
type recordingAIPublicSink struct {
	mutex   sync.Mutex
	Entries []authsys.OperationLogEntry
}

func (s *recordingAIPublicSink) Record(entry authsys.OperationLogEntry, _ *http.Request) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.Entries = append(s.Entries, entry)
}
