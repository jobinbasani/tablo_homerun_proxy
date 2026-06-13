package tablo

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

func encryptCredentials(plain []byte) ([]byte, error) {
	key := credentialKey()
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	iv := make([]byte, aes.BlockSize)
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return nil, err
	}
	padded := pkcs7Pad(plain, aes.BlockSize)
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, padded)
	return append(iv, ciphertext...), nil
}

func decryptCredentials(encrypted []byte) ([]byte, error) {
	if len(encrypted) < aes.BlockSize || len(encrypted)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("invalid encrypted credentials size")
	}
	key := credentialKey()
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	iv := encrypted[:aes.BlockSize]
	ciphertext := encrypted[aes.BlockSize:]
	plain := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, ciphertext)
	return pkcs7Unpad(plain, aes.BlockSize)
}

func makeDeviceAuth(method, path string, body []byte, date string) string {
	bodyHash := ""
	if len(body) > 0 {
		sum := md5.Sum(body)
		bodyHash = hex.EncodeToString(sum[:])
	}
	signatureText := strings.Join([]string{method, path, bodyHash, date}, "\n")
	hashKey := envOrDefault("HashKey", "6l8jU5N43cEilqItmT3U2M2PFM3qPziilXqau9ys")
	mac := hmac.New(md5.New, []byte(hashKey))
	_, _ = mac.Write([]byte(signatureText))
	deviceKey := envOrDefault("DeviceKey", "ljpg6ZkwShVv8aI12E2LP55Ep8vq1uYDPvX0DdTB")
	return "tablo:" + deviceKey + ":" + hex.EncodeToString(mac.Sum(nil))
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func newUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func credentialKey() [32]byte {
	secret := envOrDefault("CREDENTIAL_SECRET", os.Getenv("RSA"))
	if secret == "" {
		secret = "tablo-homerun-proxy-default-local-credential-key"
	}
	return sha256.Sum256([]byte(secret))
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	padding := blockSize - len(data)%blockSize
	return append(data, bytes.Repeat([]byte{byte(padding)}, padding)...)
}

func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, fmt.Errorf("invalid padded data")
	}
	padding := int(data[len(data)-1])
	if padding == 0 || padding > blockSize || padding > len(data) {
		return nil, fmt.Errorf("invalid padding")
	}
	for _, value := range data[len(data)-padding:] {
		if int(value) != padding {
			return nil, fmt.Errorf("invalid padding")
		}
	}
	return data[:len(data)-padding], nil
}
