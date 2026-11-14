package plugins

// Package integrity and publisher signatures.
//
// A marketplace entry may pin the package's SHA-256 ("integrity":
// "sha256-<base64>") and carry a publisher signature: an ed25519 signature
// over the message
//
//	crewbase-plugin-v1\n<name>\n<version>\n<sha256 hex>
//
// by a key in the trusted publishers list (config "publishers"). Integrity
// mismatches and bad signatures are refused. A valid signature marks the
// plugin's publisher as verified; unsigned packages install as
// "unverified" unless "requireSigned" is set. Signing binds the exact
// bytes and the name / version, so a publisher's package cannot be passed
// off as another plugin.

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/hedeqiang/crewbase/bridge/process"
)

// Publisher is a trusted signing key.
type Publisher struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Key  string `json:"key,omitempty"` // base64 ed25519 public key
}

// Signed is a package signature in a marketplace entry.
type Signed struct {
	Publisher string `json:"publisher"` // publisher id
	Signature string `json:"signature"` // base64 ed25519 signature
}

// ErrUntrusted is returned for packages that fail verification.
var ErrUntrusted = errors.New("package verification failed")

// SignMessage is what a publisher signs.
func SignMessage(name, version string, digest []byte) []byte {
	return []byte(fmt.Sprintf("crewbase-plugin-v1\n%s\n%s\n%s", name, version, hex.EncodeToString(digest)))
}

// FileDigest is the SHA-256 of a file.
func FileDigest(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

// Integrity formats a digest as "sha256-<base64>".
func Integrity(digest []byte) string {
	return "sha256-" + base64.StdEncoding.EncodeToString(digest)
}

func checkIntegrity(want string, digest []byte) error {
	if want == "" {
		return nil
	}
	if digest == nil {
		return fmt.Errorf("%w: integrity is pinned but the source is not a package (tarball or npm)", ErrUntrusted)
	}
	b, ok := strings.CutPrefix(want, "sha256-")
	if !ok {
		return fmt.Errorf("%w: only sha256 integrity is supported", ErrUntrusted)
	}
	exp, err := base64.StdEncoding.DecodeString(b)
	if err != nil || len(exp) != sha256.Size {
		return fmt.Errorf("%w: malformed integrity %q", ErrUntrusted, want)
	}
	if string(exp) != string(digest) {
		return fmt.Errorf("%w: the package does not match its pinned integrity (got %s)", ErrUntrusted, Integrity(digest))
	}
	return nil
}

// verify checks a signature; it returns the verified publisher.
func verify(pubs []Publisher, s *Signed, name, version string, digest []byte) (*Publisher, error) {
	if s == nil || s.Signature == "" {
		return nil, nil
	}
	if digest == nil {
		return nil, fmt.Errorf("%w: a signature needs a package (tarball or npm)", ErrUntrusted)
	}
	var p *Publisher
	for i := range pubs {
		if pubs[i].ID == s.Publisher {
			p = &pubs[i]
		}
	}
	if p == nil {
		return nil, fmt.Errorf("%w: publisher %q is not trusted", ErrUntrusted, s.Publisher)
	}
	key, err := base64.StdEncoding.DecodeString(p.Key)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: publisher %q has a malformed key", ErrUntrusted, p.ID)
	}
	sig, err := base64.StdEncoding.DecodeString(s.Signature)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(key), SignMessage(name, version, digest), sig) {
		return nil, fmt.Errorf("%w: the signature from %s does not match this package", ErrUntrusted, p.Name)
	}
	return p, nil
}

// Keygen makes a publisher key pair (base64).
func Keygen() (pub, priv string, err error) {
	pk, sk, err := ed25519.GenerateKey(nil)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(pk), base64.StdEncoding.EncodeToString(sk), nil
}

// Sign signs a package file for name / version with a base64 private key.
func Sign(privB64, tarball, name, version string) (signature, integrity string, err error) {
	sk, err := base64.StdEncoding.DecodeString(strings.TrimSpace(privB64))
	if err != nil || len(sk) != ed25519.PrivateKeySize {
		return "", "", errors.New("malformed private key")
	}
	d, err := FileDigest(tarball)
	if err != nil {
		return "", "", err
	}
	sig := ed25519.Sign(ed25519.PrivateKey(sk), SignMessage(name, version, d))
	return base64.StdEncoding.EncodeToString(sig), Integrity(d), nil
}

// ReadPackageManifest unpacks a package into dir and reads its manifest
// (for signing tools).
func ReadPackageManifest(tarball, dir string) (process.Manifest, error) {
	root, err := unpack(tarball, dir)
	if err != nil {
		return process.Manifest{}, err
	}
	return process.ReadManifest(root)
}
