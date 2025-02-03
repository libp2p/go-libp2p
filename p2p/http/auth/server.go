package httppeeridauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"hash"
	"net/http"
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

			hmac.Reset()
			hs := handshake.PeerIDAuthHandshakeServer{
				Hostname: hostname,
				PrivKey:  a.PrivKey,
				TokenTTL: a.TokenTTL,
				Hmac:     hmac,
			}
			hs.Run()
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

	if a.Next == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	a.Next(peer, w, r)
}
