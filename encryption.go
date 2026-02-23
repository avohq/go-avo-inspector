package avoinspector

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"log"
)

// shouldEncrypt returns true when property encryption should be applied.
// Encryption is enabled for dev and staging environments when a non-empty key is provided.
// Production never encrypts (properties are sent as-is or omitted).
func shouldEncrypt(env string, publicEncryptionKey string) bool {
	if publicEncryptionKey == "" {
		return false
	}
	return env == "dev" || env == "staging"
}

// encryptPropertyValue encrypts a plaintext string using ECIES with AES-256-GCM (16-byte nonce).
//
// Wire format: [0x00][65-byte uncompressed ephemeral pubkey][16-byte IV][16-byte auth tag][ciphertext]
// Output: base64-encoded wire bytes.
func encryptPropertyValue(plaintext string, publicKeyBase64 string) (string, error) {
	// Decode the recipient's public key
	pubKeyBytes, err := base64.StdEncoding.DecodeString(publicKeyBase64)
	if err != nil {
		return "", fmt.Errorf("failed to decode public key: %w", err)
	}

	recipientPubKey, err := ecdh.P256().NewPublicKey(pubKeyBytes)
	if err != nil {
		return "", fmt.Errorf("failed to parse public key: %w", err)
	}

	// Generate ephemeral key pair
	ephemeralPrivKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("failed to generate ephemeral key: %w", err)
	}

	// ECDH: compute shared secret (raw X-coordinate)
	sharedSecret, err := ephemeralPrivKey.ECDH(recipientPubKey)
	if err != nil {
		return "", fmt.Errorf("ECDH failed: %w", err)
	}

	// KDF: SHA-256 of shared secret → AES-256 key
	aesKey := sha256.Sum256(sharedSecret)

	// AES-GCM with 16-byte nonce (required for cross-SDK interop)
	block, err := aes.NewCipher(aesKey[:])
	if err != nil {
		return "", fmt.Errorf("failed to create AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCMWithNonceSize(block, 16)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	// Generate random 16-byte IV/nonce
	nonce := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}

	// Encrypt: GCM Seal appends auth tag to ciphertext
	// Result is: ciphertext || auth_tag (16 bytes)
	sealed := gcm.Seal(nil, nonce, []byte(plaintext), nil)

	// sealed = ciphertext + 16-byte tag
	// Split: ciphertext is sealed[:len-16], tag is sealed[len-16:]
	tagSize := gcm.Overhead() // 16
	ciphertextBytes := sealed[:len(sealed)-tagSize]
	authTag := sealed[len(sealed)-tagSize:]

	// Build wire format: [0x00][65-byte ephemeral pubkey][16-byte IV][16-byte auth tag][ciphertext]
	ephPubKeyBytes := ephemeralPrivKey.PublicKey().Bytes() // 65 bytes uncompressed
	wire := make([]byte, 0, 1+len(ephPubKeyBytes)+16+16+len(ciphertextBytes))
	wire = append(wire, 0x00)           // version byte
	wire = append(wire, ephPubKeyBytes...) // 65-byte uncompressed ephemeral public key
	wire = append(wire, nonce...)        // 16-byte IV
	wire = append(wire, authTag...)      // 16-byte auth tag
	wire = append(wire, ciphertextBytes...) // ciphertext

	return base64.StdEncoding.EncodeToString(wire), nil
}

// EncryptedProperty represents a property with an encrypted value for the wire payload.
type EncryptedProperty struct {
	PropertyName          string `json:"propertyName"`
	PropertyType          string `json:"propertyType"`
	EncryptedPropertyValue string `json:"encryptedPropertyValue"`
}

// encryptEventProperties encrypts non-list property types and returns EncryptedProperty items.
// List-type properties are omitted entirely (AC5).
// On encryption failure, the property is omitted and a warning is logged (AC6).
func encryptEventProperties(properties []Property, publicKeyBase64 string) []EncryptedProperty {
	var result []EncryptedProperty

	for _, prop := range properties {
		// AC5: List-type property values omitted entirely
		if prop.PropertyType == "list" {
			continue
		}

		encrypted, err := encryptPropertyValue(prop.PropertyType, publicKeyBase64)
		if err != nil {
			// AC6: log warning, omit property, continue
			log.Printf("[Avo Inspector] Warning: failed to encrypt property '%s': %v", prop.PropertyName, err)
			continue
		}

		result = append(result, EncryptedProperty{
			PropertyName:          prop.PropertyName,
			PropertyType:          prop.PropertyType,
			EncryptedPropertyValue: encrypted,
		})
	}

	return result
}

// decryptForTest is a Go decryption function used only for testing round-trips.
// It decodes the wire format and decrypts using the recipient's private key.
func decryptForTest(ciphertextBase64 string, privateKey *ecdh.PrivateKey) (string, error) {
	wire, err := base64.StdEncoding.DecodeString(ciphertextBase64)
	if err != nil {
		return "", fmt.Errorf("base64 decode failed: %w", err)
	}

	// Minimum wire length: 1 (version) + 65 (pubkey) + 16 (IV) + 16 (tag) = 98
	if len(wire) < 98 {
		return "", fmt.Errorf("wire too short: %d bytes", len(wire))
	}

	// Parse wire format
	if wire[0] != 0x00 {
		return "", fmt.Errorf("unsupported version byte: 0x%02x", wire[0])
	}

	ephPubKeyBytes := wire[1:66]
	nonce := wire[66:82]
	authTag := wire[82:98]
	ciphertextBytes := wire[98:]

	// Reconstruct ephemeral public key
	ephPubKey, err := ecdh.P256().NewPublicKey(ephPubKeyBytes)
	if err != nil {
		return "", fmt.Errorf("failed to parse ephemeral public key: %w", err)
	}

	// ECDH with recipient's private key
	sharedSecret, err := privateKey.ECDH(ephPubKey)
	if err != nil {
		return "", fmt.Errorf("ECDH failed: %w", err)
	}

	// KDF
	aesKey := sha256.Sum256(sharedSecret)

	// AES-GCM with 16-byte nonce
	block, err := aes.NewCipher(aesKey[:])
	if err != nil {
		return "", fmt.Errorf("failed to create AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCMWithNonceSize(block, 16)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	// Reassemble sealed data: ciphertext || auth_tag (as GCM.Open expects)
	sealed := append(ciphertextBytes, authTag...)

	plaintext, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return "", fmt.Errorf("GCM decryption failed: %w", err)
	}

	return string(plaintext), nil
}

// decryptWith12ByteGCM attempts to decrypt using standard 12-byte GCM.
// This is expected to FAIL, proving that the 16-byte nonce is required for cross-SDK interop.
// Go's GCM panics on nonce size mismatch, so this function recovers from panics.
func decryptWith12ByteGCM(ciphertextBase64 string, privateKey *ecdh.PrivateKey) (retErr error) {
	defer func() {
		if r := recover(); r != nil {
			retErr = fmt.Errorf("panic during 12-byte GCM decryption: %v", r)
		}
	}()

	wire, err := base64.StdEncoding.DecodeString(ciphertextBase64)
	if err != nil {
		return fmt.Errorf("base64 decode failed: %w", err)
	}

	if len(wire) < 98 {
		return fmt.Errorf("wire too short: %d bytes", len(wire))
	}

	ephPubKeyBytes := wire[1:66]
	nonce := wire[66:82]   // 16 bytes — wrong size for standard GCM
	authTag := wire[82:98]
	ciphertextBytes := wire[98:]

	ephPubKey, err := ecdh.P256().NewPublicKey(ephPubKeyBytes)
	if err != nil {
		return fmt.Errorf("failed to parse ephemeral public key: %w", err)
	}

	sharedSecret, err := privateKey.ECDH(ephPubKey)
	if err != nil {
		return fmt.Errorf("ECDH failed: %w", err)
	}

	aesKey := sha256.Sum256(sharedSecret)

	block, err := aes.NewCipher(aesKey[:])
	if err != nil {
		return fmt.Errorf("failed to create AES cipher: %w", err)
	}

	// Standard GCM with 12-byte nonce — should fail because nonce is 16 bytes
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("failed to create GCM: %w", err)
	}

	sealed := append(ciphertextBytes, authTag...)

	// Use the 16-byte nonce with 12-byte GCM — will panic with nonce size mismatch
	_, err = gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return fmt.Errorf("GCM decryption failed: %w", err)
	}

	return nil
}
