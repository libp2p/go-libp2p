package httpping

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

const pingSize = 32
const PingProtocolID = "/http-ping/1"

type Ping struct{}

var _ http.Handler = Ping{}

// ServeHTTP implements http.Handler.
func (Ping) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body := [pingSize]byte{}
	_, err := io.ReadFull(r.Body, body[:])
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(pingSize))
	w.Write(body[:])
}

// SendPing send an ping request over HTTP. The provided client should be namespaced to the Ping protocol.
func SendPing(client http.Client) error {
	body := [pingSize]byte{}
	_, err := io.ReadFull(rand.Reader, body[:])
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", "/", bytes.NewReader(body[:]))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Length", strconv.Itoa(pingSize))
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	rBody := [pingSize]byte{}
	_, err = io.ReadFull(resp.Body, rBody[:])
	if err != nil {
		return err
	}

	if !bytes.Equal(body[:], rBody[:]) {
		return errors.New("ping body mismatch")
	}
	return nil
}
