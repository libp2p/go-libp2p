package httppeeridauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"hash"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/http/auth/internal/handshake"
)

type ServerPeerIDAuth struct {
	PrivKey  crypto.PrivKey
	TokenTTL time.Duration
	Next     func(peer peer.ID, w http.ResponseWriter, r *http.Request)
	// NoTLS is a flag that allows the server to accept requests without a TLS
	// ServerName. Used when something else is terminating the TLS connection.
	NoTLS bool
	// Required when NoTLS is true. The server will only accept requests for
	// which the Host header returns true.
	ValidHostnameFn func(hostname string) bool

	Hmac     hash.Hash
	initHmac sync.Once
}

// ServeHTTP implements the http.Handler interface for PeerIDAuth. It will
// attempt to authenticate the request using using the libp2p peer ID auth
// scheme. If a Next handler is set, it will be called on authenticated
// requests.
func (a *ServerPeerIDAuth) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.ServeHTTPWithNextHandler(w, r, a.Next)
}

func (a *ServerPeerIDAuth) ServeHTTPWithNextHandler(w http.ResponseWriter, r *http.Request, next func(peer.ID, http.ResponseWriter, *http.Request)) {
	a.initHmac.Do(func() {
		if a.Hmac == nil {
			key := make([]byte, 32)
			_, err := rand.Read(key)
			if err != nil {
				panic(err)
			}
			a.Hmac = hmac.New(sha256.New, key)
		}
	})

	hostname := r.Host
	if a.NoTLS {
		if a.ValidHostnameFn == nil {
			log.Error("No ValidHostnameFn set. Required for NoTLS")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if !a.ValidHostnameFn(hostname) {
			log.Debugf("Unauthorized request for host %s: hostname returned false for ValidHostnameFn", hostname)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	} else {
		if r.TLS == nil {
			log.Warn("No TLS connection, and NoTLS is false")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if hostname != r.TLS.ServerName {
			log.Debugf("Unauthorized request for host %s: hostname mismatch. Expected %s", hostname, r.TLS.ServerName)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if a.ValidHostnameFn != nil && !a.ValidHostnameFn(hostname) {
			log.Debugf("Unauthorized request for host %s: hostname returned false for ValidHostnameFn", hostname)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}

	hs := handshake.PeerIDAuthHandshakeServer{
		Hostname: hostname,
		PrivKey:  a.PrivKey,
		TokenTTL: a.TokenTTL,
		Hmac:     a.Hmac,
	}
	err := hs.ParseHeaderVal([]byte(r.Header.Get("Authorization")))
	if err != nil {
		log.Debugf("Failed to parse header: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	err = hs.Run()
	if err != nil {
		switch {
		case errors.Is(err, handshake.ErrInvalidHMAC),
			errors.Is(err, handshake.ErrExpiredChallenge),
			errors.Is(err, handshake.ErrExpiredToken):

			hs := handshake.PeerIDAuthHandshakeServer{
				Hostname: hostname,
				PrivKey:  a.PrivKey,
				TokenTTL: a.TokenTTL,
				Hmac:     a.Hmac,
			}
			_ = hs.Run() // First run will never err
			hs.SetHeader(w.Header())
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		log.Debugf("Failed to run handshake: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	hs.SetHeader(w.Header())

	peer, err := hs.PeerID()
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	if next == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	next(peer, w, r)
}

// HasAuthHeader checks if the HTTP request contains an Authorization header
// that starts with the PeerIDAuthScheme prefix.
func HasAuthHeader(r *http.Request) bool {
	h := r.Header.Get("Authorization")
	return h != "" && strings.HasPrefix(h, handshake.PeerIDAuthScheme)
}
