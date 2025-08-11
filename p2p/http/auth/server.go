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

type hmacPool struct {
	p sync.Pool
}

func newHmacPool(key []byte) *hmacPool {
	return &hmacPool{
		p: sync.Pool{
			New: func() any {
				return hmac.New(sha256.New, key)
			},
		},
	}
}

func (p *hmacPool) Get() hash.Hash {
	h := p.p.Get().(hash.Hash)
	h.Reset()
	return h
}

func (p *hmacPool) Put(h hash.Hash) {
	p.p.Put(h)
}

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

	HmacKey  []byte
	initHmac sync.Once
	hmacPool *hmacPool
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
		if a.HmacKey == nil {
			key := make([]byte, 32)
			_, err := rand.Read(key)
			if err != nil {
				panic(err)
			}
			a.HmacKey = key
		}
		a.hmacPool = newHmacPool(a.HmacKey)
	})

	hostname := r.Host
	if a.NoTLS {
		if a.ValidHostnameFn == nil {
			log.Error("No ValidHostnameFn set. Required for NoTLS")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if !a.ValidHostnameFn(hostname) {
			log.Debug("Unauthorized request for host: hostname returned false for ValidHostnameFn", "hostname", hostname)
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
			log.Debug("Unauthorized request for host: hostname mismatch", "hostname", hostname, "expected", r.TLS.ServerName)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if a.ValidHostnameFn != nil && !a.ValidHostnameFn(hostname) {
			log.Debug("Unauthorized request for host: hostname returned false for ValidHostnameFn", "hostname", hostname)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}

	hmac := a.hmacPool.Get()
	defer a.hmacPool.Put(hmac)
	hs := handshake.PeerIDAuthHandshakeServer{
		Hostname: hostname,
		PrivKey:  a.PrivKey,
		TokenTTL: a.TokenTTL,
		Hmac:     hmac,
	}
	err := hs.ParseHeaderVal([]byte(r.Header.Get("Authorization")))
	if err != nil {
		log.Debug("Failed to parse header", "err", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	err = hs.Run()
	if err != nil {
		switch {
		case errors.Is(err, handshake.ErrInvalidHMAC),
			errors.Is(err, handshake.ErrExpiredChallenge),
			errors.Is(err, handshake.ErrExpiredToken):

			hmac.Reset()
			hs := handshake.PeerIDAuthHandshakeServer{
				Hostname: hostname,
				PrivKey:  a.PrivKey,
				TokenTTL: a.TokenTTL,
				Hmac:     hmac,
			}
			_ = hs.Run() // First run will never err
			hs.SetHeader(w.Header())
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		log.Debug("Failed to run handshake", "err", err)
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
