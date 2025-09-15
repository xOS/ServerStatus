package utils

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"sync"
)

// Pooled buffers for JSON encoding to reduce allocations
var jsonBufPool = sync.Pool{New: func() interface{} { return new(bytes.Buffer) }}

// Gzip writer pool for fast compression of large JSON responses
var gzipPool = sync.Pool{New: func() interface{} {
	w, _ := gzip.NewWriterLevel(nil, gzip.BestSpeed)
	return w
}}

// GetBuffer returns a reset bytes.Buffer from pool
func GetJSONBuffer() *bytes.Buffer {
	buf := jsonBufPool.Get().(*bytes.Buffer)
	buf.Reset()
	return buf
}

// PutBuffer returns buffer to pool
func PutJSONBuffer(buf *bytes.Buffer) {
	if buf != nil {
		buf.Reset()
		jsonBufPool.Put(buf)
	}
}

// EncodeJSON marshals using utils.Json into a pooled buffer and returns bytes
// Caller owns the returned slice (copied), buffer is returned to pool
func EncodeJSON(v interface{}) ([]byte, error) {
	buf := GetJSONBuffer()
	defer PutJSONBuffer(buf)
	enc := Json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	// json.Encoder adds a trailing newline; keep behavior to match c.JSON,
	// frontend tolerant; if needed, trim last \n by bytes.TrimRight
	out := make([]byte, buf.Len())
	copy(out, buf.Bytes())
	return out, nil
}

// GzipIfAccepted compresses and writes payload only when gzip is accepted and worthwhile.
// If gzip is not used, this function DOES NOT write to w and returns (0, false, nil).
// Callers must handle the plain write path when gzipped == false to avoid double writes.
func GzipIfAccepted(w http.ResponseWriter, r *http.Request, payload []byte) (written int, gzipped bool, err error) {
	// Only gzip when Accept-Encoding includes gzip and payload large enough
	if len(payload) < 1024 { // small payloads not worth gzipping
		return 0, false, nil
	}
	if !acceptsGzip(r) {
		return 0, false, nil
	}
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Del("Content-Length")
	gw := gzipPool.Get().(*gzip.Writer)
	gw.Reset(w)
	// 写入压缩数据并确保仅关闭一次，避免生成多个 gzip 成员
	if _, err = gw.Write(payload); err != nil {
		// 出错时归还 writer
		_ = gw.Close()
		gzipPool.Put(gw)
		return 0, true, err
	}
	cerr := gw.Close()
	gzipPool.Put(gw)
	if cerr != nil {
		return 0, true, cerr
	}
	return 0, true, nil
}

func acceptsGzip(r *http.Request) bool {
	enc := r.Header.Get("Accept-Encoding")
	return bytes.Contains([]byte(enc), []byte("gzip"))
}
