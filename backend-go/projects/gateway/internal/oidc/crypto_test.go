// crypto_test.go covers the OIDC value encryption, RS256 signing material,
// ID-token signing, subject derivation and PKCE verification with strict
// Node-semantics assertions: envelope layout, deterministic HMAC vectors and
// the RFC 7636 Appendix-B PKCE vector are all pinned to byte-exact values.
package oidc

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"
)

// pkceTestVerifier is a charset-valid 43-char verifier used across the suite.
const pkceTestVerifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"

// pkceChallengeOf mirrors sha256Base64Url from the Node repository.
func pkceChallengeOf(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func sealNodeEnvelope(t *testing.T, secret string, plaintext []byte) string {
	t.Helper()
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatalf("aes cipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("aes-gcm: %v", err)
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		t.Fatalf("rand: %v", err)
	}
	sealed := gcm.Seal(nil, iv, plaintext, nil)
	ciphertext, tag := sealed[:len(sealed)-gcm.Overhead()], sealed[len(sealed)-gcm.Overhead():]
	encode := base64.RawURLEncoding
	return encode.EncodeToString(iv) + "." + encode.EncodeToString(tag) + "." + encode.EncodeToString(ciphertext)
}

func openNodeEnvelope(t *testing.T, secret, envelope string) []byte {
	t.Helper()
	parts := strings.Split(envelope, ".")
	if len(parts) != 3 {
		t.Fatalf("envelope %q does not have 3 segments", envelope)
	}
	decode := base64.RawURLEncoding
	iv, err := decode.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("iv: %v", err)
	}
	tag, err := decode.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("tag: %v", err)
	}
	ciphertext, err := decode.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("ciphertext: %v", err)
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatalf("aes cipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("aes-gcm: %v", err)
	}
	plain, err := gcm.Open(nil, iv, append(ciphertext, tag...), nil)
	if err != nil {
		t.Fatalf("open envelope: %v", err)
	}
	return plain
}

// ---------------------------------------------------------------------------
// Value encryption.
// ---------------------------------------------------------------------------

func TestEncryptDecryptOidcValueRoundTrip(t *testing.T) {
	cases := []struct {
		name  string
		value any
	}{
		{"string", "hello"},
		{"empty string", ""},
		{"number", float64(42)},
		{"object", map[string]any{"state": "s1", "csrfToken": "c1", "nonce": nil}},
		{"nested object", map[string]string{"nonce": "n-1"}},
		{"array", []string{"openid", "profile"}},
		{"bool", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			envelope, err := EncryptOidcValue(oidcTestSecret, tc.value)
			if err != nil {
				t.Fatalf("EncryptOidcValue: %v", err)
			}
			var got any
			if err := DecryptOidcValue(oidcTestSecret, envelope, &got); err != nil {
				t.Fatalf("DecryptOidcValue: %v", err)
			}
			want, _ := json.Marshal(tc.value)
			gotJSON, _ := json.Marshal(got)
			if string(want) != string(gotJSON) {
				t.Fatalf("round trip = %s, want %s", gotJSON, want)
			}
		})
	}
}

// TestOidcValueEnvelopeMatchesNodeFormat proves the Go output is the exact
// Node layout "iv.tag.ciphertext" (raw base64url, no version tag) by opening
// it with a hand-rolled Node-style AES-GCM reader, and that a hand-sealed
// Node envelope decrypts through DecryptOidcValue.
func TestOidcValueEnvelopeMatchesNodeFormat(t *testing.T) {
	plaintext, _ := json.Marshal(map[string]string{"nonce": "compat"})
	envelope, err := EncryptOidcValue(oidcTestSecret, map[string]string{"nonce": "compat"})
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	got := openNodeEnvelope(t, oidcTestSecret, envelope)
	if string(got) != string(plaintext) {
		t.Fatalf("node-style open = %s, want %s", got, plaintext)
	}

	resealed := sealNodeEnvelope(t, oidcTestSecret, plaintext)
	var decoded map[string]string
	if err := DecryptOidcValue(oidcTestSecret, resealed, &decoded); err != nil {
		t.Fatalf("decrypt node envelope: %v", err)
	}
	if decoded["nonce"] != "compat" {
		t.Fatalf("decoded = %v", decoded)
	}
}

func TestEncryptOidcValueUsesRandomIV(t *testing.T) {
	first, err := EncryptOidcValue(oidcTestSecret, "same")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	second, err := EncryptOidcValue(oidcTestSecret, "same")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if first == second {
		t.Fatal("two encryptions of the same value produced identical envelopes")
	}
	if iv := strings.Split(first, ".")[0]; iv == strings.Split(second, ".")[0] {
		t.Fatal("IV reuse detected")
	}
}

func TestOidcValueErrors(t *testing.T) {
	envelope, err := EncryptOidcValue(oidcTestSecret, map[string]string{"k": "v"})
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	if _, err := EncryptOidcValue("", "x"); err == nil || err.Error() != "OIDC 事务加密密钥未配置" {
		t.Fatalf("empty secret encrypt error = %v", err)
	}
	var target map[string]string
	if err := DecryptOidcValue("", envelope, &target); err == nil || err.Error() != "OIDC 事务加密密钥未配置" {
		t.Fatalf("empty secret decrypt error = %v", err)
	}

	// Wrong secret: GCM authentication failure → default OidcCiphertextError.
	var wrong *OidcCiphertextError
	if err := DecryptOidcValue("other-secret", envelope, &target); !errors.As(err, &wrong) || wrong.Error() != "OIDC 密文无法读取" {
		t.Fatalf("wrong secret error = %v", err)
	}

	// Tampered ciphertext body.
	parts := strings.Split(envelope, ".")
	tampered := parts[0] + "." + parts[1] + ".AAAA" + parts[2][4:]
	if err := DecryptOidcValue(oidcTestSecret, tampered, &target); err == nil || err.Error() != "OIDC 密文无法读取" {
		t.Fatalf("tampered error = %v", err)
	}

	formatCases := []struct {
		name     string
		envelope string
	}{
		{"two segments", "aaa.bbb"},
		{"four segments", "aaa.bbb.ccc.ddd"},
		{"empty iv", ".bbb.ccc"},
		{"empty tag", "aaa..ccc"},
		{"empty ciphertext", "aaa.bbb."},
		{"bad base64 iv", "!!!.bbb.ccc"},
	}
	for _, tc := range formatCases {
		t.Run(tc.name, func(t *testing.T) {
			var cerr *OidcCiphertextError
			if err := DecryptOidcValue(oidcTestSecret, tc.envelope, &target); !errors.As(err, &cerr) || cerr.Error() != "OIDC 密文格式无效" {
				t.Fatalf("format error = %v", err)
			}
		})
	}

	// Empty envelope string.
	if err := DecryptOidcValue(oidcTestSecret, "", &target); err == nil || err.Error() != "OIDC 密文格式无效" {
		t.Fatalf("empty envelope error = %v", err)
	}
	// Non-JSON plaintext inside a valid envelope.
	valid := sealNodeEnvelope(t, oidcTestSecret, []byte("not-json"))
	if err := DecryptOidcValue(oidcTestSecret, valid, &target); err == nil {
		t.Fatal("json unmarshal failure must surface")
	}
}

// ---------------------------------------------------------------------------
// Signing key material.
// ---------------------------------------------------------------------------

func TestBase64RawURLUint(t *testing.T) {
	cases := []struct {
		value *big.Int
		want  string
	}{
		{big.NewInt(1), "AQ"},
		{big.NewInt(65537), "AQAB"},
		{big.NewInt(255), "_w"},
		{big.NewInt(0), ""},
	}
	for _, tc := range cases {
		if got := base64RawURLUint(tc.value); got != tc.want {
			t.Fatalf("base64RawURLUint(%v) = %q, want %q", tc.value, got, tc.want)
		}
	}
}

func TestCreateSigningKeyMaterial(t *testing.T) {
	material, err := CreateSigningKeyMaterial(oidcTestSecret, "kid-1")
	if err != nil {
		t.Fatalf("CreateSigningKeyMaterial: %v", err)
	}
	jwk := material.PublicJWK
	if jwk["kty"] != "RSA" || jwk["use"] != "sig" || jwk["alg"] != "RS256" || jwk["kid"] != "kid-1" {
		t.Fatalf("jwk fixed fields = %v", jwk)
	}
	if jwk["e"] != "AQAB" {
		t.Fatalf("jwk e = %v", jwk["e"])
	}

	var payload struct {
		PrivateKeyPem string `json:"privateKeyPem"`
	}
	if err := DecryptOidcValue(oidcTestSecret, material.PrivateKeyCiphertext, &payload); err != nil {
		t.Fatalf("decrypt key ciphertext: %v", err)
	}
	block, _ := pem.Decode([]byte(payload.PrivateKeyPem))
	if block == nil || block.Type != "PRIVATE KEY" {
		t.Fatalf("pem block = %+v", block)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse pkcs8: %v", err)
	}
	rsaKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		t.Fatalf("pkcs8 = %T", parsed)
	}
	if rsaKey.N.BitLen() != 2048 {
		t.Fatalf("modulus bits = %d", rsaKey.N.BitLen())
	}
	// The exported JWK n matches the private modulus exactly.
	nBytes := rsaKey.N.Bytes()
	wantN := base64.RawURLEncoding.EncodeToString(nBytes)
	if jwk["n"] != wantN {
		t.Fatal("jwk n does not match the private modulus")
	}
	// Round-trip the public key: JWK → rsa.PublicKey verifies a signature.
	n, err := base64.RawURLEncoding.DecodeString(jwk["n"].(string))
	if err != nil {
		t.Fatalf("decode n: %v", err)
	}
	e, err := base64.RawURLEncoding.DecodeString(jwk["e"].(string))
	if err != nil {
		t.Fatalf("decode e: %v", err)
	}
	public := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	digest := sha256.Sum256([]byte("probe"))
	signature, err := rsa.SignPKCS1v15(rand.Reader, rsaKey, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign probe: %v", err)
	}
	if err := rsa.VerifyPKCS1v15(public, crypto.SHA256, digest[:], signature); err != nil {
		t.Fatalf("verify probe with jwk key: %v", err)
	}
}

func TestCreateSigningKeyMaterialRequiresSecret(t *testing.T) {
	if _, err := CreateSigningKeyMaterial("", "kid"); err == nil || err.Error() != "OIDC 事务加密密钥未配置" {
		t.Fatalf("empty secret error = %v", err)
	}
}

// ---------------------------------------------------------------------------
// ID-token signing.
// ---------------------------------------------------------------------------

func signTestToken(t *testing.T, expiresAtSeconds int64, nonce string, now time.Time) string {
	t.Helper()
	material, err := CreateSigningKeyMaterial(oidcTestSecret, "kid-s")
	if err != nil {
		t.Fatalf("material: %v", err)
	}
	token, err := SignIDToken(oidcTestSecret, material.PrivateKeyCiphertext, "kid-s",
		oidcTestIssuer, "client-1", "sub-1", expiresAtSeconds, nonce, now)
	if err != nil {
		t.Fatalf("SignIDToken: %v", err)
	}
	return token
}

func parseSignedToken(t *testing.T, token string) (header map[string]any, claims map[string]any, signingInput string, signature []byte) {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts", len(parts))
	}
	decode := func(part string, target any) {
		raw, err := base64.RawURLEncoding.DecodeString(part)
		if err != nil {
			t.Fatalf("decode part: %v", err)
		}
		if err := json.Unmarshal(raw, target); err != nil {
			t.Fatalf("unmarshal part: %v", err)
		}
	}
	decode(parts[0], &header)
	decode(parts[1], &claims)
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	return header, claims, parts[0] + "." + parts[1], signature
}

func TestSignIDTokenCompactJWS(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	material, err := CreateSigningKeyMaterial(oidcTestSecret, "kid-s")
	if err != nil {
		t.Fatalf("material: %v", err)
	}
	token, err := SignIDToken(oidcTestSecret, material.PrivateKeyCiphertext, "kid-s",
		oidcTestIssuer, "client-1", "sub-1", now.Add(5*time.Minute).Unix(), "nonce-xyz", now)
	if err != nil {
		t.Fatalf("SignIDToken: %v", err)
	}

	header, claims, signingInput, signature := parseSignedToken(t, token)
	if header["alg"] != "RS256" || header["kid"] != "kid-s" || header["typ"] != "JWT" {
		t.Fatalf("header = %v", header)
	}
	if claims["iss"] != oidcTestIssuer || claims["aud"] != "client-1" || claims["sub"] != "sub-1" {
		t.Fatalf("claims = %v", claims)
	}
	if claims["iat"] != float64(now.Unix()) {
		t.Fatalf("iat = %v", claims["iat"])
	}
	if claims["exp"] != float64(now.Add(5*time.Minute).Unix()) {
		t.Fatalf("exp = %v", claims["exp"])
	}
	if claims["nonce"] != "nonce-xyz" {
		t.Fatalf("nonce = %v", claims["nonce"])
	}

	// Verify the RS256 signature against the same signing key material.
	var payload struct {
		PrivateKeyPem string `json:"privateKeyPem"`
	}
	if err := DecryptOidcValue(oidcTestSecret, material.PrivateKeyCiphertext, &payload); err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	block, _ := pem.Decode([]byte(payload.PrivateKeyPem))
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	private, ok := key.(*rsa.PrivateKey)
	if !ok {
		t.Fatalf("pkcs8 = %T", key)
	}
	digest := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(&private.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Fatalf("signature verification: %v", err)
	}
}

func TestSignIDTokenSecondPrecisionAndNoNonce(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 500_000_000, time.UTC)
	token := signTestToken(t, now.Add(90*time.Second).Unix(), "", now)
	_, claims, _, _ := parseSignedToken(t, token)
	// Milliseconds in `now` must not leak into iat.
	if claims["iat"] != float64(now.Unix()) {
		t.Fatalf("iat = %v", claims["iat"])
	}
	if _, present := claims["nonce"]; present {
		t.Fatal("empty nonce must be omitted from the claims")
	}
}

func TestSignIDTokenErrors(t *testing.T) {
	now := time.Now()

	// A well-formed envelope sealed with a different key → default
	// OidcCiphertextError (GCM authentication failure).
	foreign, err := CreateSigningKeyMaterial("another-secret", "kid-e")
	if err != nil {
		t.Fatalf("foreign material: %v", err)
	}
	var cerr *OidcCiphertextError
	if _, err := SignIDToken(oidcTestSecret, foreign.PrivateKeyCiphertext, "kid-e", "i", "a", "s", 1, "", now); !errors.As(err, &cerr) || cerr.Error() != "OIDC 密文无法读取" {
		t.Fatalf("wrong-key ciphertext error = %v", err)
	}

	// Valid envelope without the PEM payload.
	missing, err := EncryptOidcValue(oidcTestSecret, map[string]string{})
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := SignIDToken(oidcTestSecret, missing, "kid-e", "i", "a", "s", 1, "", now); err == nil || err.Error() != "OIDC 签名私钥内容无效" {
		t.Fatalf("missing pem error = %v", err)
	}

	// Envelope whose PEM is not parseable.
	badPem, err := EncryptOidcValue(oidcTestSecret, map[string]string{"privateKeyPem": "not a pem"})
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := SignIDToken(oidcTestSecret, badPem, "kid-e", "i", "a", "s", 1, "", now); err == nil || err.Error() != "OIDC 签名私钥内容无效" {
		t.Fatalf("bad pem error = %v", err)
	}

	// A non-RSA PKCS8 key is rejected.
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ec key: %v", err)
	}
	ecDer, err := x509.MarshalPKCS8PrivateKey(ecKey)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	ecPem := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecDer})
	ecEnvelope, err := EncryptOidcValue(oidcTestSecret, map[string]string{"privateKeyPem": string(ecPem)})
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := SignIDToken(oidcTestSecret, ecEnvelope, "kid-e", "i", "a", "s", 1, "", now); err == nil || err.Error() != "OIDC 签名私钥内容无效" {
		t.Fatalf("non-rsa error = %v", err)
	}

	// Empty envelope payload string.
	if _, err := SignIDToken(oidcTestSecret, "", "kid-e", "i", "a", "s", 1, "", now); err == nil || err.Error() != "OIDC 密文格式无效" {
		t.Fatalf("empty ciphertext error = %v", err)
	}
}

func TestAssertSigningKeyUsable(t *testing.T) {
	material, err := CreateSigningKeyMaterial(oidcTestSecret, "kid-ok")
	if err != nil {
		t.Fatalf("material: %v", err)
	}
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	if err := AssertSigningKeyUsable(oidcTestSecret, material.PrivateKeyCiphertext, "kid-ok", oidcTestIssuer, now); err != nil {
		t.Fatalf("usable key rejected: %v", err)
	}
	if err := AssertSigningKeyUsable(oidcTestSecret, "broken-envelope", "kid-ok", oidcTestIssuer, now); err == nil {
		t.Fatal("broken key accepted")
	}
}

// ---------------------------------------------------------------------------
// Subject derivation.
// ---------------------------------------------------------------------------

func TestOidcSubjectForSystemAccountVector(t *testing.T) {
	// Pre-computed with the Node semantics:
	// HMAC-SHA256(secret, "juhe-ai:oidc-subject:v1\0" + issuer + "\0" + id).
	const want = "UriwfspfBbL9vhJoWC1wTv_mmAG0ieDVjsmr1YMwDxc"
	got, err := OidcSubjectForSystemAccount(oidcTestSecret, oidcTestIssuer, "acc-1")
	if err != nil {
		t.Fatalf("OidcSubjectForSystemAccount: %v", err)
	}
	if got != want {
		t.Fatalf("subject = %q, want %q", got, want)
	}
	// Deterministic.
	again, _ := OidcSubjectForSystemAccount(oidcTestSecret, oidcTestIssuer, "acc-1")
	if again != got {
		t.Fatal("subject derivation is not deterministic")
	}
	// Distinct per account and issuer.
	otherAccount, _ := OidcSubjectForSystemAccount(oidcTestSecret, oidcTestIssuer, "acc-2")
	if otherAccount == got {
		t.Fatal("different accounts must derive different subjects")
	}
	otherIssuer, _ := OidcSubjectForSystemAccount(oidcTestSecret, "https://other.example.com", "acc-1")
	if otherIssuer == got {
		t.Fatal("different issuers must derive different subjects")
	}
	// Independent recomputation with the standard library pins the format.
	mac := hmac.New(sha256.New, []byte(oidcTestSecret))
	mac.Write([]byte("juhe-ai:oidc-subject:v1\x00"))
	mac.Write([]byte(oidcTestIssuer))
	mac.Write([]byte("\x00"))
	mac.Write([]byte("acc-1"))
	if base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) != want {
		t.Fatal("HMAC format drifted from the domain-separated prefix contract")
	}
}

func TestOidcSubjectForSystemAccountErrors(t *testing.T) {
	if _, err := OidcSubjectForSystemAccount("", oidcTestIssuer, "acc"); err == nil || err.Error() != "OIDC issuer 或 subject 派生密钥未配置" {
		t.Fatalf("empty secret error = %v", err)
	}
	if _, err := OidcSubjectForSystemAccount(oidcTestSecret, "", "acc"); err == nil || err.Error() != "OIDC issuer 或 subject 派生密钥未配置" {
		t.Fatalf("empty issuer error = %v", err)
	}
}

// ---------------------------------------------------------------------------
// PKCE.
// ---------------------------------------------------------------------------

func TestVerifyPKCE(t *testing.T) {
	cases := []struct {
		name      string
		verifier  string
		challenge string
		want      bool
	}{
		{"RFC 7636 Appendix B vector", pkceTestVerifier, "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM", true},
		{"43 chars boundary", strings.Repeat("a", 43), pkceChallengeOf(strings.Repeat("a", 43)), true},
		{"128 chars boundary", strings.Repeat("a", 128), pkceChallengeOf(strings.Repeat("a", 128)), true},
		{"42 chars too short", strings.Repeat("a", 42), pkceChallengeOf(strings.Repeat("a", 42)), false},
		{"129 chars too long", strings.Repeat("a", 129), pkceChallengeOf(strings.Repeat("a", 129)), false},
		{"invalid charset", strings.Repeat("a", 42) + "!", pkceChallengeOf(strings.Repeat("a", 43)), false},
		{"wrong challenge", pkceTestVerifier, pkceChallengeOf("other-verifier"), false},
		{"empty challenge", pkceTestVerifier, "", false},
		{"empty verifier", "", "", false},
		{"unicode verifier", strings.Repeat("ä", 43), pkceChallengeOf(strings.Repeat("ä", 43)), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := VerifyPKCE(tc.verifier, tc.challenge); got != tc.want {
				t.Fatalf("VerifyPKCE(%q, %q) = %v, want %v", tc.verifier, tc.challenge, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Hash-secret compat (shared with the Node storage/crypto.ts contract).
// ---------------------------------------------------------------------------

func TestHashSecretNodeCompat(t *testing.T) {
	// sha256("test") in hex, exactly like Node createHash('sha256').digest('hex').
	if got := hashSecret("test"); got != "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08" {
		t.Fatalf("hashSecret(test) = %q", got)
	}
	if got := hashSecret(""); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("hashSecret(empty) = %q", got)
	}
}
