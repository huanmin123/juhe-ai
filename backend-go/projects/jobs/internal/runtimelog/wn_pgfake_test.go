package runtimelog

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// wn_pgfake_test.go 提供一个最小化的 PostgreSQL 线协议（protocol v3）假服务。
// runtimelog 的 postgresStore 直接持有 *pgxpool.Pool，无法用 database/sql
// 驱动 fake 替换；该假服务监听真实 127.0.0.1 TCP 端口，让 OpenStore/EnsureSchema
// 等生产入口原样运行，从而在无真实 PostgreSQL 的环境下覆盖 pgx 直连路径。
// 只实现本包查询需要的形状：文本结果格式、按 SQL 注册的脚本化响应、
// 简单查询与扩展查询（Parse/Bind/Describe/Execute/Sync）。

const (
	pgFakeTypeAuthOk          byte = 'R'
	pgFakeTypeParameterStatus byte = 'S'
	pgFakeTypeBackendKeyData  byte = 'K'
	pgFakeTypeReadyForQuery   byte = 'Z'
	pgFakeTypeSimpleQuery     byte = 'Q'
	pgFakeTypeParse           byte = 'P'
	pgFakeTypeBind            byte = 'B'
	pgFakeTypeDescribe        byte = 'D'
	pgFakeTypeExecute         byte = 'E'
	pgFakeTypeSync            byte = 'S'
	pgFakeTypeTerminate       byte = 'X'
	pgFakeTypeClose           byte = 'C'
	pgFakeTypeFlush           byte = 'H'

	pgFakeTypeParseComplete     byte = '1'
	pgFakeTypeBindComplete      byte = '2'
	pgFakeTypeCloseComplete     byte = '3'
	pgFakeTypeCommandComplete   byte = 'C'
	pgFakeTypeDataRow           byte = 'D'
	pgFakeTypeErrorResponse     byte = 'E'
	pgFakeTypeEmptyQuery        byte = 'I'
	pgFakeTypeNoData            byte = 'n'
	pgFakeTypeParamDescription  byte = 't'
	pgFakeTypeRowDescription    byte = 'T'
	pgFakeTypeNotice            byte = 'N'
	pgFakeTypeNegotiateProtocol byte = 'v'
)

const (
	pgFakeOIDText    uint32 = 25
	pgFakeOIDInt8    uint32 = 20
	pgFakeOIDInt4    uint32 = 23
	pgFakeOIDUnknown uint32 = 0
)

var pgFakeDollarPattern = regexp.MustCompile(`\$(\d+)`)

// pgFakeColumn 描述一行结果列：名称与类型 OID。所有列一律以文本格式返回。
type pgFakeColumn struct {
	name string
	oid  uint32
}

// pgFakeError 描述一次 ErrorResponse。
type pgFakeError struct {
	code    string
	message string
}

// pgFakeSpec 是一条 SQL 的响应脚本。columns 为结果列声明（Describe 响应需要）；
// rows/tag 为静态结果；fn/prefixFn 非空时在每次执行时基于 SQL 与文本参数动态生成行。
type pgFakeSpec struct {
	columns  []pgFakeColumn
	rows     [][]any
	tag      string
	err      *pgFakeError
	fn       func(args []string) ([][]any, string, *pgFakeError)
	prefixFn func(sql string, args []string) ([][]any, string, *pgFakeError)
}

// pgFakeExecuted 记录一次已执行的 SQL 及其文本参数，供测试逐字符断言。
type pgFakeExecuted struct {
	sql  string
	args []string
}

type pgFakeServer struct {
	t        *testing.T
	listener net.Listener

	mu       sync.Mutex
	handlers map[string]*pgFakeSpec
	prefixes map[string]*pgFakeSpec
	executed []pgFakeExecuted
	closed   bool

	wg sync.WaitGroup
}

func newPGFakeServer(t *testing.T) *pgFakeServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动 PostgreSQL fake 监听失败: %v", err)
	}
	server := &pgFakeServer{t: t, listener: listener, handlers: make(map[string]*pgFakeSpec)}
	server.installDefaults()
	server.wg.Add(1)
	go server.acceptLoop()
	t.Cleanup(server.Close)
	return server
}

func (server *pgFakeServer) installDefaults() {
	// pgx 的事务语句大小写随版本而异，全部注册。
	for _, sql := range []string{"BEGIN", "begin"} {
		server.handle(sql, nil, nil, "BEGIN")
	}
	for _, sql := range []string{"COMMIT", "commit"} {
		server.handle(sql, nil, nil, "COMMIT")
	}
	for _, sql := range []string{"ROLLBACK", "rollback"} {
		server.handle(sql, nil, nil, "ROLLBACK")
	}
	server.handle("SET", nil, nil, "SET")
	server.handlePrefixFunc("SET LOCAL", nil, func(string, []string) ([][]any, string, *pgFakeError) {
		return nil, "SET", nil
	})
}

// normalizePGSQL 把任意空白序列折叠为单个空格，避免多行 SQL 注册时
// 因缩进差异无法命中。
func normalizePGSQL(sql string) string {
	return strings.Join(strings.Fields(sql), " ")
}

// handle 注册静态响应脚本。tag 为空时按行数推导 SELECT n。
func (server *pgFakeServer) handle(sql string, columns []pgFakeColumn, rows [][]any, tag string) {
	server.mu.Lock()
	defer server.mu.Unlock()
	server.handlers[normalizePGSQL(sql)] = &pgFakeSpec{columns: columns, rows: rows, tag: tag}
}

// handleFunc 注册动态响应脚本，每次执行时以文本参数调用 fn；columns 声明结果列。
func (server *pgFakeServer) handleFunc(sql string, columns []pgFakeColumn, fn func(args []string) ([][]any, string, *pgFakeError)) {
	server.mu.Lock()
	defer server.mu.Unlock()
	server.handlers[normalizePGSQL(sql)] = &pgFakeSpec{columns: columns, fn: fn}
}

// handlePrefixFunc 注册按归一化前缀匹配的动态脚本，用于运行期拼接行数的
// 批量 INSERT/DELETE 语句。
func (server *pgFakeServer) handlePrefixFunc(prefix string, columns []pgFakeColumn, fn func(sql string, args []string) ([][]any, string, *pgFakeError)) {
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.prefixes == nil {
		server.prefixes = make(map[string]*pgFakeSpec)
	}
	server.prefixes[normalizePGSQL(prefix)] = &pgFakeSpec{columns: columns, prefixFn: fn}
}

// handleError 注册固定 ErrorResponse 脚本，用于非 ErrNoRows 的数据库错误分支。
func (server *pgFakeServer) handleError(sql string, code string, message string) {
	server.mu.Lock()
	defer server.mu.Unlock()
	server.handlers[normalizePGSQL(sql)] = &pgFakeSpec{err: &pgFakeError{code: code, message: message}}
}

// handleErrorPrefix 注册按归一化前缀匹配的 ErrorResponse 脚本。
func (server *pgFakeServer) handleErrorPrefix(prefix string, code string, message string) {
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.prefixes == nil {
		server.prefixes = make(map[string]*pgFakeSpec)
	}
	server.prefixes[normalizePGSQL(prefix)] = &pgFakeSpec{err: &pgFakeError{code: code, message: message}}
}

func (server *pgFakeServer) executedLog() []pgFakeExecuted {
	server.mu.Lock()
	defer server.mu.Unlock()
	return append([]pgFakeExecuted(nil), server.executed...)
}

func (server *pgFakeServer) logExec(sql string, args []string) {
	server.mu.Lock()
	defer server.mu.Unlock()
	server.executed = append(server.executed, pgFakeExecuted{sql: sql, args: args})
}

// URL 返回可供 pgxpool.ParseConfig 直接使用的连接串。
func (server *pgFakeServer) URL() string {
	return "postgres://jobs:secret@" + server.listener.Addr().String() + "/juhe_ai_fake?sslmode=disable"
}

func (server *pgFakeServer) Close() {
	server.mu.Lock()
	if server.closed {
		server.mu.Unlock()
		return
	}
	server.closed = true
	server.mu.Unlock()
	_ = server.listener.Close()
	server.wg.Wait()
}

func (server *pgFakeServer) acceptLoop() {
	defer server.wg.Done()
	for {
		conn, err := server.listener.Accept()
		if err != nil {
			return
		}
		server.wg.Add(1)
		go func() {
			defer server.wg.Done()
			server.serveConn(conn)
		}()
	}
}

// resolve 按 SQL 全文查找脚本，其次按归一化前缀匹配；未注册的 DDL 视为成功，
// 其余报未注册错误。返回命中的脚本与归一化 SQL。
func (server *pgFakeServer) resolve(sql string) (*pgFakeSpec, string, error) {
	server.mu.Lock()
	defer server.mu.Unlock()
	key := normalizePGSQL(sql)
	if spec, ok := server.handlers[key]; ok {
		return spec, key, nil
	}
	for prefix, spec := range server.prefixes {
		if strings.HasPrefix(key, prefix) {
			return spec, key, nil
		}
	}
	if strings.HasPrefix(key, "CREATE ") || strings.HasPrefix(key, "ALTER ") || strings.HasPrefix(key, "DROP ") || strings.HasPrefix(key, "COMMENT ") {
		fields := strings.Fields(key)
		return &pgFakeSpec{tag: fields[0] + " " + fields[1]}, key, nil
	}
	return nil, key, fmt.Errorf("wn_pgfake 未注册的 SQL: %.200s", key)
}

func (server *pgFakeServer) serveConn(conn net.Conn) {
	defer conn.Close()
	state := &pgFakeConn{
		server:     server,
		conn:       conn,
		reader:     bufio.NewReader(conn),
		statements: make(map[string]*pgFakePrepared),
		portals:    make(map[string]*pgFakePrepared),
	}
	if !state.handshake() {
		return
	}
	for {
		messageType, payload, err := readPGMessage(state.reader)
		if err != nil {
			return
		}
		if state.handleMessage(messageType, payload) {
			return
		}
	}
}

func readPGMessage(reader *bufio.Reader) (byte, []byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return 0, nil, err
	}
	length := binary.BigEndian.Uint32(header[1:5])
	if length < 4 {
		return 0, nil, fmt.Errorf("wn_pgfake 消息长度非法: %d", length)
	}
	payload := make([]byte, length-4)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return 0, nil, err
	}
	return header[0], payload, nil
}

// handshake 处理启动阶段：忽略 SSL/GSSENC 探测，完成 AuthenticationOk +
// ParameterStatus + BackendKeyData + ReadyForQuery。
func (state *pgFakeConn) handshake() bool {
	for {
		var header [8]byte
		if _, err := io.ReadFull(state.reader, header[:]); err != nil {
			return false
		}
		length := binary.BigEndian.Uint32(header[0:4])
		code := binary.BigEndian.Uint32(header[4:8])
		switch code {
		case 80877103, 80877104: // SSLRequest / GSSENCRequest：拒绝加密
			_, _ = state.conn.Write([]byte{pgFakeTypeNotice})
			continue
		case 80877102: // CancelRequest：直接断开
			return false
		default: // StartupMessage：剩余部分为参数键值，忽略即可
			if length > 8 {
				rest := make([]byte, length-8)
				if _, err := io.ReadFull(state.reader, rest); err != nil {
					return false
				}
			}
		}
		break
	}
	state.send(pgFakeTypeAuthOk, pgFakeInt32(0))
	state.sendParameterStatus("server_version", "16.4")
	state.sendParameterStatus("client_encoding", "UTF8")
	state.sendParameterStatus("DateStyle", "ISO, MDY")
	state.sendParameterStatus("TimeZone", "UTC")
	state.sendParameterStatus("integer_datetimes", "on")
	state.sendParameterStatus("standard_conforming_strings", "on")
	state.send(pgFakeTypeBackendKeyData, append(pgFakeInt32(1), pgFakeInt32(1)...))
	state.sendReadyForQuery()
	return true
}

type pgFakePrepared struct {
	sql           string
	params        int
	spec          *pgFakeSpec
	args          []string
	resultFormats []int16
}

type pgFakeConn struct {
	server     *pgFakeServer
	conn       net.Conn
	reader     *bufio.Reader
	out        bytes.Buffer
	statements map[string]*pgFakePrepared
	portals    map[string]*pgFakePrepared
	inTx       bool
	inFailedTx bool
}

func (state *pgFakeConn) send(messageType byte, payload []byte) {
	state.out.Reset()
	state.out.WriteByte(messageType)
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)+4))
	state.out.Write(header[:])
	state.out.Write(payload)
	_, _ = state.conn.Write(state.out.Bytes())
}

func (state *pgFakeConn) sendParameterStatus(key string, value string) {
	payload := append([]byte(key), 0)
	payload = append(payload, []byte(value)...)
	payload = append(payload, 0)
	state.send(pgFakeTypeParameterStatus, payload)
}

func (state *pgFakeConn) sendReadyForQuery() {
	status := byte('I')
	if state.inFailedTx {
		status = 'E'
	} else if state.inTx {
		status = 'T'
	}
	state.send(pgFakeTypeReadyForQuery, []byte{status})
}

func (state *pgFakeConn) sendError(code string, message string) {
	var payload bytes.Buffer
	payload.WriteByte('S')
	payload.WriteString("ERROR\x00")
	payload.WriteByte('V')
	payload.WriteString("ERROR\x00")
	payload.WriteByte('C')
	payload.WriteString(code)
	payload.WriteByte(0)
	payload.WriteByte('M')
	payload.WriteString(message)
	payload.WriteByte(0)
	payload.WriteByte(0)
	state.send(pgFakeTypeErrorResponse, payload.Bytes())
}

// applyTag 依据命令完成标签维护事务状态，让 ReadyForQuery 状态字节贴近真实。
func (state *pgFakeConn) applyTag(tag string) {
	switch strings.ToUpper(tag) {
	case "BEGIN":
		state.inTx = true
		state.inFailedTx = false
	case "COMMIT", "ROLLBACK":
		state.inTx = false
		state.inFailedTx = false
	}
}

// materialize 生成一次执行的结果；动态脚本以 SQL 与文本参数调用。
func (spec *pgFakeSpec) materialize(sql string, args []string) ([]pgFakeColumn, [][]any, string, *pgFakeError) {
	if spec.err != nil {
		return nil, nil, "", spec.err
	}
	rows, tag := spec.rows, spec.tag
	var execErr *pgFakeError
	switch {
	case spec.prefixFn != nil:
		rows, tag, execErr = spec.prefixFn(sql, args)
	case spec.fn != nil:
		rows, tag, execErr = spec.fn(args)
	}
	if execErr != nil {
		return nil, nil, "", execErr
	}
	if tag == "" {
		tag = fmt.Sprintf("SELECT %d", len(rows))
	}
	return spec.columns, rows, tag, nil
}

func (state *pgFakeConn) handleMessage(messageType byte, payload []byte) bool {
	reader := &pgFakeReader{buf: payload}
	switch messageType {
	case pgFakeTypeSimpleQuery:
		state.handleSimpleQuery(reader.cstring())
	case pgFakeTypeParse:
		state.handleParse(reader)
	case pgFakeTypeBind:
		state.handleBind(reader)
	case pgFakeTypeDescribe:
		state.handleDescribe(reader)
	case pgFakeTypeExecute:
		state.handleExecute(reader)
	case pgFakeTypeSync:
		state.sendReadyForQuery()
	case pgFakeTypeClose:
		reader.cstring()
		state.send(pgFakeTypeCloseComplete, nil)
	case pgFakeTypeFlush:
		// Flush 不需要独立响应，响应随写缓冲即时落盘。
	case pgFakeTypeTerminate:
		return true
	case pgFakeTypeNegotiateProtocol:
		state.send(pgFakeTypeNegotiateProtocol, pgFakeInt32(196608))
	default:
		state.sendError("0A000", fmt.Sprintf("wn_pgfake 不支持的消息类型 %q", string(messageType)))
	}
	return false
}

func (state *pgFakeConn) handleSimpleQuery(sql string) {
	trimmed := strings.TrimSpace(sql)
	if trimmed == "" || trimmed == ";" || stripPGComments(trimmed) == "" {
		// 纯注释（例如 pgx 的 "-- ping"）在真实 PostgreSQL 上返回空查询响应。
		state.send(pgFakeTypeEmptyQuery, nil)
		state.sendReadyForQuery()
		return
	}
	state.server.logExec(trimmed, nil)
	spec, _, err := state.server.resolve(trimmed)
	if err != nil {
		state.sendError("42601", err.Error())
		state.inFailedTx = state.inTx
		state.sendReadyForQuery()
		return
	}
	columns, rows, tag, execErr := spec.materialize(trimmed, nil)
	if execErr != nil {
		state.sendError(execErr.code, execErr.message)
		state.inFailedTx = state.inTx
		state.sendReadyForQuery()
		return
	}
	if len(columns) > 0 {
		state.sendRowDescription(columns)
	}
	for _, row := range rows {
		state.sendDataRow(row)
	}
	var tagPayload []byte
	tagPayload = append(tagPayload, []byte(tag)...)
	tagPayload = append(tagPayload, 0)
	state.send(pgFakeTypeCommandComplete, tagPayload)
	state.applyTag(tag)
	state.sendReadyForQuery()
}

func (state *pgFakeConn) handleParse(reader *pgFakeReader) {
	name := reader.cstring()
	sql := reader.cstring()
	paramCount := reader.int16()
	for index := 0; index < int(paramCount); index++ {
		_ = reader.int32()
	}
	params := maxDollarIndex(sql)
	spec, _, err := state.server.resolve(sql)
	if err != nil {
		state.sendError("42601", err.Error())
		state.inFailedTx = state.inTx
		return
	}
	state.statements[name] = &pgFakePrepared{sql: sql, params: params, spec: spec}
	state.send(pgFakeTypeParseComplete, nil)
}

func (state *pgFakeConn) handleBind(reader *pgFakeReader) {
	portal := reader.cstring()
	statement := reader.cstring()
	formatCount := reader.int16()
	for index := 0; index < int(formatCount); index++ {
		_ = reader.int16()
	}
	paramCount := reader.int16()
	args := make([]string, 0, paramCount)
	for index := 0; index < int(paramCount); index++ {
		length := reader.int32()
		if length < 0 {
			args = append(args, "")
			continue
		}
		args = append(args, decodePGParam(reader.bytes(int(length))))
	}
	resultFormatCount := reader.int16()
	resultFormats := make([]int16, 0, resultFormatCount)
	for index := 0; index < int(resultFormatCount); index++ {
		resultFormats = append(resultFormats, reader.int16())
	}
	prepared := state.statements[statement]
	if prepared == nil {
		state.sendError("26000", "wn_pgfake 未预编译的语句 "+statement)
		state.inFailedTx = state.inTx
		return
	}
	entry := *prepared
	entry.args = args
	entry.resultFormats = resultFormats
	state.portals[portal] = &entry
	state.send(pgFakeTypeBindComplete, nil)
}

func (state *pgFakeConn) handleDescribe(reader *pgFakeReader) {
	kind := reader.byteValue()
	name := reader.cstring()
	prepared := state.statements[name]
	if prepared == nil {
		if kind == 'P' {
			prepared = state.portals[name]
		}
		if prepared == nil {
			state.sendError("26000", "wn_pgfake 未预编译的语句 "+name)
			state.inFailedTx = state.inTx
			return
		}
	}
	if kind == 'S' {
		payload := pgFakeInt16(int16(prepared.params))
		for index := 0; index < prepared.params; index++ {
			payload = append(payload, pgFakeInt32(int32(pgFakeOIDUnknown))...)
		}
		state.send(pgFakeTypeParamDescription, payload)
	}
	if len(prepared.spec.columns) > 0 {
		state.sendRowDescription(prepared.spec.columns)
	} else {
		state.send(pgFakeTypeNoData, nil)
	}
}

func (state *pgFakeConn) handleExecute(reader *pgFakeReader) {
	portal := reader.cstring()
	_ = reader.int32() // maxRows：本假服务始终一次返回全部行
	entry := state.portals[portal]
	if entry == nil {
		state.sendError("34000", "wn_pgfake 未绑定的 portal "+portal)
		state.inFailedTx = state.inTx
		return
	}
	state.server.logExec(entry.sql, entry.args)
	// 每次执行都重新解析脚本，允许测试在预编译语句复用后覆盖响应。
	spec, _, err := state.server.resolve(entry.sql)
	if err != nil {
		state.sendError("42601", err.Error())
		state.inFailedTx = state.inTx
		return
	}
	_, rows, tag, execErr := spec.materialize(entry.sql, entry.args)
	if execErr != nil {
		state.sendError(execErr.code, execErr.message)
		state.inFailedTx = state.inTx
		return
	}
	for _, row := range rows {
		state.sendDataRowForPortal(row, entry, spec.columns)
	}
	var tagPayload []byte
	tagPayload = append(tagPayload, []byte(tag)...)
	tagPayload = append(tagPayload, 0)
	state.send(pgFakeTypeCommandComplete, tagPayload)
	state.applyTag(tag)
}

// portalFormatCode 返回列 i 实际使用的格式：与 PostgreSQL 一致，客户端在
// Bind 中请求的结果格式会覆盖预编译语句 Describe 时的默认文本格式。
func portalFormatCode(entry *pgFakePrepared, index int) int16 {
	if len(entry.resultFormats) == 1 {
		return entry.resultFormats[0]
	}
	if index < len(entry.resultFormats) {
		return entry.resultFormats[index]
	}
	return 0
}

// sendDataRowForPortal 按列格式编码数据行；二进制格式仅支持本包查询
// 实际 Scan 的整数 OID。
func (state *pgFakeConn) sendDataRowForPortal(row []any, entry *pgFakePrepared, columns []pgFakeColumn) {
	payload := pgFakeInt16(int16(len(row)))
	for index, cell := range row {
		value := ""
		if text, ok := cell.(string); ok {
			value = text
		} else if cell != nil {
			value = fmt.Sprintf("%v", cell)
		}
		if cell == nil {
			payload = append(payload, pgFakeInt32(-1)...)
			continue
		}
		if portalFormatCode(entry, index) == 1 {
			var encoded []byte
			switch columns[index].oid {
			case pgFakeOIDInt8:
				number, _ := strconv.ParseInt(value, 10, 64)
				encoded = make([]byte, 8)
				binary.BigEndian.PutUint64(encoded, uint64(number))
			case pgFakeOIDInt4:
				number, _ := strconv.ParseInt(value, 10, 32)
				encoded = make([]byte, 4)
				binary.BigEndian.PutUint32(encoded, uint32(number))
			default:
				encoded = []byte(value)
			}
			payload = append(payload, pgFakeInt32(int32(len(encoded)))...)
			payload = append(payload, encoded...)
			continue
		}
		payload = append(payload, pgFakeInt32(int32(len(value)))...)
		payload = append(payload, []byte(value)...)
	}
	state.send(pgFakeTypeDataRow, payload)
}

func (state *pgFakeConn) sendRowDescription(columns []pgFakeColumn) {
	payload := pgFakeInt16(int16(len(columns)))
	for _, column := range columns {
		payload = append(payload, []byte(column.name)...)
		payload = append(payload, 0)
		payload = append(payload, pgFakeInt32(0)...) // 表 OID
		payload = append(payload, pgFakeInt16(0)...) // 属性号
		payload = append(payload, pgFakeInt32(int32(column.oid))...)
		payload = append(payload, pgFakeInt16(-1)...) // typlen
		payload = append(payload, pgFakeInt32(-1)...) // typmod
		payload = append(payload, pgFakeInt16(0)...)  // 文本格式
	}
	state.send(pgFakeTypeRowDescription, payload)
}

func (state *pgFakeConn) sendDataRow(row []any) {
	payload := pgFakeInt16(int16(len(row)))
	for _, cell := range row {
		switch value := cell.(type) {
		case nil:
			payload = append(payload, pgFakeInt32(-1)...)
		case string:
			payload = append(payload, pgFakeInt32(int32(len(value)))...)
			payload = append(payload, []byte(value)...)
		default:
			text := fmt.Sprintf("%v", value)
			payload = append(payload, pgFakeInt32(int32(len(text)))...)
			payload = append(payload, []byte(text)...)
		}
	}
	state.send(pgFakeTypeDataRow, payload)
}

type pgFakeReader struct {
	buf []byte
	pos int
}

func (reader *pgFakeReader) byteValue() byte {
	value := reader.buf[reader.pos]
	reader.pos++
	return value
}

func (reader *pgFakeReader) cstring() string {
	end := reader.pos
	for end < len(reader.buf) && reader.buf[end] != 0 {
		end++
	}
	value := string(reader.buf[reader.pos:end])
	reader.pos = end + 1
	return value
}

func (reader *pgFakeReader) int16() int16 {
	value := int16(binary.BigEndian.Uint16(reader.buf[reader.pos : reader.pos+2]))
	reader.pos += 2
	return value
}

func (reader *pgFakeReader) int32() int32 {
	value := int32(binary.BigEndian.Uint32(reader.buf[reader.pos : reader.pos+4]))
	reader.pos += 4
	return value
}

func (reader *pgFakeReader) bytes(length int) []byte {
	value := reader.buf[reader.pos : reader.pos+length]
	reader.pos += length
	return value
}

func pgFakeInt16(value int16) []byte {
	var buffer [2]byte
	binary.BigEndian.PutUint16(buffer[:], uint16(value))
	return buffer[:]
}

func pgFakeInt32(value int32) []byte {
	var buffer [4]byte
	binary.BigEndian.PutUint32(buffer[:], uint32(value))
	return buffer[:]
}

// maxDollarIndex 统计 SQL 中 $N 占位符的最大序号，作为 ParameterDescription
// 的参数数量，pgx 以此校验实参数量。
func maxDollarIndex(sql string) int {
	maximum := 0
	for _, match := range pgFakeDollarPattern.FindAllStringSubmatch(sql, -1) {
		value, err := strconv.Atoi(match[1])
		if err == nil && value > maximum {
			maximum = value
		}
	}
	return maximum
}

// stripPGComments 去除以 -- 开头的行注释，判断语句是否实际为空。
func stripPGComments(sql string) string {
	lines := strings.Split(sql, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// decodePGParam 还原参数文本值；二进制参数尽力解码常见定长类型，仅用于日志与断言。
func decodePGParam(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	if utf8.Valid(raw) {
		return string(raw)
	}
	switch len(raw) {
	case 8:
		value := int64(binary.BigEndian.Uint64(raw))
		if value > -1<<53 && value < 1<<53 {
			return strconv.FormatInt(value, 10)
		}
	case 4:
		return strconv.FormatInt(int64(int32(binary.BigEndian.Uint32(raw))), 10)
	case 2:
		return strconv.FormatInt(int64(int16(binary.BigEndian.Uint16(raw))), 10)
	}
	return fmt.Sprintf("<binary len=%d>", len(raw))
}
