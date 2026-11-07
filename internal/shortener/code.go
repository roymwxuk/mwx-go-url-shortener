// Package shortener generates and validates short codes.
package shortener

import (
	"crypto/rand"
	"math/big"
	"regexp"
)

const (
	alphabet      = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no 0/O/1/l/I
	DefaultLength = 7
)

var validCode = regexp.MustCompile(`^[A-Za-z0-9_-]{3,32}$`)

// NewCode returns a cryptographically random code of length n.
func NewCode(n int) (string, error) {
	b := make([]byte, n)
	max := big.NewInt(int64(len(alphabet)))
	for i := range b {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b[i] = alphabet[idx.Int64()]
	}
	return string(b), nil
}

// ValidCustomCode reports whether a user-supplied alias is acceptable.
func ValidCustomCode(code string) bool { return validCode.MatchString(code) }
