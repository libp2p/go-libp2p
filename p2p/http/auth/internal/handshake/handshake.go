package handshake

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"

	pool "github.com/libp2p/go-buffer-pool"
)

const PeerIDAuthScheme = "libp2p-PeerID"
const challengeLen = 32
const maxHeaderSize = 2048

var peerIDAuthSchemeBytes = []byte(PeerIDAuthScheme)

var errTooBig = errors.New("header value too big")
var errInvalid = errors.New("invalid header value")
var errNotRan = errors.New("not ran. call Run() first")

var randReader = rand.Reader // A var so it can be changed in tests
var nowFn = time.Now         // A var so it can be changed in tests

// params represent params passed in via headers. All []byte fields to avoid allocations.
type params struct {
	bearerTokenB64  []byte
	challengeClient []byte
	challengeServer []byte
	opaqueB64       []byte
	publicKeyB64    []byte
	sigB64          []byte
}

// parsePeerIDAuthSchemeParams parses the parameters of the PeerID auth scheme
// from the header string. zero alloc.
func (p *params) parsePeerIDAuthSchemeParams(headerVal []byte) error {
	if len(headerVal) > maxHeaderSize {
		return errTooBig
	}
	startIdx := bytes.Index(headerVal, peerIDAuthSchemeBytes)
	if startIdx == -1 {
		return nil
	}

	headerVal = headerVal[startIdx+len(PeerIDAuthScheme):]
	advance, token, err := splitAuthHeaderParams(headerVal, true)
	for ; err == nil; advance, token, err = splitAuthHeaderParams(headerVal, true) {
		headerVal = headerVal[advance:]
		bs := token
		splitAt := bytes.Index(bs, []byte("="))
		if splitAt == -1 {
			return errInvalid
		}
		kB := bs[:splitAt]
		v := bs[splitAt+1:]
		if len(v) < 2 || v[0] != '"' || v[len(v)-1] != '"' {
			return errInvalid
		}
		v = v[1 : len(v)-1] // drop quotes
		switch string(kB) {
		case "bearer":
			p.bearerTokenB64 = v
		case "challenge-client":
			p.challengeClient = v
		case "challenge-server":
			p.challengeServer = v
		case "opaque":
			p.opaqueB64 = v
		case "public-key":
			p.publicKeyB64 = v
		case "sig":
			p.sigB64 = v
		}
	}
	if err == bufio.ErrFinalToken {
		err = nil
	}
	return err
}

func splitAuthHeaderParams(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if len(data) == 0 && atEOF {
		return 0, nil, bufio.ErrFinalToken
	}

	start := 0
	for start < len(data) && (data[start] == ' ' || data[start] == ',') {
		// Ignore leading spaces and commas
		start++
	}
	if start == len(data) {
		return len(data), nil, nil
	}
	end := start + 1
	for end < len(data) && data[end] != ' ' && data[end] != ',' {
		// Consume until we hit a space or comma
		end++
	}
	token = data[start:end]
	if !bytes.ContainsAny(token, "=") {
		// This isn't a param. It's likely the next scheme. We're done
		return len(data), nil, bufio.ErrFinalToken
	}

	return end, token, nil
}

type headerBuilder struct {
	b              strings.Builder
	pastFirstField bool
}

func (h *headerBuilder) clear() {
	h.b.Reset()
	h.pastFirstField = false
}

func (h *headerBuilder) writeScheme(scheme string) {
	h.b.WriteString(scheme)
	h.b.WriteByte(' ')
}

func (h *headerBuilder) maybeAddComma() {
	if !h.pastFirstField {
		h.pastFirstField = true
		return
	}
	h.b.WriteString(", ")
}

// writeParam writes a key value pair to the header. It first b64 encodes the
// value. It uses buf as scratch space.
func (h *headerBuilder) writeParamB64(buf []byte, key string, val []byte) {
	if buf == nil {
		buf = make([]byte, base64.URLEncoding.EncodedLen(len(val)))
	}
	encodedVal := base64.URLEncoding.AppendEncode(buf[:0], val)
	h.writeParam(key, encodedVal)
}

// writeParam writes a key value pair to the header. It writes the val as-is.
func (h *headerBuilder) writeParam(key string, val []byte) {
	if len(val) == 0 {
		return
	}
	h.maybeAddComma()

	h.b.Grow(len(key) + len(`="`) + len(val) + 1)
	// Not doing fmt.Fprintf here to avoid one allocation
	h.b.WriteString(key)
	h.b.WriteString(`="`)
	h.b.Write(val)
	h.b.WriteByte('"')
}

type sigParam struct {
	k string
	v []byte
}

func verifySig(publicKey crypto.PubKey, prefix string, signedParts []sigParam, sig []byte) error {
	if publicKey == nil {
		return fmt.Errorf("no public key to verify signature")
	}

	b := pool.Get(4096)
	defer pool.Put(b)
	buf, err := genDataToSign(b[:0], prefix, signedParts)
	if err != nil {
		return fmt.Errorf("failed to generate signed data: %w", err)
	}
	ok, err := publicKey.Verify(buf, sig)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("signature verification failed")
	}

	return nil
}

func sign(privKey crypto.PrivKey, prefix string, partsToSign []sigParam) ([]byte, error) {
	if privKey == nil {
		return nil, fmt.Errorf("no private key available to sign")
	}
	b := pool.Get(4096)
	defer pool.Put(b)
	buf, err := genDataToSign(b[:0], prefix, partsToSign)
	if err != nil {
		return nil, fmt.Errorf("failed to generate data to sign: %w", err)
	}
	return privKey.Sign(buf)
}

func genDataToSign(buf []byte, prefix string, parts []sigParam) ([]byte, error) {
	// Sort the parts in lexicographic order
	slices.SortFunc(parts, func(a, b sigParam) int {
		return strings.Compare(a.k, b.k)
	})
	buf = append(buf, prefix...)
	for _, p := range parts {
		buf = binary.AppendUvarint(buf, uint64(len(p.k)+1+len(p.v))) // +1 for '='
		buf = append(buf, p.k...)
		buf = append(buf, '=')
		buf = append(buf, p.v...)
	}
	return buf, nil
}
