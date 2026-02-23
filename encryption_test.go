package avoinspector

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"testing"
)

// generateTestKeyPair creates a P-256 key pair for testing.
func generateTestKeyPair(t *testing.T) (*ecdh.PrivateKey, *ecdh.PublicKey) {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key pair: %v", err)
	}
	return priv, priv.PublicKey()
}

// uncompressedPublicKeyBytes returns the 65-byte uncompressed public key (0x04 || X || Y).
func uncompressedPublicKeyBytes(pub *ecdh.PublicKey) []byte {
	// crypto/ecdh PublicKey.Bytes() returns uncompressed point for NIST curves
	return pub.Bytes()
}

// --- shouldEncrypt truth table ---

func TestShouldEncrypt_DevWithKey(t *testing.T) {
	if !shouldEncrypt("dev", "some-key") {
		t.Error("expected shouldEncrypt=true for dev with key")
	}
}

func TestShouldEncrypt_StagingWithKey(t *testing.T) {
	if !shouldEncrypt("staging", "some-key") {
		t.Error("expected shouldEncrypt=true for staging with key")
	}
}

func TestShouldEncrypt_ProdWithKey(t *testing.T) {
	if shouldEncrypt("prod", "some-key") {
		t.Error("expected shouldEncrypt=false for prod (even with key)")
	}
}

func TestShouldEncrypt_DevEmptyKey(t *testing.T) {
	if shouldEncrypt("dev", "") {
		t.Error("expected shouldEncrypt=false for dev with empty key")
	}
}

func TestShouldEncrypt_StagingEmptyKey(t *testing.T) {
	if shouldEncrypt("staging", "") {
		t.Error("expected shouldEncrypt=false for staging with empty key")
	}
}

func TestShouldEncrypt_ProdEmptyKey(t *testing.T) {
	if shouldEncrypt("prod", "") {
		t.Error("expected shouldEncrypt=false for prod with empty key")
	}
}

// --- Wire format tests ---

func TestEncryptPropertyValue_WireFormat(t *testing.T) {
	priv, pub := generateTestKeyPair(t)
	pubKeyHex := hex.EncodeToString(uncompressedPublicKeyBytes(pub))

	plaintext := "hello world"
	cipherB64, err := encryptPropertyValue(plaintext, pubKeyHex)
	if err != nil {
		t.Fatalf("encryption failed: %v", err)
	}

	wire, err := base64.StdEncoding.DecodeString(cipherB64)
	if err != nil {
		t.Fatalf("base64 decode failed: %v", err)
	}

	// AC7: len(base64Decode(output)) >= 99
	if len(wire) < 99 {
		t.Errorf("wire length %d < 99", len(wire))
	}

	// AC7: output[0] == 0x00
	if wire[0] != 0x00 {
		t.Errorf("wire[0] = 0x%02x, want 0x00", wire[0])
	}

	// AC7: output[1] == 0x04
	if wire[1] != 0x04 {
		t.Errorf("wire[1] = 0x%02x, want 0x04", wire[1])
	}

	// Round-trip decryption
	decrypted, err := decryptForTest(cipherB64, priv)
	if err != nil {
		t.Fatalf("decryption failed: %v", err)
	}
	if decrypted != plaintext {
		t.Errorf("round-trip failed: got %q, want %q", decrypted, plaintext)
	}
}

func TestEncryptPropertyValue_EmptyString(t *testing.T) {
	priv, pub := generateTestKeyPair(t)
	pubKeyHex := hex.EncodeToString(uncompressedPublicKeyBytes(pub))

	cipherB64, err := encryptPropertyValue("", pubKeyHex)
	if err != nil {
		t.Fatalf("encryption failed: %v", err)
	}

	decrypted, err := decryptForTest(cipherB64, priv)
	if err != nil {
		t.Fatalf("decryption failed: %v", err)
	}
	if decrypted != "" {
		t.Errorf("round-trip of empty string failed: got %q", decrypted)
	}
}

func TestEncryptPropertyValue_InvalidPublicKey(t *testing.T) {
	_, err := encryptPropertyValue("test", "not-valid-hex-key!!")
	if err == nil {
		t.Error("expected error for invalid public key")
	}
}

// AC2: Standard 12-byte GCM FAILS interop decryption
func TestStandard12ByteGCM_FailsInterop(t *testing.T) {
	priv, pub := generateTestKeyPair(t)
	pubKeyHex := hex.EncodeToString(uncompressedPublicKeyBytes(pub))

	// Encrypt with the correct 16-byte nonce implementation
	cipherB64, err := encryptPropertyValue("test data", pubKeyHex)
	if err != nil {
		t.Fatalf("encryption failed: %v", err)
	}

	// Attempt to decrypt using standard 12-byte GCM — this MUST fail
	err = decryptWith12ByteGCM(cipherB64, priv)
	if err == nil {
		t.Fatal("expected 12-byte GCM decryption to FAIL (proving 16-byte nonce is required for interop)")
	}
}

// AC8: Cross-SDK interop structure test
func TestCrossSDK_WireFormatStructure(t *testing.T) {
	_, pub := generateTestKeyPair(t)
	pubKeyHex := hex.EncodeToString(uncompressedPublicKeyBytes(pub))

	cipherB64, err := encryptPropertyValue("cross-sdk test value", pubKeyHex)
	if err != nil {
		t.Fatalf("encryption failed: %v", err)
	}

	wire, err := base64.StdEncoding.DecodeString(cipherB64)
	if err != nil {
		t.Fatalf("base64 decode failed: %v", err)
	}

	// Wire format: [0x00][65-byte uncompressed ephemeral pubkey][16-byte IV][16-byte auth tag][ciphertext]
	// Minimum: 1 + 65 + 16 + 16 + 1 = 99 bytes (for 1 byte plaintext)
	if len(wire) < 99 {
		t.Errorf("wire too short: %d bytes", len(wire))
	}

	// Byte 0: version marker
	if wire[0] != 0x00 {
		t.Errorf("expected version byte 0x00, got 0x%02x", wire[0])
	}

	// Byte 1: uncompressed point marker
	if wire[1] != 0x04 {
		t.Errorf("expected uncompressed point marker 0x04, got 0x%02x", wire[1])
	}

	// Ephemeral public key should be 65 bytes (bytes 1..65)
	ephemeralPubKeyBytes := wire[1:66]
	_, err = ecdh.P256().NewPublicKey(ephemeralPubKeyBytes)
	if err != nil {
		t.Errorf("ephemeral public key bytes are not a valid P-256 point: %v", err)
	}
}

// Multiple encryptions of same plaintext produce different ciphertexts (randomness)
func TestEncryptPropertyValue_NonDeterministic(t *testing.T) {
	_, pub := generateTestKeyPair(t)
	pubKeyHex := hex.EncodeToString(uncompressedPublicKeyBytes(pub))

	c1, err := encryptPropertyValue("same", pubKeyHex)
	if err != nil {
		t.Fatalf("encryption 1 failed: %v", err)
	}
	c2, err := encryptPropertyValue("same", pubKeyHex)
	if err != nil {
		t.Fatalf("encryption 2 failed: %v", err)
	}
	if c1 == c2 {
		t.Error("two encryptions of the same plaintext should differ (ephemeral key + random IV)")
	}
}

// Test long plaintext
func TestEncryptPropertyValue_LongValue(t *testing.T) {
	priv, pub := generateTestKeyPair(t)
	pubKeyHex := hex.EncodeToString(uncompressedPublicKeyBytes(pub))

	longStr := ""
	for i := 0; i < 1000; i++ {
		longStr += "a"
	}

	cipherB64, err := encryptPropertyValue(longStr, pubKeyHex)
	if err != nil {
		t.Fatalf("encryption failed: %v", err)
	}

	decrypted, err := decryptForTest(cipherB64, priv)
	if err != nil {
		t.Fatalf("decryption failed: %v", err)
	}
	if decrypted != longStr {
		t.Error("round-trip of long string failed")
	}
}
