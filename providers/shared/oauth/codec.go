package oauth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

type tokenCodec interface {
	Encode([]byte) ([]byte, error)
	Decode([]byte) ([]byte, error)
}

type plainTokenCodec struct{}

func (plainTokenCodec) Encode(b []byte) ([]byte, error) { return b, nil }
func (plainTokenCodec) Decode(b []byte) ([]byte, error) { return b, nil }

type tokenAESGCMCodec struct {
	aead cipher.AEAD
}

func newTokenAESGCMCodec(key []byte) (tokenCodec, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("oauth: encryption key: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("oauth: encryption: %w", err)
	}
	return tokenAESGCMCodec{aead: aead}, nil
}

type tokenEnvelope struct {
	Version int    `json:"v"`
	Alg     string `json:"alg"`
	Nonce   string `json:"nonce"`
	Data    string `json:"data"`
}

func (c tokenAESGCMCodec) Encode(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ciphertext := c.aead.Seal(nil, nonce, plaintext, nil)
	return json.Marshal(tokenEnvelope{
		Version: 1,
		Alg:     "AES-GCM",
		Nonce:   base64.StdEncoding.EncodeToString(nonce),
		Data:    base64.StdEncoding.EncodeToString(ciphertext),
	})
}

func (c tokenAESGCMCodec) Decode(data []byte) ([]byte, error) {
	var env tokenEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	if env.Version != 1 || env.Alg != "AES-GCM" {
		return nil, fmt.Errorf("unsupported encrypted credentials format")
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
