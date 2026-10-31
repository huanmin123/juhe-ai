package playground

// 归一 SSE 写出器（设计 §3.3：data: <json>\n\n 帧，type 为判别字段；与上游
// chat chunk 解耦）。帧头在首个事件写出时提交（校验失败走 JSON 错误面，不占
// SSE 通道）；带 5s 心跳注释防代理断流（chat 面心跳惯例）：ticker goroutine
// 周期写 `: ping\n\n` 注释帧并 Flush，注释帧仅在帧头提交后写出（不改变「帧头
// 首事件提交」契约，避免长派发首帧失败时 SSE 通道被心跳占位），流结束（含
// error/panic 路径）必须 Stop（runChatStream defer 承接）。

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// playgroundSSEHeartbeatInterval 是 SSE 心跳注释帧间隔（包内变量仅为测试注入
// 用；生产恒 5s）。
var playgroundSSEHeartbeatInterval = 5 * time.Second

// playgroundSSEWriter 是演练场 SSE 响应写出器（writeEvent 与心跳 goroutine
// 并发，mu 互斥；Stop 幂等且同步等待心跳退出）。
type playgroundSSEWriter struct {
	w        http.ResponseWriter
	flusher  http.Flusher
	prepared bool
	broken   bool

	mu       sync.Mutex
	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
}

// sseEventFrame 是归一 SSE 事件帧载荷（设计 §3.3 表：type 为判别字段；各键
// 按事件类型互斥出现，omitempty 收敛输出形状）。事件语义：
//
//	round {round} / delta {text} / tool_start {callId,name,args} /
//	tool_end {callId,name,ok,summary} / usage {promptTokens,completionTokens,
//	totalTokens} / done {rounds} / error {code,message}。
type sseEventFrame struct {
	Type             string `json:"type"`
	Round            int    `json:"round,omitempty"`
	Text             string `json:"text,omitempty"`
	CallID           string `json:"callId,omitempty"`
	Name             string `json:"name,omitempty"`
	Args             string `json:"args,omitempty"`
	OK               *bool  `json:"ok,omitempty"`
	Summary          string `json:"summary,omitempty"`
	PromptTokens     *int64 `json:"promptTokens,omitempty"`
	CompletionTokens *int64 `json:"completionTokens,omitempty"`
	TotalTokens      *int64 `json:"totalTokens,omitempty"`
	Rounds           *int   `json:"rounds,omitempty"`
	Code             string `json:"code,omitempty"`
	Message          string `json:"message,omitempty"`
}

// newPlaygroundSSEWriter 构造写出器并启动心跳 goroutine（构造不写头；首个事件
// 提交帧头）。流结束必须 Stop。
func newPlaygroundSSEWriter(w http.ResponseWriter) *playgroundSSEWriter {
	flusher, _ := w.(http.Flusher)
	s := &playgroundSSEWriter{
		w:       w,
		flusher: flusher,
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	go func() {
		defer close(s.done)
		s.pumpHeartbeat()
	}()
	return s
}

// pumpHeartbeat 周期写 : ping 注释帧（SSE 注释，前端 consumePlaygroundSseFrame
// 跳过 `:` 帧无感），防流内停顿（如工具执行/上游长间隔）被代理断流。
func (s *playgroundSSEWriter) pumpHeartbeat() {
	ticker := time.NewTicker(playgroundSSEHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.writePing()
		}
	}
}

// writePing 写一帧心跳注释（帧头未提交前跳过——首事件提交契约优先于保活）。
func (s *playgroundSSEWriter) writePing() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.broken || !s.prepared {
		return
	}
	s.prepare()
	if _, err := s.w.Write([]byte(": ping\n\n")); err != nil {
		s.broken = true
		return
	}
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

// Stop 停止心跳 goroutine 并等待其退出（幂等；并发调用安全）。
func (s *playgroundSSEWriter) Stop() {
	s.stopOnce.Do(func() {
		close(s.stop)
		<-s.done
	})
}

// prepare 提交 SSE 帧头（chat prepareSSEResponse 同款头集）。调用方须持 mu。
func (s *playgroundSSEWriter) prepare() {
	if s.prepared {
		return
	}
	s.prepared = true
	header := s.w.Header()
	header.Set("Content-Type", "text/event-stream; charset=utf-8")
	header.Set("Cache-Control", "no-store")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	s.w.WriteHeader(http.StatusOK)
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

// writeEvent 写出一帧归一事件（载荷序列化失败按 broken 收敛；演练场事件均为
// 平面结构，恒可序列化——分支仅为防御 json.Marshal 的泛化签名）。
func (s *playgroundSSEWriter) writeEvent(frame sseEventFrame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.broken {
		return
	}
	s.prepare()
	payload, err := json.Marshal(frame)
	if err != nil {
		s.broken = true
		return
	}
	if _, err := s.w.Write(append(append([]byte("data: "), payload...), '\n', '\n')); err != nil {
		s.broken = true
		return
	}
	if s.flusher != nil {
		s.flusher.Flush()
	}
}
