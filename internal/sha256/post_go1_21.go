//go:build go1.21

// This package use build tags to select between github.com/minio/sha256-simd
// for go1.20 and bellow and crypto/sha256 for go1.21 and above.
// This is used because a fast SHANI implementation of sha256 is only avaiable
// in the std for go1.21 and above. See https://go.dev/issue/50543.
// TODO: Once go1.22 releases remove this package and replace all uses
// with crypto/sha256 because the two supported version of go will have the fast
// implementation.
package sha256

import (
	"crypto/sha256"
	"hash"
)

func Sum256(b []byte) [sha256.Size]byte {
	return sha256.Sum256(b)
}

func New() hash.Hash {
	return sha256.New()
}
