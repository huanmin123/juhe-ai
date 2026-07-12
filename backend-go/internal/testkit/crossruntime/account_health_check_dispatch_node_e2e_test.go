//go:build integration

package crossruntime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/platform/accounthealthcheckdispatch"
)

const (
	accountHealthCheckDispatchNodeE2ESecretEnv = "JUHE_AI_ACCOUNT_HEALTH_CHECK_DISPATCH_E2E_SECRET"
	requireNodeGoBridgeEnv                     = "JUHE_AI_REQUIRE_NODE_GO_BRIDGE"
	requireIntegrationEnv                      = "JUHE_AI_REQUIRE_INTEGRATION"
	nodeE2EPrefix                              = "JUHE_AI_E2E "
	nodeE2EScannerMaxToken                     = 64 * 1024
	nodeE2EOutputTailLimit                     = 32 * 1024
	nodeE2EReadyTimeout                        = 10 * time.Second
	nodeE2EEventTimeout                        = 10 * time.Second
	nodeE2EGracefulShutdownTimeout             = 3 * time.Second
	nodeE2ETotalTimeout                        = 15 * time.Second
)

type accountHealthCheckDispatchNodeE2ECall struct {
	AccountID string `json:"accountId"`
	Reason    string `json:"reason"`
}

type accountHealthCheckDispatchNodeE2EEvent struct {
	Event    string
	BaseURL  string
	Calls    []accountHealthCheckDispatchNodeE2ECall
	Expected *accountHealthCheckDispatchNodeE2ECall
	Actual   *accountHealthCheckDispatchNodeE2ECall
}

type accountHealthCheckDispatchNodeE2ERecord struct {
	event accountHealthCheckDispatchNodeE2EEvent
	err   error
}

type accountHealthCheckDispatchNodeE2EProcess struct {
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	records    chan accountHealthCheckDispatchNodeE2ERecord
	stdoutDone chan struct{}
	stderrDone chan struct{}
	exited     chan struct{}
	stdoutTail *accountHealthCheckDispatchNodeE2EBoundedTail
	stderrTail *accountHealthCheckDispatchNodeE2EBoundedTail

	shutdownOnce sync.Once
	shutdownErr  error

	waitMu  sync.Mutex
	waitErr error

	stdoutErrMu sync.Mutex
	stdoutErr   error
}

type accountHealthCheckDispatchNodeE2EBoundedTail struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func TestAccountHealthCheckDispatchNodeE2EParseBool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		raw     string
		want    bool
		wantErr bool
	}{
		{raw: "", want: false},
		{raw: "off", want: false},
		{raw: "1", want: true},
		{raw: " TRUE ", want: true},
		{raw: "required", wantErr: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.raw, func(t *testing.T) {
			t.Parallel()

			got, err := accountHealthCheckDispatchNodeE2EParseBool(test.raw)
			if test.wantErr {
				if err == nil {
					t.Fatal("parse bool error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parse bool error = %v", err)
			}
			if got != test.want {
				t.Fatalf("parse bool = %t, want %t", got, test.want)
			}
		})
	}
}

func TestAccountHealthCheckDispatchNodeE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), nodeE2ETotalTimeout)
	defer cancel()

	nodePath, backendDir, helperPath := accountHealthCheckDispatchNodeE2EPrerequisites(t, ctx)

	const secret = "account-health-check-dispatch-node-go-e2e-secret"
	process := startAccountHealthCheckDispatchNodeE2EProcess(
		t,
		ctx,
		nodePath,
		backendDir,
		helperPath,
		secret,
	)
	t.Cleanup(func() {
		process.cleanup(t)
	})

	ready := process.awaitEvent(t, ctx, nodeE2EReadyTimeout, "ready")
	client, err := accounthealthcheckdispatch.NewClient(ready.BaseURL, secret)
	if err != nil {
		t.Fatalf("create account health check dispatch client: %v", err)
	}

	expectedCalls := []accountHealthCheckDispatchNodeE2ECall{
		{AccountID: "e2e-activation", Reason: "activation"},
		{AccountID: "e2e-configuration", Reason: "configuration"},
	}
	for _, call := range expectedCalls {
		if err := client.Dispatch(ctx, call.AccountID, call.Reason); err != nil {
			t.Fatalf(
				"dispatch account %q with reason %q: %v%s",
				call.AccountID,
				call.Reason,
				err,
				process.diagnostics(),
			)
		}
	}

	confirmed := process.awaitEvent(t, ctx, nodeE2EEventTimeout, "confirmed")
	if !reflect.DeepEqual(confirmed.Calls, expectedCalls) {
		t.Fatalf(
			"confirmed calls = %#v, want %#v%s",
			confirmed.Calls,
			expectedCalls,
			process.diagnostics(),
		)
	}

	if err := process.requestShutdown(); err != nil {
		t.Fatalf("send Node helper shutdown command: %v%s", err, process.diagnostics())
	}
	process.awaitEvent(t, ctx, nodeE2EEventTimeout, "stopped")

	if err := process.waitForExit(ctx); err != nil {
		t.Fatalf("wait for Node helper exit: %v%s", err, process.diagnostics())
	}
	if err := process.waitForReaders(ctx); err != nil {
		t.Fatalf("wait for Node helper output readers: %v%s", err, process.diagnostics())
	}
	if err := process.verifyStdoutComplete(); err != nil {
		t.Fatalf("verify Node helper stdout protocol: %v%s", err, process.diagnostics())
	}
}

func accountHealthCheckDispatchNodeE2EPrerequisites(
	t *testing.T,
	ctx context.Context,
) (string, string, string) {
	t.Helper()

	nodePath, err := exec.LookPath("node")
	if err != nil {
		accountHealthCheckDispatchNodeE2EDependencyUnavailable(t, "node executable not found: %v", err)
	}
	nodePath, err = filepath.Abs(nodePath)
	if err != nil {
		t.Fatalf("resolve absolute node executable path: %v", err)
	}

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current integration test path")
	}
	repositoryDir := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", "..", ".."))
	backendDir := filepath.Join(repositoryDir, "backend")
	helperPath := filepath.Join(
		backendDir,
		"src",
		"scripts",
		"regression",
		"account-health-check-dispatch-node-e2e-server.ts",
	)
	helperPath, err = filepath.Abs(helperPath)
	if err != nil {
		t.Fatalf("resolve absolute Node helper path: %v", err)
	}
	if info, statErr := os.Stat(helperPath); statErr != nil {
		t.Fatalf("stat Node helper %q: %v", helperPath, statErr)
	} else if info.IsDir() {
		t.Fatalf("Node helper path %q is a directory", helperPath)
	}

	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	probe := exec.CommandContext(probeCtx, nodePath, "--import", "tsx", "--eval", "")
	probe.Dir = backendDir
	probeOutput, err := probe.CombinedOutput()
	if err != nil {
		accountHealthCheckDispatchNodeE2EDependencyUnavailable(
			t,
			"tsx dependency is unavailable from %q: %v; output: %s",
			backendDir,
			err,
			accountHealthCheckDispatchNodeE2EOutputTail(probeOutput),
		)
	}

	return nodePath, backendDir, helperPath
}

func accountHealthCheckDispatchNodeE2EDependencyUnavailable(
	t *testing.T,
	format string,
	args ...any,
) {
	t.Helper()

	message := fmt.Sprintf(format, args...)
	required, err := accountHealthCheckDispatchNodeE2ERequired()
	if err != nil {
		t.Fatal(err)
	}
	if required {
		t.Fatalf(
			"%s or %s requires the Node-Go bridge, but its dependency is unavailable: %s",
			requireIntegrationEnv,
			requireNodeGoBridgeEnv,
			message,
		)
	}
	t.Skipf("skip Node-Go bridge E2E because dependency is unavailable: %s", message)
}

func accountHealthCheckDispatchNodeE2ERequired() (bool, error) {
	for _, environmentName := range []string{requireIntegrationEnv, requireNodeGoBridgeEnv} {
		enabled, err := accountHealthCheckDispatchNodeE2EParseBool(os.Getenv(environmentName))
		if err != nil {
			return false, fmt.Errorf("%s 配置无效: %w", environmentName, err)
		}
		if enabled {
			return true, nil
		}
	}
	return false, nil
}

func accountHealthCheckDispatchNodeE2EParseBool(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "0", "false", "no", "off":
		return false, nil
	case "1", "true", "yes", "on":
		return true, nil
	default:
		return false, errors.New("只接受 1/0、true/false、yes/no 或 on/off")
	}
}

func startAccountHealthCheckDispatchNodeE2EProcess(
	t *testing.T,
	ctx context.Context,
	nodePath string,
	backendDir string,
	helperPath string,
	secret string,
) *accountHealthCheckDispatchNodeE2EProcess {
	t.Helper()

	cmd := exec.CommandContext(ctx, nodePath, "--import", "tsx", helperPath)
	cmd.Dir = backendDir
	cmd.Env = append(os.Environ(), accountHealthCheckDispatchNodeE2ESecretEnv+"="+secret)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("open Node helper stdin: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		t.Fatalf("open Node helper stdout: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		t.Fatalf("open Node helper stderr: %v", err)
	}

	process := &accountHealthCheckDispatchNodeE2EProcess{
		cmd:        cmd,
		stdin:      stdin,
		records:    make(chan accountHealthCheckDispatchNodeE2ERecord, 16),
		stdoutDone: make(chan struct{}),
		stderrDone: make(chan struct{}),
		exited:     make(chan struct{}),
		stdoutTail: &accountHealthCheckDispatchNodeE2EBoundedTail{limit: nodeE2EOutputTailLimit},
		stderrTail: &accountHealthCheckDispatchNodeE2EBoundedTail{limit: nodeE2EOutputTailLimit},
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		t.Fatalf("start Node helper: %v", err)
	}

	go process.scanStdout(stdout)
	go process.scanStderr(stderr)
	go func() {
		<-process.stdoutDone
		<-process.stderrDone
		err := cmd.Wait()
		process.waitMu.Lock()
		process.waitErr = err
		process.waitMu.Unlock()
		close(process.exited)
	}()

	return process
}

func (process *accountHealthCheckDispatchNodeE2EProcess) scanStdout(reader io.Reader) {
	defer close(process.stdoutDone)
	defer close(process.records)

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4*1024), nodeE2EScannerMaxToken)
	for scanner.Scan() {
		line := scanner.Text()
		process.stdoutTail.appendLine(line)
		if !strings.HasPrefix(line, nodeE2EPrefix) {
			process.publishRecord(accountHealthCheckDispatchNodeE2ERecord{
				err: fmt.Errorf("unexpected stdout line %q", line),
			})
			continue
		}

		event, err := decodeAccountHealthCheckDispatchNodeE2EEvent(
			strings.TrimPrefix(line, nodeE2EPrefix),
		)
		process.publishRecord(accountHealthCheckDispatchNodeE2ERecord{
			event: event,
			err:   err,
		})
	}
	if err := scanner.Err(); err != nil {
		process.publishRecord(accountHealthCheckDispatchNodeE2ERecord{
			err: fmt.Errorf("scan stdout: %w", err),
		})
	}
}

func (process *accountHealthCheckDispatchNodeE2EProcess) scanStderr(reader io.Reader) {
	defer close(process.stderrDone)

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4*1024), nodeE2EScannerMaxToken)
	for scanner.Scan() {
		process.stderrTail.appendLine(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		process.stderrTail.appendLine("stderr scanner error: " + err.Error())
	}
}

func (process *accountHealthCheckDispatchNodeE2EProcess) publishRecord(
	record accountHealthCheckDispatchNodeE2ERecord,
) {
	select {
	case process.records <- record:
	default:
		process.stdoutErrMu.Lock()
		if process.stdoutErr == nil {
			process.stdoutErr = errors.New("stdout event queue exceeded its fixed capacity")
		}
		process.stdoutErrMu.Unlock()
	}
}

func decodeAccountHealthCheckDispatchNodeE2EEvent(
	raw string,
) (accountHealthCheckDispatchNodeE2EEvent, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return accountHealthCheckDispatchNodeE2EEvent{}, fmt.Errorf("decode event JSON: %w", err)
	}

	eventRaw, ok := fields["event"]
	if !ok {
		return accountHealthCheckDispatchNodeE2EEvent{}, errors.New("event JSON is missing event")
	}
	var eventName string
	if err := json.Unmarshal(eventRaw, &eventName); err != nil || eventName == "" {
		return accountHealthCheckDispatchNodeE2EEvent{}, errors.New("event JSON has invalid event")
	}

	event := accountHealthCheckDispatchNodeE2EEvent{Event: eventName}
	switch eventName {
	case "ready":
		if err := requireAccountHealthCheckDispatchNodeE2EFields(fields, "event", "baseUrl"); err != nil {
			return accountHealthCheckDispatchNodeE2EEvent{}, err
		}
		if err := json.Unmarshal(fields["baseUrl"], &event.BaseURL); err != nil || event.BaseURL == "" {
			return accountHealthCheckDispatchNodeE2EEvent{}, errors.New("ready event has invalid baseUrl")
		}
	case "confirmed":
		if err := requireAccountHealthCheckDispatchNodeE2EFields(fields, "event", "calls"); err != nil {
			return accountHealthCheckDispatchNodeE2EEvent{}, err
		}
		decoder := json.NewDecoder(bytes.NewReader(fields["calls"]))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&event.Calls); err != nil {
			return accountHealthCheckDispatchNodeE2EEvent{}, fmt.Errorf("confirmed event has invalid calls: %w", err)
		}
		if event.Calls == nil {
			return accountHealthCheckDispatchNodeE2EEvent{}, errors.New("confirmed event has invalid calls")
		}
	case "stopped":
		if err := requireAccountHealthCheckDispatchNodeE2EFields(fields, "event"); err != nil {
			return accountHealthCheckDispatchNodeE2EEvent{}, err
		}
	case "rejected":
		if err := requireAccountHealthCheckDispatchNodeE2EFields(fields, "event", "expected", "actual"); err != nil {
			return accountHealthCheckDispatchNodeE2EEvent{}, err
		}
		if string(fields["expected"]) != "null" {
			event.Expected = &accountHealthCheckDispatchNodeE2ECall{}
			if err := json.Unmarshal(fields["expected"], event.Expected); err != nil {
				return accountHealthCheckDispatchNodeE2EEvent{}, fmt.Errorf("rejected event has invalid expected call: %w", err)
			}
		}
		event.Actual = &accountHealthCheckDispatchNodeE2ECall{}
		if err := json.Unmarshal(fields["actual"], event.Actual); err != nil {
			return accountHealthCheckDispatchNodeE2EEvent{}, fmt.Errorf("rejected event has invalid actual call: %w", err)
		}
	default:
		return accountHealthCheckDispatchNodeE2EEvent{}, fmt.Errorf("unexpected event %q", eventName)
	}
	return event, nil
}

func requireAccountHealthCheckDispatchNodeE2EFields(
	fields map[string]json.RawMessage,
	required ...string,
) error {
	if len(fields) != len(required) {
		return fmt.Errorf("event fields = %v, want exactly %v", accountHealthCheckDispatchNodeE2EFieldNames(fields), required)
	}
	for _, name := range required {
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("event is missing field %q", name)
		}
	}
	return nil
}

func accountHealthCheckDispatchNodeE2EFieldNames(fields map[string]json.RawMessage) []string {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	return names
}

func (process *accountHealthCheckDispatchNodeE2EProcess) awaitEvent(
	t *testing.T,
	ctx context.Context,
	timeout time.Duration,
	want string,
) accountHealthCheckDispatchNodeE2EEvent {
	t.Helper()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case record, ok := <-process.records:
		if !ok {
			t.Fatalf("Node helper stdout closed while waiting for %q%s", want, process.diagnostics())
		}
		if record.err != nil {
			t.Fatalf("Node helper stdout protocol error while waiting for %q: %v%s", want, record.err, process.diagnostics())
		}
		if record.event.Event != want {
			t.Fatalf("Node helper event = %q, want %q%s", record.event.Event, want, process.diagnostics())
		}
		return record.event
	case <-timer.C:
		t.Fatalf("timed out after %s waiting for Node helper event %q%s", timeout, want, process.diagnostics())
	case <-ctx.Done():
		t.Fatalf("Node helper E2E deadline reached while waiting for %q: %v%s", want, ctx.Err(), process.diagnostics())
	}
	return accountHealthCheckDispatchNodeE2EEvent{}
}

func (process *accountHealthCheckDispatchNodeE2EProcess) requestShutdown() error {
	process.shutdownOnce.Do(func() {
		_, writeErr := io.WriteString(process.stdin, "shutdown\n")
		closeErr := process.stdin.Close()
		process.shutdownErr = errors.Join(writeErr, closeErr)
	})
	return process.shutdownErr
}

func (process *accountHealthCheckDispatchNodeE2EProcess) waitForExit(ctx context.Context) error {
	select {
	case <-process.exited:
		process.waitMu.Lock()
		defer process.waitMu.Unlock()
		return process.waitErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (process *accountHealthCheckDispatchNodeE2EProcess) waitForReaders(ctx context.Context) error {
	for name, done := range map[string]<-chan struct{}{
		"stdout": process.stdoutDone,
		"stderr": process.stderrDone,
	} {
		select {
		case <-done:
		case <-ctx.Done():
			return fmt.Errorf("wait for %s reader: %w", name, ctx.Err())
		}
	}
	return nil
}

func (process *accountHealthCheckDispatchNodeE2EProcess) verifyStdoutComplete() error {
	process.stdoutErrMu.Lock()
	stdoutErr := process.stdoutErr
	process.stdoutErrMu.Unlock()
	if stdoutErr != nil {
		return stdoutErr
	}
	for record := range process.records {
		if record.err != nil {
			return record.err
		}
		return fmt.Errorf("unexpected extra event %q after stopped", record.event.Event)
	}
	return nil
}

func (process *accountHealthCheckDispatchNodeE2EProcess) cleanup(t *testing.T) {
	t.Helper()

	select {
	case <-process.exited:
		_ = process.stdin.Close()
	default:
		if err := process.requestShutdown(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Logf("cleanup: send Node helper shutdown command: %v", err)
		}
	}

	timer := time.NewTimer(nodeE2EGracefulShutdownTimeout)
	defer timer.Stop()
	select {
	case <-process.exited:
	case <-timer.C:
		if err := process.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Errorf("cleanup: kill Node helper after %s: %v", nodeE2EGracefulShutdownTimeout, err)
		}
		killTimer := time.NewTimer(nodeE2EGracefulShutdownTimeout)
		select {
		case <-process.exited:
		case <-killTimer.C:
			t.Errorf("cleanup: Node helper did not exit after kill")
		}
		killTimer.Stop()
	}

	readerTimer := time.NewTimer(nodeE2EGracefulShutdownTimeout)
	defer readerTimer.Stop()
	for name, done := range map[string]<-chan struct{}{
		"stdout": process.stdoutDone,
		"stderr": process.stderrDone,
	} {
		select {
		case <-done:
		case <-readerTimer.C:
			t.Errorf("cleanup: %s reader did not stop", name)
			return
		}
	}
}

func (process *accountHealthCheckDispatchNodeE2EProcess) diagnostics() string {
	process.waitMu.Lock()
	waitErr := process.waitErr
	process.waitMu.Unlock()
	pid := 0
	if process.cmd.Process != nil {
		pid = process.cmd.Process.Pid
	}
	return fmt.Sprintf(
		"\nNode helper PID: %d\nwait error: %v\nstdout tail:\n%s\nstderr tail:\n%s",
		pid,
		waitErr,
		process.stdoutTail.String(),
		process.stderrTail.String(),
	)
}

func accountHealthCheckDispatchNodeE2EOutputTail(output []byte) string {
	if len(output) > nodeE2EOutputTailLimit {
		output = output[len(output)-nodeE2EOutputTailLimit:]
	}
	return strings.TrimSpace(string(output))
}

func (tail *accountHealthCheckDispatchNodeE2EBoundedTail) appendLine(line string) {
	tail.mu.Lock()
	defer tail.mu.Unlock()

	tail.data = append(tail.data, line...)
	tail.data = append(tail.data, '\n')
	if len(tail.data) > tail.limit {
		tail.data = append([]byte(nil), tail.data[len(tail.data)-tail.limit:]...)
	}
}

func (tail *accountHealthCheckDispatchNodeE2EBoundedTail) String() string {
	tail.mu.Lock()
	defer tail.mu.Unlock()
	return string(tail.data)
}
