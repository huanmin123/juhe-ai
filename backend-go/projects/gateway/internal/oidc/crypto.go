// Package oidc implements the P04 vertical slice: the OIDC provider public
// protocol surface ported from backend/src/modules/oidc-provider
// (oidc-provider.routes.ts + oidc-provider.repository.ts + oidc-provider.crypto.ts
// + oidc-rate-limit.middleware.ts) over the oauth_* business tables. The
// admin/management reads and writes already landed with M16c
// (internal/policyreads oauth.go); this package only owns the public face:
// discovery, jwks, authorize (+consent decision), device flow, token, token
// renewal, revoke and userinfo, with RS256 ID-token signing backed by the
// encrypted oauth_signing_keys private key.
//
// Storage contract: every secret at rest (private keys, client secrets,
// state/csrf envelopes, nonces) is sealed with the OIDC value encryption
// (OIDC_KEY_ENCRYPTION_SECRET semantics) exactly like Node.
package oidc

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// OidcCiphertextError mirrors Node OidcCiphertextError: a stored OIDC
// ciphertext cannot be read back (wrong key, corrupted envelope).
type OidcCiphertextError struct{ Message string }

func (e *OidcCiphertextError) Error() string {
	if e.Message == "" {
		return "OIDC 密文无法读取"
	}
	return e.Message
}

// ---------------------------------------------------------------------------
// OIDC value encryption (oidc-provider.crypto.ts encryptOidcValue /
// decryptOidcValue): AES-256-GCM keyed by sha256(keyEncryptionSecret), sealed
// as "iv.tag.ciphertext" with raw base64url parts and no version tag.
// ---------------------------------------------------------------------------

func oidcGCM(secret string) (cipher.AEAD, error) {
	if secret == "" {
		return nil, errors.New("OIDC 事务加密密钥未配置")
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// EncryptOidcValue mirrors encryptOidcValue for any JSON-serializable value.
func EncryptOidcValue(secret string, value any) (string, error) {
	plain, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	gcm, err := oidcGCM(secret)
	if err != nil {
		return "", err
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, iv, plain, nil)
	ciphertext, tag := sealed[:len(sealed)-gcm.Overhead()], sealed[len(sealed)-gcm.Overhead():]
	encode := base64.RawURLEncoding
	return encode.EncodeToString(iv) + "." + encode.EncodeToString(tag) + "." + encode.EncodeToString(ciphertext), nil
}

// DecryptOidcValue mirrors decryptOidcValue into a JSON-decodable target.
func DecryptOidcValue(secret, envelope string, target any) error {
	parts := strings.Split(envelope, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return &OidcCiphertextError{Message: "OIDC 密文格式无效"}
	}
	decode := func(value string) ([]byte, error) {
		raw, err := base64.RawURLEncoding.DecodeString(value)
		if err != nil {
			return nil, &OidcCiphertextError{Message: "OIDC 密文格式无效"}
		}
		return raw, nil
	}
	iv, err := decode(parts[0])
	if err != nil {
		return err
	}
	tag, err := decode(parts[1])
	if err != nil {
		return err
	}
	ciphertext, err := decode(parts[2])
	if err != nil {
		return err
	}
	gcm, err := oidcGCM(secret)
	if err != nil {
		return err
	}
	plain, err := gcm.Open(nil, iv, append(ciphertext, tag...), nil)
	if err != nil {
		return &OidcCiphertextError{}
	}
	return json.Unmarshal(plain, target)
}

// ---------------------------------------------------------------------------
// RS256 signing key material (createOidcSigningKeyMaterial): a 2048-bit RSA
// key pair; the PKCS#8 PEM is stored encrypted, the public part is exported
// as a JWK {kty, n, e, kid, use: sig, alg: RS256}.
// ---------------------------------------------------------------------------

// SigningKeyMaterial mirrors OidcSigningKeyMaterial.
type SigningKeyMaterial struct {
	PrivateKeyCiphertext string
	PublicJWK            map[string]any
}

// CreateSigningKeyMaterial mirrors createOidcSigningKeyMaterial.
func CreateSigningKeyMaterial(secret, kid string) (*SigningKeyMaterial, error) {
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return nil, err
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	ciphertext, err := EncryptOidcValue(secret, map[string]string{"privateKeyPem": string(pemKey)})
	if err != nil {
		return nil, err
	}
	return &SigningKeyMaterial{
		PrivateKeyCiphertext: ciphertext,
		PublicJWK: map[string]any{
			"kty": "RSA",
			"n":   base64RawURLUint(private.N),
			"e":   base64RawURLUint(big.NewInt(int64(private.E))),
			"kid": kid,
			"use": "sig",
			"alg": "RS256",
		},
	}, nil
}

// base64RawURLUint renders a JWK integer (minimal big-endian bytes, raw
// base64url, exactly like jose exportJWK).
func base64RawURLUint(value *big.Int) string {
	return base64.RawURLEncoding.EncodeToString(value.Bytes())
}

// ---------------------------------------------------------------------------
// ID-token signing (signOidcIdToken): compact JWS, protected header
// {alg: RS256, kid, typ: JWT}, claims iss/aud/sub/iat/exp (+nonce), RS256
// over the signing input with crypto/rsa + sha256.
// ---------------------------------------------------------------------------

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

type jwtClaims struct {
	Nonce string `json:"nonce,omitempty"`
	Iss   string `json:"iss"`
	Aud   string `json:"aud"`
	Sub   string `json:"sub"`
	Iat   int64  `json:"iat"`
	Exp   int64  `json:"exp"`
}

// loadSigningPrivateKey decrypts private_key_ciphertext into an *rsa.PrivateKey.
func loadSigningPrivateKey(secret, privateKeyCiphertext string) (*rsa.PrivateKey, error) {
	var payload struct {
		PrivateKeyPem string `json:"privateKeyPem"`
	}
	if err := DecryptOidcValue(secret, privateKeyCiphertext, &payload); err != nil {
		return nil, err
	}
	if payload.PrivateKeyPem == "" {
		return nil, errors.New("OIDC 签名私钥内容无效")
	}
	block, _ := pem.Decode([]byte(payload.PrivateKeyPem))
	if block == nil {
		return nil, errors.New("OIDC 签名私钥内容无效")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("OIDC 签名私钥内容无效")
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("OIDC 签名私钥内容无效")
	}
	return rsaKey, nil
}

// SignIDToken mirrors signOidcIdToken. expiresAt is second-precision epoch
// (Node floors the RFC3339 input); now supplies iat.
func SignIDToken(secret, privateKeyCiphertext, kid, issuer, audience, subject string, expiresAtSeconds int64, nonce string, now time.Time) (string, error) {
	privateKey, err := loadSigningPrivateKey(secret, privateKeyCiphertext)
	if err != nil {
		return "", err
	}
	header, err := json.Marshal(jwtHeader{Alg: "RS256", Kid: kid, Typ: "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(jwtClaims{
		Nonce: nonce,
		Iss:   issuer,
		Aud:   audience,
		Sub:   subject,
		Iat:   now.Unix(),
		Exp:   expiresAtSeconds,
	})
	if err != nil {
		return "", err
	}
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// AssertSigningKeyUsable mirrors assertOidcSigningKeyUsable: a real RS256
// signature so a broken encrypted key fails before a one-time code is
// consumed (the caller can retry after the operator repairs the key).
func AssertSigningKeyUsable(secret, privateKeyCiphertext, kid, issuer string, now time.Time) error {
	_, err := SignIDToken(secret, privateKeyCiphertext, kid, issuer,
		"juhe-ai-oidc-preflight", "juhe-ai-oidc-preflight", now.Add(time.Minute).Unix(), "", now)
	return err
}

// OidcSubjectForSystemAccount mirrors oidcSubjectForSystemAccount:
// HMAC-SHA256 keyed by the raw keyEncryptionSecret over the versioned domain
// separation prefix, the issuer and the account id, rendered base64url.
func OidcSubjectForSystemAccount(secret, issuer, systemAccountID string) (string, error) {
	if secret == "" || issuer == "" {
		return "", errors.New("OIDC issuer 或 subject 派生密钥未配置")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("juhe-ai:oidc-subject:v1\x00"))
	mac.Write([]byte(issuer))
	mac.Write([]byte("\x00"))
	mac.Write([]byte(systemAccountID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// VerifyPKCE mirrors verifyPkce: S256(verifier) == challenge for a
// charset-valid verifier.
func VerifyPKCE(verifier, expectedChallenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	for i := 0; i < len(verifier); i++ {
		c := verifier[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
			c == '-' || c == '.' || c == '_' || c == '~') {
			return false
		}
	}
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:]) == expectedChallenge
}

// randomBase64URLBytes mirrors randomBytes(n).toString('base64url').
func randomBase64URLBytes(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("oidc: random source failed: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}
