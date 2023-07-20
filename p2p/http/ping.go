package libp2phttp

import (
	"io"
	"net/http"
	"strconv"
)

const pingSize = 32

func Ping(w http.ResponseWriter, r *http.Request) {
	body := [pingSize]byte{}
	_, err := io.ReadFull(r.Body, body[:])
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(pingSize))
	w.WriteHeader(http.StatusOK)
	w.Write(body[:])
}
