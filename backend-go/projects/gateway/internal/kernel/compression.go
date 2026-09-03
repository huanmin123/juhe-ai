package kernel

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"strconv"
	"strings"
)

// CompressionThresholdBytes mirrors httpCompressionThresholdBytes.
const CompressionThresholdBytes = 1024

// CompressionMiddleware mirrors createHttpCompressionMiddleware: gzip when
// the client accepts it and the response reaches the 1024-byte threshold.
// The compressibility checks (already encoded / event stream / attachment)
// run when response headers are final, i.e. at first write, so SSE handlers
// that set their Content-Type late stay uncompressed. Bodies below the
// threshold are flushed uncompressed at handler end, mirroring the Node
// compression buffer.
func CompressionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !acceptsGzip(r) {
			next.ServeHTTP(w, r)
			return
		}
		cw := &compressionWriter{ResponseWriter: w}
		defer cw.finish()
		next.ServeHTTP(cw, r)
	})
}

func acceptsGzip(r *http.Request) bool {
	encoding := r.Header.Get("Accept-Encoding")
	for _, part := range strings.Split(encoding, ",") {
		if strings.EqualFold(strings.TrimSpace(strings.SplitN(part, ";", 2)[0]), "gzip") {
			return true
		}
	}
	return false
}

type compressionWriter struct {
	http.ResponseWriter
	buffer    *bytes.Buffer
	gz        *gzip.Writer
	started   bool
	forbidden bool
}

func (c *compressionWriter) start() {
	c.started = true
	header := c.Header()
	if header.Get("Content-Encoding") != "" ||
		strings.Contains(strings.ToLower(header.Get("Content-Type")), "text/event-stream") ||
		strings.Contains(strings.ToLower(header.Get("Content-Disposition")), "attachment") {
		c.forbidden = true
		return
	}
	declared := -1
	if lengthHeader := header.Get("Content-Length"); lengthHeader != "" {
		if length, err := strconv.Atoi(lengthHeader); err == nil {
			declared = length
		}
	}
	if declared >= 0 {
		if declared >= CompressionThresholdBytes {
			c.beginGzip()
		} else {
			c.buffer = &bytes.Buffer{}
		}
		return
	}
	// Streaming response: buffer up to the threshold, then decide.
	c.buffer = &bytes.Buffer{}
}

func (c *compressionWriter) beginGzip() {
	c.Header().Set("Content-Encoding", "gzip")
	c.Header().Del("Content-Length")
	c.Header().Add("Vary", "Accept-Encoding")
	c.gz = gzip.NewWriter(c.ResponseWriter)
	if c.buffer != nil {
		_, _ = c.gz.Write(c.buffer.Bytes())
		c.buffer.Reset()
		c.buffer = nil
	}
}

func (c *compressionWriter) Write(body []byte) (int, error) {
	if !c.started {
		c.start()
	}
	if c.forbidden {
		return c.ResponseWriter.Write(body)
	}
	if c.gz != nil {
		return c.gz.Write(body)
	}
	c.buffer.Write(body)
	if c.buffer.Len() >= CompressionThresholdBytes {
		c.beginGzip()
	}
	return len(body), nil
}

// finish flushes buffered bytes uncompressed when the handler stayed below
// the threshold, mirroring the Node compression end-of-response decision.
func (c *compressionWriter) finish() {
	if c.gz != nil {
		_ = c.gz.Close()
		return
	}
	if c.buffer != nil && c.buffer.Len() > 0 {
		_, _ = c.ResponseWriter.Write(c.buffer.Bytes())
		c.buffer.Reset()
	}
}

// Flush drains gzip state so streaming handlers stay live.
func (c *compressionWriter) Flush() {
	if c.gz != nil {
		_ = c.gz.Flush()
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

func (c *compressionWriter) MarkedUpstream() bool {
	if marker, ok := c.ResponseWriter.(UpstreamMarker); ok {
		return marker.MarkedUpstream()
	}
	return false
}
