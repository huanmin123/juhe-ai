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
// the client accepts it and the body reaches the 1024-byte threshold. The
// decision runs at first body byte (headers are final then); responses below
// the threshold flush raw at handler end, mirroring the Node compression
// buffer. WriteHeader is deferred until the decision so the Content-Encoding
// mutation lands before net/http snapshots the status.
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
	buffer        bytes.Buffer
	gz            *gzip.Writer
	status        int
	headerPassed  bool
	bodyWritten   bool
	gzipCommitted bool
}

func (c *compressionWriter) WriteHeader(status int) {
	if c.headerPassed {
		return
	}
	c.status = status
	c.headerPassed = true
	// Deferred: the gzip decision (which mutates headers) must run before
	// net/http snapshots the status. decide() passes the status through.
}

func (c *compressionWriter) decide(total int) {
	header := c.Header()
	c.gzipCommitted = false
	canGzip := header.Get("Content-Encoding") == "" &&
		!strings.Contains(strings.ToLower(header.Get("Content-Type")), "text/event-stream") &&
		!strings.Contains(strings.ToLower(header.Get("Content-Disposition")), "attachment")
	if canGzip {
		if lengthHeader := header.Get("Content-Length"); lengthHeader != "" {
			if length, err := strconv.Atoi(lengthHeader); err == nil && length < CompressionThresholdBytes {
				canGzip = false
			}
		}
	}
	if canGzip && total >= CompressionThresholdBytes {
		// Mutate headers BEFORE the status snapshot reaches net/http.
		header.Set("Content-Encoding", "gzip")
		header.Del("Content-Length")
		header.Add("Vary", "Accept-Encoding")
		c.gz = gzip.NewWriter(c.ResponseWriter)
		c.gzipCommitted = true
	}
	c.ResponseWriter.WriteHeader(c.status)
}

func (c *compressionWriter) Write(body []byte) (int, error) {
	if !c.headerPassed {
		c.WriteHeader(http.StatusOK)
	}
	if !c.bodyWritten {
		c.decide(c.buffer.Len() + len(body))
		c.bodyWritten = true
	}
	if c.gz != nil {
		return c.gz.Write(body)
	}
	return c.ResponseWriter.Write(body)
}

// finish flushes buffered bytes (raw or gzipped) after the handler returns.
func (c *compressionWriter) finish() {
	if !c.headerPassed {
		c.WriteHeader(http.StatusOK)
		c.decide(0)
		c.bodyWritten = true
	}
	if c.gz != nil {
		_ = c.gz.Close()
		c.gz = nil
		return
	}
	if c.buffer.Len() > 0 {
		_, _ = c.ResponseWriter.Write(c.buffer.Bytes())
	}
	c.buffer.Reset()
}

// Flush drains gzip state so streaming handlers stay live.
func (c *compressionWriter) Flush() {
	if !c.headerPassed {
		c.WriteHeader(http.StatusOK)
		c.decide(0)
		c.bodyWritten = true
	}
	if c.gz != nil {
		_ = c.gz.Flush()
	} else if c.buffer.Len() > 0 {
		c.buffer.WriteTo(c.ResponseWriter)
		c.buffer.Reset()
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
