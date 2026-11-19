package credentials

// The vault: Crewbase's own store for every secret it keeps (API keys,
// MCP and IM tokens, OAuth logins). It replaces the system keychain as
// the place secrets are written to.
//
// Layout, under <root>/vault (0700):
//
//	master.key   32 random bytes (0600), made on first use
//	vault.json   {"version":1,"items":{"<id>":{"nonce","data","created","updated"}}} (0600)
//
// Each value is sealed with AES-256-GCM under the master key, with the
// item id as additional data: a sealed value moved to another id does not
// open. The key file is protected by file permissions only, like ~/.ssh
// keys; anyone who can read both files as this user can read the secrets.
// vault.json is written to a temp file and renamed, so a crash never
// leaves it half-written, and reloaded when another process changed it.
//
// Refs: "vault:<id>" and, for configs written before the vault,
// "keychain:<id>" both name vault item <id> (e.g. crewbase/openai). The
// system keychain is not read: a secret kept there before has to be
// entered again.

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

// VaultDir is the vault folder under a data root.
const VaultDir = "vault"

const (
	vaultFile = "vault.json"
	keyFile   = "master.key"
)

// ErrVaultLocked means the vault file exists but its master key is gone:
// its items cannot be read, and the vault refuses to start over on its own.
var ErrVaultLocked = errors.New("credentials: the vault's master key is missing")

type sealed struct {
	Nonce   string    `json:"nonce"`
	Data    string    `json:"data"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

type vaultData struct {
	Version int               `json:"version"`
	Items   map[string]sealed `json:"items"`
}

// Vault is the file-backed encrypted store.
type Vault struct {
	dir string

	mu    sync.Mutex
	aead  cipher.AEAD
	data  vaultData
	mtime time.Time // of vault.json when last read or written
}

// VaultItem is an item's metadata (never its value).
type VaultItem struct {
	ID      string    `json:"id"`
	Masked  string    `json:"masked"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

// OpenVault opens (creating on first use) the vault in dir.
func OpenVault(dir string) (*Vault, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(dir, 0o700)
	key, err := os.ReadFile(filepath.Join(dir, keyFile))
	switch {
	case errors.Is(err, os.ErrNotExist):
		if _, serr := os.Stat(filepath.Join(dir, vaultFile)); serr == nil {
			return nil, fmt.Errorf("%w (%s); restore it, or move %s away to start an empty vault", ErrVaultLocked, filepath.Join(dir, keyFile), vaultFile)
		}
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := writeFileAtomic(filepath.Join(dir, keyFile), key); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	case len(key) != 32:
		return nil, fmt.Errorf("credentials: %s is not a vault key (%d bytes)", filepath.Join(dir, keyFile), len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	v := &Vault{dir: dir, aead: aead, data: vaultData{Version: 1, Items: map[string]sealed{}}}
	if err := v.reload(true); err != nil {
		return nil, err
	}
	return v, nil
}

// reload reads vault.json when it changed since it was last read.
// Callers hold v.mu (or own v exclusively).
func (v *Vault) reload(force bool) error {
	p := filepath.Join(v.dir, vaultFile)
	st, err := os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !force && st.ModTime().Equal(v.mtime) {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	var d vaultData
	if err := json.Unmarshal(b, &d); err != nil {
		return fmt.Errorf("credentials: %s is damaged: %v", p, err)
	}
	if d.Items == nil {
		d.Items = map[string]sealed{}
	}
	v.data, v.mtime = d, st.ModTime()
	return nil
}

func (v *Vault) save() error {
	b, err := json.MarshalIndent(v.data, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(v.dir, vaultFile)
	if err := writeFileAtomic(p, b); err != nil {
		return err
	}
	if st, err := os.Stat(p); err == nil {
		v.mtime = st.ModTime()
	}
	return nil
}

func (v *Vault) open(id string, s sealed) (string, error) {
	nonce, err1 := base64.StdEncoding.DecodeString(s.Nonce)
	data, err2 := base64.StdEncoding.DecodeString(s.Data)
	if err1 != nil || err2 != nil || len(nonce) != v.aead.NonceSize() {
		return "", fmt.Errorf("credentials: vault item %s is damaged", id)
	}
	pt, err := v.aead.Open(nil, nonce, data, []byte(id))
	if err != nil {
		return "", fmt.Errorf("credentials: vault item %s does not open with this vault's key", id)
	}
	return string(pt), nil
}

// Name implements Backend.
func (*Vault) Name() string { return "vault" }

// Get implements Backend.
func (v *Vault) Get(_ context.Context, id string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.reload(false); err != nil {
		return "", err
	}
	s, ok := v.data.Items[id]
	if !ok {
		return "", ErrNotFound
	}
	return v.open(id, s)
}

// Set implements Backend.
func (v *Vault) Set(_ context.Context, id, value string) error {
	if !validID(id) {
		return fmt.Errorf("%w: a vault id is <service>/<name> using letters, digits and . _ @ -", ErrUnsupported)
	}
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	ct := v.aead.Seal(nil, nonce, []byte(value), []byte(id))
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.reload(false); err != nil {
		return err
	}
	now := time.Now().UTC()
	s := sealed{Nonce: base64.StdEncoding.EncodeToString(nonce), Data: base64.StdEncoding.EncodeToString(ct), Created: now, Updated: now}
	if old, ok := v.data.Items[id]; ok {
		s.Created = old.Created
	}
	v.data.Items[id] = s
	return v.save()
}

// Delete implements Backend.
func (v *Vault) Delete(_ context.Context, id string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.reload(false); err != nil {
		return err
	}
	if _, ok := v.data.Items[id]; !ok {
		return nil
	}
	delete(v.data.Items, id)
	return v.save()
}

// List returns every item's metadata, by id.
func (v *Vault) List() ([]VaultItem, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.reload(false); err != nil {
		return nil, err
	}
	out := make([]VaultItem, 0, len(v.data.Items))
	for id, s := range v.data.Items {
		it := VaultItem{ID: id, Created: s.Created, Updated: s.Updated}
		if pt, err := v.open(id, s); err == nil {
			it.Masked = Mask(pt)
		}
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// writeFileAtomic writes b to p (0600) through a temp file and a rename.
func writeFileAtomic(p string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

var vaultID = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,128}/[A-Za-z0-9._@-]{1,128}$`)

func validID(id string) bool { return vaultID.MatchString(id) }
