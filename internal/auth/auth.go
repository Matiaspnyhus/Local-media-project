// Package auth holds password hashing and token helpers. It is deliberately
// small: everything that decides who may do what goes through here or
// through the route policies in internal/server.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters (OWASP's second recommended setting).
const (
	argonTime    = 2
	argonMemory  = 19 * 1024 // KiB
	argonThreads = 1
	argonKeyLen  = 32
)

// HashPassword returns an encoded argon2id hash.
func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword checks password against an encoded hash in constant time.
func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m, t, p uint64
	for _, kv := range strings.Split(parts[3], ",") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return false
		}
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return false
		}
		switch k {
		case "m":
			m = n
		case "t":
			t = n
		case "p":
			p = n
		}
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil || m == 0 || t == 0 || p == 0 || p > 255 || m > 1<<21 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, uint32(t), uint32(m), uint8(p), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// NewToken returns a random secret for a session cookie and the hash that
// is stored in the database. The secret itself is never stored.
func NewToken() (secret, hash string) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("auth: no randomness: " + err.Error())
	}
	secret = base64.RawURLEncoding.EncodeToString(b)
	return secret, HashToken(secret)
}

// HashToken maps a token secret to its stored form.
func HashToken(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// ValidatePassword applies the one password rule: long enough to resist
// guessing over a home network.
func ValidatePassword(p string) error {
	if len(p) < 8 {
		return errors.New("the password needs at least 8 characters")
	}
	if len(p) > 256 {
		return errors.New("the password is too long")
	}
	return nil
}

// SignLink returns the signature for a stream link: the file, the moment it
// expires and the session it was issued to are all covered.
func SignLink(key []byte, fileID string, expires int64, sessionID string) string {
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "stream\n%s\n%d\n%s", fileID, expires, sessionID)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// VerifyLink checks a signature in constant time.
func VerifyLink(key []byte, fileID string, expires int64, sessionID, sig string) bool {
	want := SignLink(key, fileID, expires, sessionID)
	return subtle.ConstantTimeCompare([]byte(want), []byte(sig)) == 1
}
