package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/argon2"
	"gorm.io/gorm"

	authv1 "github.com/dso202/bank-platform/gen/go/bank/auth/v1"
)

// Argon2id parameters (OWASP minimum: m=19 MiB, t=2, p=1).
const (
	argonTime    = 2
	argonMemory  = 19 * 1024
	argonThreads = 1
	argonKeyLen  = 32
)

func hashPassword(pw string) string {
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

func checkPassword(encoded, pw string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func otpCode() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(1000000))
	return fmt.Sprintf("%06d", n.Int64())
}

// SigningKey persists the RSA key so tokens survive restarts when no key file is mounted.
type SigningKey struct {
	Kid        string `gorm:"primaryKey"`
	PrivatePEM string `gorm:"not null"`
	CreatedAt  time.Time
}

type keySet struct {
	kid  string
	priv *rsa.PrivateKey
}

// loadOrCreateKey prefers a mounted PEM file (Kubernetes Secret); otherwise it
// reuses or creates a key stored in auth_db.
func loadOrCreateKey(db *gorm.DB, file string) (*keySet, error) {
	parse := func(p []byte) (*rsa.PrivateKey, error) {
		block, _ := pem.Decode(p)
		if block == nil {
			return nil, errors.New("invalid PEM")
		}
		if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
			return k, nil
		}
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("not an RSA key")
		}
		return rk, nil
	}
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		k, err := parse(b)
		if err != nil {
			return nil, err
		}
		return &keySet{kid: kidOf(&k.PublicKey), priv: k}, nil
	}
	var sk SigningKey
	if err := db.Order("created_at DESC").First(&sk).Error; err == nil {
		k, err := parse([]byte(sk.PrivatePEM))
		if err != nil {
			return nil, err
		}
		return &keySet{kid: sk.Kid, priv: k}, nil
	}
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	p := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	sk = SigningKey{Kid: kidOf(&k.PublicKey), PrivatePEM: string(p)}
	if err := db.Create(&sk).Error; err != nil {
		// Another replica created one first; use it.
		return loadOrCreateKey(db, "")
	}
	return &keySet{kid: sk.Kid, priv: k}, nil
}

func kidOf(pub *rsa.PublicKey) string {
	h := sha256.Sum256(pub.N.Bytes())
	return hex.EncodeToString(h[:8])
}

func (k *keySet) jwk() *authv1.Jwk {
	pub := k.priv.PublicKey
	return &authv1.Jwk{
		Kid: k.kid, Kty: "RSA", Alg: "RS256", Use: "sig",
		N: base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		E: base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}
}

// Claims are the JWT claims the gateway relies on.
type Claims struct {
	Email string   `json:"email"`
	Name  string   `json:"name"`
	Roles []string `json:"roles"`
	jwt.RegisteredClaims
}

func (k *keySet) sign(u *User, ttl time.Duration) (string, error) {
	now := time.Now()
	c := Claims{
		Email: u.Email, Name: u.FullName, Roles: u.roleList(),
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   u.ID,
			Issuer:    "bank-auth",
			Audience:  jwt.ClaimStrings{"bank-api"},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			ID:        randomToken()[:16],
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
	t.Header["kid"] = k.kid
	return t.SignedString(k.priv)
}
