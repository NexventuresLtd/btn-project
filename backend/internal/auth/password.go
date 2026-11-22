package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"btn-ops/internal/httpx"

	"golang.org/x/crypto/argon2"
)

// Same OWASP parameters as the Node backend (@node-rs/argon2): m=19 MiB, t=2, p=1, 32-byte output.
const (
	argonMemory  = 19456
	argonTime    = 2
	argonThreads = 1
	argonKeyLen  = 32
	argonSaltLen = 16
)

// HashPassword returns a PHC string "$argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>" (unpadded base64).
func HashPassword(password string) string {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		panic(err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

// VerifyHash checks password against a PHC argon2id string using the parameters embedded in it
// (so hashes produced by the Node @node-rs/argon2 keep working). Malformed hashes return false.
func VerifyHash(phc, password string) bool {
	parts := strings.Split(phc, "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	var m, t uint32
	var p uint8
	var pp uint32
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &pp); err != nil {
		return false
	}
	p = uint8(pp)
	if m == 0 || m > 1<<20 || t == 0 || t > 20 || p == 0 {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

var (
	dummyOnce sync.Once
	dummyHash string
)

// VerifyPassword verifies against hash; with a nil hash it burns equal time against a dummy hash so
// response time does not reveal which accounts exist.
func VerifyPassword(hash *string, password string) bool {
	if hash == nil || *hash == "" {
		dummyOnce.Do(func() { dummyHash = HashPassword("not-a-real-password-" + RandomToken(8)) })
		VerifyHash(dummyHash, password)
		return false
	}
	return VerifyHash(*hash, password)
}

var (
	reLower = regexp.MustCompile(`[a-z]`)
	reUpper = regexp.MustCompile(`[A-Z]`)
	reDigit = regexp.MustCompile(`[0-9]`)
)

// PasswordField validates a password field with the policy (10–128 chars, lower, upper, digit) and
// returns the value. Messages are the zod ones.
func PasswordField(v *httpx.V, key string) string {
	return v.Str(key).
		Min(10, "v.pw_min").Max(128, "v.pw_max").
		Regex(reLower, "v.pw_lower").Regex(reUpper, "v.pw_upper").Regex(reDigit, "v.pw_digit").Get()
}

// TempPassword returns a policy-compliant temporary password (14 chars + guaranteed classes).
func TempPassword() string {
	s := strings.NewReplacer("-", "x", "_", "x").Replace(RandomToken(9))
	return s + "Aa7"
}
