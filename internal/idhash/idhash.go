// Package idhash derives the short fixed-width ids shared by repoid and
// agentid, so both packages produce keys of the same shape.
package idhash

import (
	"crypto/sha256"
	"encoding/hex"
)

// Len is the hex length of a derived id.
const Len = 12

// Sum returns the first Len hex characters of the SHA-256 of s.
func Sum(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:Len]
}
