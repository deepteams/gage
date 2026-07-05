package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/deepteams/gage"
)

// MemoryStore is an in-memory TokenStore, handy for tests and short-lived
// processes. The zero value is not usable; use NewMemoryStore.
type MemoryStore struct {
	mu    sync.RWMutex
	creds gage.Credentials
	set   bool
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore { return &MemoryStore{} }

// NewMemoryStoreWith seeds the store with initial credentials.
func NewMemoryStoreWith(c gage.Credentials) *MemoryStore {
	return &MemoryStore{creds: c, set: true}
}

func (m *MemoryStore) Load(ctx context.Context) (gage.Credentials, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.set {
		return gage.Credentials{}, fmt.Errorf("oauth: no credentials: %w", gage.ErrAuth)
	}
	return m.creds, nil
}

func (m *MemoryStore) Save(ctx context.Context, c gage.Credentials) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.creds = c
	m.set = true
	return nil
}

// FileStore persists Credentials as JSON at a caller-chosen path. It is offered
// as a convenience; consumers may implement gage.TokenStore differently (DB,
// keychain, ...). The file is written with 0600 permissions.
type FileStore struct {
	Path  string
	codec tokenCodec
	mu    sync.Mutex
}

// NewFileStore returns a FileStore writing to path.
func NewFileStore(path string) *FileStore { return &FileStore{Path: path, codec: plainTokenCodec{}} }

// NewEncryptedFileStore returns a FileStore that encrypts credentials with
// AES-GCM before writing them to disk. key must be 16, 24, or 32 bytes.
func NewEncryptedFileStore(path string, key []byte) (*FileStore, error) {
	codec, err := newTokenAESGCMCodec(key)
	if err != nil {
		return nil, err
	}
	return &FileStore{Path: path, codec: codec}, nil
}

func (f *FileStore) Load(ctx context.Context) (gage.Credentials, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := os.ReadFile(f.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return gage.Credentials{}, fmt.Errorf("oauth: credentials file %s not found: %w", f.Path, gage.ErrAuth)
		}
		return gage.Credentials{}, fmt.Errorf("oauth: read credentials: %w", err)
	}
	b, err = f.codec.Decode(b)
	if err != nil {
		return gage.Credentials{}, fmt.Errorf("oauth: decrypt credentials: %w", err)
	}
	var c gage.Credentials
	if err := json.Unmarshal(b, &c); err != nil {
		return gage.Credentials{}, fmt.Errorf("oauth: parse credentials: %w", err)
	}
	return c, nil
}

func (f *FileStore) Save(ctx context.Context, c gage.Credentials) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if dir := filepath.Dir(f.Path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("oauth: create dir: %w", err)
		}
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	b, err = f.codec.Encode(b)
	if err != nil {
		return fmt.Errorf("oauth: encrypt credentials: %w", err)
	}
	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("oauth: write credentials: %w", err)
	}
	if err := os.Rename(tmp, f.Path); err != nil {
		return fmt.Errorf("oauth: replace credentials: %w", err)
	}
	return nil
}
