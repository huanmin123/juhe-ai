package mockupstream

// Realtime WS face tests (contract §4.4 / Realtime design §7): a real
// gorilla/websocket client against the httptest upstream for every scenario
// arm — byte-exact echo, usage events, close behaviors, upgrade rejection
// and idle silence.

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// dialRealtime opens a WS client against the mock realtime endpoint with the
// given query string (already scenario/model/script parameters) and an
// optional single extra header pair (name, value).
func dialRealtime(t *testing.T, m *Server, query string, header ...string) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(m.URL, "http") + realtimeUpgradePath
	if query != "" {
		url += "?" + query
	}
	var extraHeader http.Header
	if len(header) >= 2 {
		extraHeader = http.Header{header[0]: []string{header[1]}}
	}
	conn, _, err := websocket.DefaultDialer.Dial(url, extraHeader)
	if err != nil {
		t.Fatalf("dial realtime (%s): %v", query, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestRealtimeEchoReplaysClientTextFramesVerbatim(t *testing.T) {
	m := New()
	defer m.Close()
	conn := dialRealtime(t, m, "scenario=media_realtime_echo&model=gpt-realtime")

	frames := []string{
		`{"type":"session.update","session":{"model":"gpt-realtime","voice":"alloy"}}`,
		`{"type":"input_audio_buffer.append","audio":"Base64字节-片段☃"}`,
		`{"type":"response.create"}`,
	}
	for _, frame := range frames {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
			t.Fatalf("write frame: %v", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		messageType, payload, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read echo: %v", err)
		}
		if messageType != websocket.TextMessage {
			t.Fatalf("echo opcode = %d want text", messageType)
		}
		if string(payload) != frame {
			t.Fatalf("echo payload = %q want byte-identical %q", payload, frame)
		}
	}
	// The scenario header selects the same scenario as the query parameter.
	headerConn := dialRealtime(t, m, "model=gpt-realtime", "X-Mock-Scenario", string(ScenarioMediaRealtimeEcho))
	if err := headerConn.WriteMessage(websocket.TextMessage, []byte(`{"type":"ping"}`)); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	_ = headerConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, payload, err := headerConn.ReadMessage()
	if err != nil || string(payload) != `{"type":"ping"}` {
		t.Fatalf("header-selected echo = %q, %v", payload, err)
	}
	// The upgrade request is recorded like every other upstream request.
	requests := m.Requests()
	if len(requests) == 0 || requests[0].Method != http.MethodGet || requests[0].Path != realtimeUpgradePath {
		t.Fatalf("recorded requests = %+v want GET %s", requests, realtimeUpgradePath)
	}
}

func TestRealtimeEchoEmitsUsageDoneEventsAtScriptedPoints(t *testing.T) {
	m := New()
	defer m.Close()
	conn := dialRealtime(t, m, "scenario=media_realtime_echo&model=gpt-realtime&usage_events=1,3")

	read := func() (string, string) {
		t.Helper()
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		messageType, payload, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if messageType != websocket.TextMessage {
			t.Fatalf("opcode = %d want text", messageType)
		}
		var parsed struct {
			Type     string `json:"type"`
			Response struct {
				Usage struct {
					InputTokens       int `json:"input_tokens"`
					OutputTokens      int `json:"output_tokens"`
					InputTokenDetails struct {
						AudioTokens int `json:"audio_tokens"`
					} `json:"input_token_details"`
					OutputTokenDetails struct {
						AudioTokens int `json:"audio_tokens"`
					} `json:"output_token_details"`
				} `json:"usage"`
			} `json:"response"`
		}
		if err := json.Unmarshal(payload, &parsed); err != nil {
			t.Fatalf("usage event json: %v (%s)", err, payload)
		}
		return parsed.Type, string(payload)
	}

	// Frame 1: echo then one response.done with the full usage shape.
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","modalities":["text"]}`)); err != nil {
		t.Fatalf("write frame 1: %v", err)
	}
	if _, echoPayload := read(); echoPayload != `{"type":"response.create","modalities":["text"]}` {
		t.Fatalf("frame 1 echo = %q", echoPayload)
	}
	eventType, usagePayload := read()
	if eventType != "response.done" {
		t.Fatalf("usage event type = %s want response.done", eventType)
	}
	var envelope struct {
		Response struct {
			Usage struct {
				InputTokens       int `json:"input_tokens"`
				OutputTokens      int `json:"output_tokens"`
				InputTokenDetails struct {
					AudioTokens int `json:"audio_tokens"`
				} `json:"input_token_details"`
				OutputTokenDetails struct {
					AudioTokens int `json:"audio_tokens"`
				} `json:"output_token_details"`
			} `json:"usage"`
		} `json:"response"`
	}
	if err := json.Unmarshal([]byte(usagePayload), &envelope); err != nil {
		t.Fatalf("usage envelope json: %v", err)
	}
	usage := envelope.Response.Usage
	if usage.InputTokens != 100 || usage.OutputTokens != 50 ||
		usage.InputTokenDetails.AudioTokens != 80 || usage.OutputTokenDetails.AudioTokens != 40 {
		t.Fatalf("usage = %+v want input 100 (audio 80) / output 50 (audio 40)", usage)
	}

	// Frame 2 carries no usage point: echo only.
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"input_audio_buffer.append","audio":"AA=="}`)); err != nil {
		t.Fatalf("write frame 2: %v", err)
	}
	if _, payload := read(); payload != `{"type":"input_audio_buffer.append","audio":"AA=="}` {
		t.Fatalf("frame 2 echo = %q", payload)
	}

	// Frame 3 triggers the second usage point.
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create"}`)); err != nil {
		t.Fatalf("write frame 3: %v", err)
	}
	read() // echo
	if eventType, _ := read(); eventType != "response.done" {
		t.Fatalf("second usage point type = %s want response.done", eventType)
	}
}

func TestRealtimeEchoClosesAfterScriptedFrameCount(t *testing.T) {
	m := New()
	defer m.Close()
	conn := dialRealtime(t, m, "scenario=media_realtime_echo&model=gpt-realtime&close_after=2")

	for i := 0; i < 2; i++ {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"tick"}`)); err != nil {
			t.Fatalf("write frame %d: %v", i+1, err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, _, err := conn.ReadMessage(); err != nil {
			t.Fatalf("read echo %d: %v", i+1, err)
		}
	}
	// After the second frame the server closes 1000: the next read surfaces
	// the close error carrying the code.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err := conn.ReadMessage()
	closeError, ok := err.(*websocket.CloseError)
	if !ok {
		t.Fatalf("read after close_after = %v want *websocket.CloseError", err)
	}
	if closeError.Code != websocket.CloseNormalClosure {
		t.Fatalf("close code = %d want 1000", closeError.Code)
	}
}

func TestRealtimeRejectUpgradeAnswers403JSON(t *testing.T) {
	m := New()
	defer m.Close()
	url := "ws" + strings.TrimPrefix(m.URL, "http") + realtimeUpgradePath + "?scenario=media_realtime_reject_upgrade&model=gpt-realtime"
	conn, httpResp, err := websocket.DefaultDialer.Dial(url, nil)
	if err == nil {
		_ = conn.Close()
		t.Fatal("reject_upgrade dial must fail")
	}
	if httpResp == nil {
		t.Fatalf("dial error = %v want handshake response", err)
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusForbidden {
		t.Fatalf("reject status = %d want 403", httpResp.StatusCode)
	}
	body, _ := io.ReadAll(httpResp.Body)
	if !strings.Contains(string(body), `"type":"insufficient_quota"`) {
		t.Fatalf("reject body = %s want insufficient_quota JSON", body)
	}
}

func TestRealtimeCloseAfterEstablishedCloses1000(t *testing.T) {
	m := New()
	defer m.Close()
	conn := dialRealtime(t, m, "scenario=media_realtime_close_after_established&model=gpt-realtime")
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err := conn.ReadMessage()
	closeError, ok := err.(*websocket.CloseError)
	if !ok {
		t.Fatalf("first read = %v want *websocket.CloseError", err)
	}
	if closeError.Code != websocket.CloseNormalClosure {
		t.Fatalf("close code = %d want 1000", closeError.Code)
	}
}

func TestRealtimeIdleStaysSilent(t *testing.T) {
	m := New()
	defer m.Close()
	conn := dialRealtime(t, m, "scenario=media_realtime_idle&model=gpt-realtime")
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.update"}`)); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	_, _, err := conn.ReadMessage()
	if !isTimeout(err) {
		t.Fatalf("idle read = %v want read deadline timeout (no echo, no events)", err)
	}
}

func TestRealtimeRequiresModelQueryParameter(t *testing.T) {
	m := New()
	defer m.Close()
	url := "ws" + strings.TrimPrefix(m.URL, "http") + realtimeUpgradePath + "?scenario=media_realtime_echo"
	conn, httpResp, err := websocket.DefaultDialer.Dial(url, nil)
	if err == nil {
		_ = conn.Close()
		t.Fatal("dial without model must fail")
	}
	if httpResp == nil {
		t.Fatalf("dial error = %v want handshake response", err)
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("dial status = %d want 400", httpResp.StatusCode)
	}
	body, _ := io.ReadAll(httpResp.Body)
	if !strings.Contains(string(body), "missing_model") {
		t.Fatalf("missing model body = %s", body)
	}
}

func TestRealtimeEndpointWhitelistIsMethodAndPathExact(t *testing.T) {
	m := New()
	defer m.Close()
	// POST on the realtime path is not the upgrade shape: 404 like every
	// non-whitelisted pair.
	resp, err := http.Post(m.URL+realtimeUpgradePath+"?model=gpt-realtime", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("post realtime: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("POST /v1/realtime status = %d want 404", resp.StatusCode)
	}
	// A sub-path is not the upgrade endpoint either.
	resp2, err := http.Get(m.URL + realtimeUpgradePath + "/extra?model=gpt-realtime")
	if err != nil {
		t.Fatalf("get realtime subpath: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /v1/realtime/extra status = %d want 404", resp2.StatusCode)
	}
	// Generic status scenarios compose as pre-upgrade rejections.
	url := "ws" + strings.TrimPrefix(m.URL, "http") + realtimeUpgradePath + "?scenario=status_429&model=gpt-realtime"
	conn, httpResp, err := websocket.DefaultDialer.Dial(url, nil)
	if err == nil {
		_ = conn.Close()
		t.Fatal("status_429 dial must fail")
	}
	if httpResp == nil || httpResp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status_429 dial = (%v, %v) want handshake 429", err, httpResp)
	}
}

// isTimeout reports a net timeout error without importing net everywhere.
func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	if netErr, ok := err.(interface{ Timeout() bool }); ok {
		return netErr.Timeout()
	}
	return strings.Contains(err.Error(), "i/o timeout")
}
