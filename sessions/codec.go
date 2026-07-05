package sessions

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

type fileCodec interface {
	Encode([]byte) ([]byte, error)
	Decode([]byte) ([]byte, error)
}

type plainCodec struct{}

func (plainCodec) Encode(b []byte) ([]byte, error) { return b, nil }
func (plainCodec) Decode(b []byte) ([]byte, error) { return b, nil }

type aesGCMCodec struct {
	aead cipher.AEAD
}

func newAESGCMCodec(key []byte) (fileCodec, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("sessions: encryption key: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("sessions: encryption: %w", err)
	}
	return aesGCMCodec{aead: aead}, nil
}

type encryptedEnvelope struct {
	Version int    `json:"v"`
	Alg     string `json:"alg"`
	Nonce   string `json:"nonce"`
	Data    string `json:"data"`
}

func (c aesGCMCodec) Encode(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ciphertext := c.aead.Seal(nil, nonce, plaintext, nil)
	return json.Marshal(encryptedEnvelope{
		Version: 1,
		Alg:     "AES-GCM",
		Nonce:   base64.StdEncoding.EncodeToString(nonce),
		Data:    base64.StdEncoding.EncodeToString(ciphertext),
	})
}

func (c aesGCMCodec) Decode(data []byte) ([]byte, error) {
	var env encryptedEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	if env.Version != 1 || env.Alg != "AES-GCM" {
		return nil, fmt.Errorf("unsupported encrypted session format")
	}
	nonce, err := base64.StdEncoding.DecodeString(env.Nonce)
	if err != nil {
		return nil, fmt.Errorf("decode nonce: %w", err)
	}
	ciphertext, err := base64.StdEncoding.DecodeString(env.Data)
	if err != nil {
		return nil, fmt.Errorf("decode data: %w", err)
	}
	return c.aead.Open(nil, nonce, ciphertext, nil)
}
