// Package turn implements a built-in TURN relay server and HMAC-SHA1
// time-limited credential generation for WebRTC NAT traversal.
package turn

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"time"
)

// CredentialGenerator creates time-limited TURN credentials using HMAC-SHA1
// as specified in RFC 5766 long-term credential mechanism with time-limited
// credentials (draft-uberti-behave-turn-rest-00).
type CredentialGenerator struct {
	// sharedSecret is the secret key shared between TURN server and credential issuer.
	sharedSecret string
	// ttlSeconds is the credential validity duration in seconds.
	ttlSeconds int
	// realm is the TURN server realm.
	realm string
}

// NewCredentialGenerator creates a credential generator with the specified
// shared secret, TTL, and realm.
func NewCredentialGenerator(sharedSecret string, ttlSeconds int, realm string) *CredentialGenerator {
	return &CredentialGenerator{
		sharedSecret: sharedSecret,
		ttlSeconds:   ttlSeconds,
		realm:        realm,
	}
}

// Credential holds a time-limited TURN credential pair.
type Credential struct {
	Username   string    `json:"username"`
	Password   string    `json:"password"`
	TTL        int       `json:"ttl"`
	ExpiresAt  time.Time `json:"expires_at"`
	URLs       []string  `json:"urls"`
}

// Generate creates a new time-limited TURN credential for the given user ID.
// The username encodes the expiration timestamp and user identity.
// The password is computed as HMAC-SHA1(sharedSecret, username).
func (cg *CredentialGenerator) Generate(userID string, turnURLs []string) *Credential {
	expiresAt := time.Now().Add(time.Duration(cg.ttlSeconds) * time.Second)
	unixExpiry := expiresAt.Unix()

	// Username format: "timestamp:userID" as per TURN REST API convention
	username := fmt.Sprintf("%d:%s", unixExpiry, userID)

	// Compute HMAC-SHA1
	mac := hmac.New(sha1.New, []byte(cg.sharedSecret))
	mac.Write([]byte(username))
	password := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	return &Credential{
		Username:  username,
		Password:  password,
		TTL:       cg.ttlSeconds,
		ExpiresAt: expiresAt,
		URLs:      turnURLs,
	}
}

// Validate checks if a username/password credential pair is valid.
// Returns true if the credential is correctly signed and not expired.
func (cg *CredentialGenerator) Validate(username, password string) bool {
	// Compute expected password
	mac := hmac.New(sha1.New, []byte(cg.sharedSecret))
	mac.Write([]byte(username))
	expectedPassword := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(password), []byte(expectedPassword)) {
		return false
	}

	// Check expiry (username format: "timestamp:userID")
	var expiry int64
	_, err := fmt.Sscanf(username, "%d:", &expiry)
	if err != nil {
		return false
	}

	return time.Now().Unix() < expiry
}
