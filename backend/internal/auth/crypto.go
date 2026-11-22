// Package auth: argon2id passwords, sessions, email OTP, passkeys, permissions and middleware.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
)

// RandomToken is a URL-safe random token (32 bytes ≈ 256 bits by default).
func RandomToken(nbytes ...int) string {
	n := 32
	if len(nbytes) > 0 {
		n = nbytes[0]
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// SHA256Hex hashes high-entropy tokens (session, ticket). Not for passwords or OTP codes.
func SHA256Hex(v string) string {
	h := sha256.Sum256([]byte(v))
	return hex.EncodeToString(h[:])
}

// HashOTP is an HMAC-SHA256 keyed with the server pepper and bound to the challenge id.
func HashOTP(pepper, challengeID, code string) string {
	m := hmac.New(sha256.New, []byte(pepper))
	m.Write([]byte(challengeID + ":" + code))
	return hex.EncodeToString(m.Sum(nil))
}

// SafeEqualHex compares two hex strings in constant time (false on malformed input).
func SafeEqualHex(a, b string) bool {
	ba, err1 := hex.DecodeString(a)
	bb, err2 := hex.DecodeString(b)
	if err1 != nil || err2 != nil || len(ba) != len(bb) {
		return false
	}
	return subtle.ConstantTimeCompare(ba, bb) == 1
}

// GenerateOTPCode returns a zero-padded 6-digit code from a CSPRNG.
func GenerateOTPCode() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%06d", n.Int64())
}
