package kernel

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/andybalholm/brotli"
)

// CompressionThresholdBytes mirrors httpCompressionThresholdBytes.
const CompressionThresholdBytes = 1024

// compressionSupportedEncodings / compressionPreferredEncodings mirror
// compression@1.8.1 with Node brotli support (SUPPORTED_ENCODING /
// PREFERRED_ENCODING).
var (
	compressionSupportedEncodings = []string{"br", "gzip", "deflate", "identity"}
	compressionPreferredEncodings = []string{"br", "gzip"}
)

// CompressionMiddleware mirrors createHttpCompressionMiddleware backed by
// compression@1.8.1: every response is wrapped; the encode decision runs at
// header-commit time exactly like the on-headers hook. Commit timing:
//   - a single buffered write that stays below the threshold resolves at
//     handler end (Node res.end(chunk) length estimate),
//   - a second write or an explicit Flush commits immediately without an
//     end-chunk estimate (Node res.write path),
//   - flushes issued before the first body byte are no-ops that neither
//     commit headers nor abandon the later compression decision.
//
// WriteHeader is deferred until the decision so the Content-Encoding/Vary
// mutations land before net/http snapshots the status.
func CompressionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cw := &compressionWriter{ResponseWriter: w, req: r}
		defer cw.finish()
		next.ServeHTTP(cw, r)
	})
}

type compressionWriter struct {
	http.ResponseWriter
	req            *http.Request
	buffer         bytes.Buffer
	stream         io.WriteCloser // gzip / zlib / brotli writer once committed
	streamFlusher  interface{ Flush() error }
	status         int
	headerRecorded bool
	bodySeen       bool
	committed      bool
}

func (c *compressionWriter) WriteHeader(status int) {
	if c.headerRecorded || c.committed {
		return
	}
	c.status = status
	c.headerRecorded = true
}

// recordedStatus exposes the handler-declared status before the deferred
// forward reaches the underlying chain (used by the mutation guard to
// classify the claim at Node's res 'finish' timing).
func (c *compressionWriter) recordedStatus() (int, bool) {
	if c.headerRecorded || c.committed {
		return c.status, true
	}
	return 0, false
}

func (c *compressionWriter) Write(body []byte) (int, error) {
	if !c.committed {
		if !c.bodySeen {
			// First write: ambiguous between Node res.end(chunk) and
			// res.write; buffer until the length source resolves.
			c.bodySeen = true
			c.buffer.Write(body)
			if c.buffer.Len() >= CompressionThresholdBytes {
				// The threshold is satisfied no matter which length source
				// (Content-Length header or end-chunk estimate) applies.
				c.commit(true, c.buffer.Len())
			}
			return len(body), nil
		}
		// Second write: the handler streams chunks, which is Node's
		// res.write path — headers commit without an end-chunk estimate.
		c.commit(false, 0)
	}
	c.bodySeen = true
	if c.stream != nil {
		return c.stream.Write(body)
	}
	return c.ResponseWriter.Write(body)
}

// commit mirrors the compression@1.8.1 on-headers hook. estimateKnown
// carries the Node end-proxy length estimate (res.end(chunk)); without it
// only the Content-Length header feeds the threshold check.
func (c *compressionWriter) commit(estimateKnown bool, estimate int) {
	if c.committed {
		return
	}
	c.committed = true
	status := c.status
	if !c.headerRecorded {
		status = http.StatusOK
	}
	header := c.Header()
	if compressionFilter(header) && compressionShouldTransform(header) {
		varyAcceptEncoding(header)
		if !compressionBelowThreshold(header, estimateKnown, estimate) &&
			header.Get("Content-Encoding") == "" &&
			c.req.Method != http.MethodHead {
			// negotiateAcceptEncoding returns "identity" both for an absent
			// Accept-Encoding header (Node enforceEncoding default) and for
			// headers whose only acceptable encoding is identity; both
			// resolve to the nocompress('not acceptable') branch.
			method := negotiateAcceptEncoding(c.req.Header.Get("Accept-Encoding"))
			if method != "" && method != "identity" {
				header.Set("Content-Encoding", method)
				header.Del("Content-Length")
				c.stream, c.streamFlusher = newCompressionStream(method, c.ResponseWriter)
			}
		}
	}
	c.ResponseWriter.WriteHeader(status)
	if c.stream != nil {
		if c.buffer.Len() > 0 {
			_, _ = c.stream.Write(c.buffer.Bytes())
		}
	} else if c.buffer.Len() > 0 {
		_, _ = c.ResponseWriter.Write(c.buffer.Bytes())
	}
	c.buffer.Reset()
}

// finish flushes the compression stream (or spills buffered raw bytes) after
// the handler returns, mirroring res.end.
func (c *compressionWriter) finish() {
	if !c.committed {
		c.commit(true, c.buffer.Len())
	}
	if c.stream != nil {
		_ = c.stream.Close()
		c.stream = nil
	}
}

// Flush mirrors res.flush of compression@1.8.1: before the first body byte
// it is a no-op (Node's flush never commits headers or locks identity);
// afterwards it flushes the compression stream or the raw response.
func (c *compressionWriter) Flush() {
	if !c.committed {
		if !c.bodySeen {
			return
		}
		c.commit(false, 0)
	}
	if c.stream != nil && c.streamFlusher != nil {
		_ = c.streamFlusher.Flush()
	}
	if flusher, ok := c.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (c *compressionWriter) MarkUpstream() {
	if marker, ok := c.ResponseWriter.(UpstreamMarker); ok {
		marker.MarkUpstream()
	}
}

// MarkExplicitMethodContract forwards the handler-facing 405 opt-out to the
// kernel contract writer underneath the compression layer.
func (c *compressionWriter) MarkExplicitMethodContract() {
	if exempt, ok := c.ResponseWriter.(MethodContractExempt); ok {
		exempt.MarkExplicitMethodContract()
	}
}

func (c *compressionWriter) MarkedUpstream() bool {
	if marker, ok := c.ResponseWriter.(UpstreamMarker); ok {
		return marker.MarkedUpstream()
	}
	return false
}

// compressionFilter mirrors shouldCompressHttpResponse plus the
// compression.filter default (compressible content type).
func compressionFilter(header http.Header) bool {
	if header.Get("Content-Encoding") != "" {
		return false
	}
	if responseHeaderIncludes(header, "Content-Type", "text/event-stream") {
		return false
	}
	if responseHeaderIncludes(header, "Content-Disposition", "attachment") {
		return false
	}
	return compressibleMediaType(header.Get("Content-Type"))
}

func responseHeaderIncludes(header http.Header, name, expected string) bool {
	for _, value := range header.Values(name) {
		if strings.Contains(strings.ToLower(value), expected) {
			return true
		}
	}
	return false
}

// compressibleMediaType mirrors compressible@2.0.18.
var (
	compressibleTypeExtract  = regexp.MustCompile(`^\s*([^;\s]*)(?:;|\s|$)`)
	compressibleTypeFallback = regexp.MustCompile(`^text/|\+(?:json|text|xml)$`)
)

func compressibleMediaType(contentType string) bool {
	match := compressibleTypeExtract.FindStringSubmatch(contentType)
	if match == nil {
		return false
	}
	mime := strings.ToLower(match[1])
	if flag, ok := mimeDBCompressible[mime]; ok {
		return flag
	}
	return compressibleTypeFallback.MatchString(mime)
}

// compressionShouldTransform mirrors the no-transform branch of
// compression@1.8.1 (Cache-Control must not contain the no-transform
// directive).
var cacheControlNoTransform = regexp.MustCompile(`(?:^|,)\s*?no-transform\s*?(?:,|$)`)

func compressionShouldTransform(header http.Header) bool {
	cacheControl := header.Get("Cache-Control")
	return cacheControl == "" || !cacheControlNoTransform.MatchString(cacheControl)
}

// compressionBelowThreshold mirrors `Number(Content-Length) < threshold ||
// length < threshold`: an absent or unparseable header is NaN (never below),
// and the end-chunk estimate only applies when no header is set.
func compressionBelowThreshold(header http.Header, estimateKnown bool, estimate int) bool {
	if length := header.Get("Content-Length"); length != "" {
		if parsed, err := strconv.ParseInt(strings.TrimSpace(length), 10, 64); err == nil {
			return parsed < CompressionThresholdBytes
		}
		return false
	}
	if !estimateKnown {
		return false
	}
	return int64(estimate) < CompressionThresholdBytes
}

func newCompressionStream(method string, w http.ResponseWriter) (io.WriteCloser, interface{ Flush() error }) {
	switch method {
	case "br":
		// Node compression@1.8.1 pins BROTLI_PARAM_QUALITY to 4.
		bw := brotli.NewWriterOptions(w, brotli.WriterOptions{Quality: 4})
		return bw, bw
	case "deflate":
		// zlib.createDeflate: zlib-wrapped deflate (RFC 1950).
		zw := zlib.NewWriter(w)
		return zw, zw
	default:
		gw := gzip.NewWriter(w)
		return gw, gw
	}
}

// varyAcceptEncoding mirrors vary@1.1.2 for the Accept-Encoding field.
func varyAcceptEncoding(header http.Header) {
	existing := strings.Join(header.Values("Vary"), ", ")
	appended := appendVaryField(existing, "Accept-Encoding")
	if appended != "" {
		header.Set("Vary", appended)
	}
}

func appendVaryField(headerValue, field string) string {
	// existing, unspecified vary
	if headerValue == "*" {
		return headerValue
	}
	vals := splitVaryFields(strings.ToLower(headerValue))
	for _, v := range vals {
		if v == "*" {
			return "*"
		}
	}
	lower := strings.ToLower(field)
	if !sliceContainsString(vals, lower) {
		vals = append(vals, lower)
		if headerValue != "" {
			return headerValue + ", " + field
		}
		return field
	}
	return headerValue
}

func splitVaryFields(header string) []string {
	fields := strings.Split(header, ",")
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		out = append(out, strings.TrimSpace(field))
	}
	return out
}

func sliceContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// negotiateAcceptEncoding mirrors Negotiator#encoding over
// compressionSupportedEncodings with negotiator@0.6.4 preferred semantics.
func negotiateAcceptEncoding(header string) string {
	accepts := parseAcceptEncodingHeader(header)

	type encodingPriority struct {
		encoding string
		q        float64
		s        int
		o        int
		i        int
	}
	priorities := make([]encodingPriority, 0, len(compressionSupportedEncodings))
	for index, provided := range compressionSupportedEncodings {
		current := encodingPriority{encoding: provided, q: 0, s: 0, o: -1, i: index}
		for _, spec := range accepts {
			s := 0
			if strings.EqualFold(spec.encoding, provided) {
				s = 1
			} else if spec.encoding != "*" {
				continue
			}
			candidate := encodingPriority{encoding: provided, q: spec.q, s: s, o: spec.i, i: index}
			// JS: (p.s - c.s || p.q - c.q || p.o - c.o) < 0 — NaN q falls
			// through to the order comparison.
			less := float64(current.s - candidate.s)
			if less == 0 {
				less = current.q - candidate.q
			}
			if less == 0 || math.IsNaN(less) {
				less = float64(current.o - candidate.o)
			}
			if less < 0 {
				current = candidate
			}
		}
		priorities = append(priorities, current)
	}

	acceptable := make([]encodingPriority, 0, len(priorities))
	for _, p := range priorities {
		if p.q > 0 && !math.IsNaN(p.q) { // isQuality
			acceptable = append(acceptable, p)
		}
	}
	if len(acceptable) == 0 {
		return ""
	}
	preferredIndex := func(encoding string) int {
		for i, candidate := range compressionPreferredEncodings {
			if candidate == encoding {
				return i
			}
		}
		return -1
	}
	sort.SliceStable(acceptable, func(a, b int) bool {
		x, y := acceptable[a], acceptable[b]
		// JS: if (a.q !== b.q) return b.q - a.q — a NaN difference sorts as
		// "equal" and falls through to the preference order.
		if !math.IsNaN(x.q) && !math.IsNaN(y.q) && x.q != y.q {
			return x.q > y.q
		}
		xPreferred := preferredIndex(x.encoding)
		yPreferred := preferredIndex(y.encoding)
		if xPreferred == -1 && yPreferred == -1 {
			if x.s != y.s {
				return x.s > y.s
			}
			if x.o != y.o {
				return x.o < y.o
			}
			return x.i < y.i
		}
		if xPreferred != -1 && yPreferred != -1 {
			return xPreferred < yPreferred
		}
		return yPreferred == -1
	})
	return acceptable[0].encoding
}

type acceptEncodingSpec struct {
	encoding string
	q        float64
	i        int
}

var encodingSpecPattern = regexp.MustCompile(`^\s*([^\s;]+)\s*(?:;(.*))?$`)

// parseAcceptEncodingHeader mirrors negotiator@0.6.4 parseAcceptEncoding,
// including the implicit identity entry carrying the minimum quality.
func parseAcceptEncodingHeader(header string) []acceptEncodingSpec {
	parts := strings.Split(header, ",")
	specs := make([]acceptEncodingSpec, 0, len(parts)+1)
	hasIdentity := false
	minQuality := 1.0
	for i, part := range parts {
		spec, ok := parseEncodingSpec(strings.TrimSpace(part), i)
		if !ok {
			continue
		}
		specs = append(specs, spec)
		if strings.EqualFold(spec.encoding, "identity") || spec.encoding == "*" {
			hasIdentity = true
		}
		// minQuality = Math.min(minQuality, encoding.q || 1)
		q := spec.q
		if q == 0 || math.IsNaN(q) {
			q = 1
		}
		if q < minQuality {
			minQuality = q
		}
	}
	if !hasIdentity {
		specs = append(specs, acceptEncodingSpec{encoding: "identity", q: minQuality, i: len(parts)})
	}
	return specs
}

func parseEncodingSpec(value string, index int) (acceptEncodingSpec, bool) {
	match := encodingSpecPattern.FindStringSubmatch(value)
	if match == nil {
		return acceptEncodingSpec{}, false
	}
	spec := acceptEncodingSpec{encoding: match[1], q: 1, i: index}
	if params := match[2]; params != "" {
		for _, param := range strings.Split(params, ";") {
			keyValue := strings.SplitN(strings.TrimSpace(param), "=", 2)
			if keyValue[0] == "q" {
				spec.q = jsParseFloat(keyValueValue(keyValue))
				break
			}
		}
	}
	return spec, true
}

func keyValueValue(parts []string) string {
	if len(parts) == 2 {
		return parts[1]
	}
	return ""
}

// jsParseFloat mirrors parseFloat for the q parameter prefix grammar.
var jsFloatPrefix = regexp.MustCompile(`^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?`)

func jsParseFloat(value string) float64 {
	match := jsFloatPrefix.FindString(value)
	if match == "" {
		return math.NaN()
	}
	parsed, err := strconv.ParseFloat(match, 64)
	if err != nil {
		return math.NaN()
	}
	return parsed
}
