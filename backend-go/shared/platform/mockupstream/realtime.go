package mockupstream

// Realtime WebSocket scenario family for the M5b realtime chain, per
// docs/functions/媒体上游协议契约与Mock上游规格.md §4.4 and
// docs/functions/实时语音Realtime网关设计.md §7:
//
//   - GET /v1/realtime is the WS upgrade endpoint (exact method+path pair on
//     the engine whitelist). The model query parameter must be non-empty
//     (missing model answers a 400 JSON before any upgrade, mirroring the
//     upstream contract "wss://...?model=<model>").
//   - media_realtime_echo: upgrade succeeds; every client frame is echoed
//     back verbatim (same opcode, same bytes — the acceptance golden for the
//     "透传不重写" bridge contract). Two script parameters ride the upgrade
//     query string (the WS upgrade request has no body):
//       * usage_events=<n,m,...> — comma-separated 1-based client frame
//         ordinals; after receiving each listed frame the server proactively
//         sends one response.done-shaped event carrying response.usage
//         (input_tokens/output_tokens plus the *_token_details.audio_tokens
//         fields the gateway observer must parse);
//       * close_after=<n> — after the n-th received client frame the server
//         closes the session with a normal 1000 close.
//   - media_realtime_reject_upgrade: the upgrade request answers 403 JSON
//     (受理前失败, the retry-on-another-account arm).
//   - media_realtime_close_after_established: upgrade succeeds and the server
//     immediately closes 1000 (受理后断开, the no-retry arm).
//   - media_realtime_idle: upgrade succeeds then the server stays silent —
//     no echo, no events (the idle-timeout arm).
//
// Scenario selection reuses the X-Mock-Scenario / ?scenario= mechanism. Like
// the other media faces, an unknown scenario on the realtime endpoint
// defaults to that family's OK behavior (echo), and the generic status
// scenarios (status_429 etc.) still compose ahead of the family dispatch as
// pre-upgrade rejections.

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// Realtime scenario names (contract §4.4).
const (
	ScenarioMediaRealtimeEcho                  Scenario = "media_realtime_echo"
	ScenarioMediaRealtimeRejectUpgrade         Scenario = "media_realtime_reject_upgrade"
	ScenarioMediaRealtimeCloseAfterEstablished Scenario = "media_realtime_close_after_established"
	ScenarioMediaRealtimeIdle                  Scenario = "media_realtime_idle"
)

// realtimeUpgradePath is the exact realtime WS endpoint (whitelist pair is
// method-aware: GET only, mirroring the upstream wss contract).
const realtimeUpgradePath = "/v1/realtime"

// realtimeUpgrader has no origin check: the mock upstream accepts browser and
// server clients alike (the scenario under test is the wire behavior, not the
// origin policy).
var realtimeUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
}

// isRealtimeUpgradePath reports whether the path is the realtime WS endpoint.
func isRealtimeUpgradePath(path string) bool { return path == realtimeUpgradePath }

// acceptsRealtimeEndpoint matches GET /v1/realtime exactly.
func acceptsRealtimeEndpoint(method, path string) bool {
	return method == http.MethodGet && isRealtimeUpgradePath(path)
}

// serveRealtime handles the realtime WS face. It runs after the generic
// status scenarios (a status_429 still renders a pre-upgrade JSON rejection)
// and hijacks the connection for the four realtime scenarios.
func (m *Server) serveRealtime(w http.ResponseWriter, r *http.Request, scenario Scenario) {
	model := strings.TrimSpace(r.URL.Query().Get("model"))
	if model == "" {
		writeJSONStatus(w, http.StatusBadRequest, `{"error":{"message":"Missing required query parameter 'model'.","type":"invalid_request_error","param":"model","code":"missing_model"}}`)
		return
	}
	if scenario == ScenarioMediaRealtimeRejectUpgrade {
		writeJSONStatus(w, http.StatusForbidden, `{"error":{"message":"Realtime access denied for this account","type":"insufficient_quota"}}`)
		return
	}
	conn, err := realtimeUpgrader.Upgrade(w, r, nil)
	if err != nil {
		// The upgrade rejection has already been written by the upgrader.
		return
	}
	defer conn.Close()
	if scenario == ScenarioMediaRealtimeCloseAfterEstablished {
		writeRealtimeClose(conn, websocket.CloseNormalClosure, "established then close")
		return
	}
	m.serveRealtimeEcho(conn, r, scenario)
}

// serveRealtimeEcho runs the echo / idle read loop. idle stays silent (the
// client observes no frames until it gives up); every other scenario echoes
// client frames verbatim and applies the usage_events / close_after script
// parameters from the upgrade query.
func (m *Server) serveRealtimeEcho(conn *websocket.Conn, r *http.Request, scenario Scenario) {
	echo := scenario != ScenarioMediaRealtimeIdle
	usagePoints := parseRealtimeUsageEvents(r.URL.Query().Get("usage_events"))
	closeAfter := parseRealtimeCloseAfter(r.URL.Query().Get("close_after"))
	frames := 0
	for {
		messageType, payload, err := conn.ReadMessage()
		if err != nil {
			return // client closed / errored: session over
		}
		frames++
		if echo {
			if err := conn.WriteMessage(messageType, payload); err != nil {
				return
			}
		}
		if usagePoints[frames] {
			if err := conn.WriteMessage(websocket.TextMessage, []byte(realtimeUsageDoneEvent())); err != nil {
				return
			}
		}
		if closeAfter > 0 && frames >= closeAfter {
			writeRealtimeClose(conn, websocket.CloseNormalClosure, "close_after")
			return
		}
	}
}

// realtimeUsageDoneEvent is the deterministic response.done-shaped usage
// event (contract §4.4: the metering point the gateway observer parses from
// the event stream). Fixed values keep golden diffs stable; every emitted
// event carries the same usage so multi-point scripts sum predictably.
func realtimeUsageDoneEvent() string {
	return `{"type":"response.done","event_id":"event_mock_realtime_done","response":{"id":"resp_mock_realtime","status":"completed","usage":{"input_tokens":100,"input_token_details":{"text_tokens":20,"audio_tokens":80},"output_tokens":50,"output_token_details":{"text_tokens":10,"audio_tokens":40}}}}`
}

// writeRealtimeClose sends one close control frame with a short deadline and
// stops writing (best effort: a gone client is not an error here).
func writeRealtimeClose(conn *websocket.Conn, code int, text string) {
	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, text), time.Now().Add(time.Second))
}

// parseRealtimeUsageEvents parses the comma-separated 1-based frame ordinals
// of the usage_events script parameter into a lookup set ("2,5" → {2,5}).
// Unparseable tokens are skipped; the empty parameter yields no points.
func parseRealtimeUsageEvents(raw string) map[int]bool {
	points := map[int]bool{}
	for _, token := range strings.Split(raw, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		if value, err := strconv.Atoi(token); err == nil && value > 0 {
			points[value] = true
		}
	}
	return points
}

// parseRealtimeCloseAfter parses the close_after script parameter (positive
// integer frame count; anything else means "never close on frame count").
func parseRealtimeCloseAfter(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return 0
	}
	return value
}
