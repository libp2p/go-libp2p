package libp2phttp

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"

	logging "github.com/ipfs/go-log/v2"
)

var bufWriterPool = sync.Pool{
	New: func() any {
		return bufio.NewWriterSize(nil, 4<<10)
	},
}

var bufReaderPool = sync.Pool{
	New: func() any {
		return bufio.NewReaderSize(nil, 4<<10)
	},
}

var log = logging.Logger("p2phttp")

var _ http.ResponseWriter = (*httpResponseWriter)(nil)

type httpResponseWriter struct {
	w                     *bufio.Writer
	directWriter          io.Writer
	header                http.Header
	wroteHeader           bool
	inferredContentLength bool
}

// Header implements http.ResponseWriter
func (w *httpResponseWriter) Header() http.Header {
	return w.header
}

// Write implements http.ResponseWriter
func (w *httpResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		if w.header.Get("Content-Type") == "" {
			contentType := http.DetectContentType(b)
			w.header.Set("Content-Type", contentType)
		}

		if w.w.Available() > len(b) {
			return w.w.Write(b)
		}
	}

	// Ran out of buffered space, We should check if we need to write the headers.
	if !w.wroteHeader {
		if w.header.Get("Content-Length") == "" && w.header.Get("Transfer-Encoding") == "" {
			log.Error("Handler did not set the Content-Length or Transfer-Encoding header. Clients may fail to parse this response")
		}
		w.WriteHeader(http.StatusOK)
	} else if w.inferredContentLength {
		log.Error("Tried to infer content length, but another write happened, so content length is wrong and headers are already written. This response may fail to parse by clients")
	}

	return w.w.Write(b)
}

func (w *httpResponseWriter) flush() {
	if !w.wroteHeader {
		// Be nice for small things
		if w.header.Get("Content-Length") == "" {
			w.inferredContentLength = true
			w.header.Set("Content-Length", strconv.Itoa(w.w.Buffered()))
		}

		// If WriteHeader has not yet been called, Write calls
		// WriteHeader(http.StatusOK) before writing the data.
		w.WriteHeader(http.StatusOK)
	}
	w.w.Flush()
}

// WriteHeader implements http.ResponseWriter
func (w *httpResponseWriter) WriteHeader(statusCode int) {
	if w.wroteHeader {
		log.Errorf("multiple WriteHeader calls dropping %d", statusCode)
		return
	}
	w.wroteHeader = true
	w.writeStatusLine(statusCode)
	w.header.Write(w.directWriter)
	w.directWriter.Write([]byte("\r\n"))
}

// Copied from Go stdlib https://cs.opensource.google/go/go/+/refs/tags/go1.20.2:src/net/http/server.go;drc=ea4631cc0cf301c824bd665a7980c13289ab5c9d;l=1533
func (w *httpResponseWriter) writeStatusLine(code int) {
	// Stack allocated
	scratch := [4]byte{}
	// Always HTTP/1.1
	w.directWriter.Write([]byte("HTTP/1.1 "))

	if text := http.StatusText(code); text != "" {
		w.directWriter.Write(strconv.AppendInt(scratch[:0], int64(code), 10))
		w.directWriter.Write([]byte(" "))
		w.directWriter.Write([]byte(text))
		w.directWriter.Write([]byte("\r\n"))
	} else {
		// don't worry about performance
		fmt.Fprintf(w.directWriter, "%03d status code %d\r\n", code, code)
	}
}

func ServeReadWriter(rw io.ReadWriter, handler http.Handler) {
	r := bufReaderPool.Get().(*bufio.Reader)
	r.Reset(rw)
	defer bufReaderPool.Put(r)

	buffedWriter := bufWriterPool.Get().(*bufio.Writer)
	buffedWriter.Reset(rw)
	defer bufWriterPool.Put(buffedWriter)
	w := httpResponseWriter{
		w:            buffedWriter,
		directWriter: rw,
		header:       make(http.Header),
	}
	defer w.flush()

	req, err := http.ReadRequest(r)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.w.Flush()
		log.Errorf("error reading request: %s", err)
		return
	}

	handler.ServeHTTP(&w, req)
}
