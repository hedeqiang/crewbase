// Package relay connects this Crewbase to a relay server so phones and
// other computers can reach it with zero inbound ports (docs/06 §8): the
// daemon dials out over WebSocket; devices connect to the same relay; the
// relay forwards opaque frames between them.
//
// Everything between a device and this daemon is end-to-end encrypted.
// The pairing link carries the host's static P-256 public key in the URL
// fragment (never sent to the relay), so the device pins it:
//
//	device → host   0x01 ‖ eD (65-byte uncompressed P-256)
//	host → device   0x02 ‖ eH
//	ikm  = ECDH(eD, S) ‖ ECDH(eD, eH)          (S = host static key)
//	keys = HKDF-SHA256(ikm, salt "crewbase-relay-v1", info eD‖eH‖S, 64)
//	       → device→host key (32) ‖ host→device key (32)
//	frames: AES-256-GCM, nonce = 0⁴ ‖ counter (big-endian uint64), per direction
//
// A relay in the middle cannot compute ECDH(eD, S), so it can neither read
// nor forge frames. Devices still authenticate with their device token
// inside the channel; the relay sees no tokens and no content. (A relay
// that also serves the web app is trusted to serve unmodified code — run
// your own, see docs/deploy.md.)
package relay

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Handshake message tags.
const (
	tagClientHello = 0x01
	tagHostHello   = 0x02
)

const salt = "crewbase-relay-v1"

// Identity is the host's static key: it signs the relay challenge and is
// the static half of every device handshake.
type Identity struct {
	priv *ecdsa.PrivateKey
	dh   *ecdh.PrivateKey
	pub  []byte // uncompressed P-256
}

// LoadOrCreateIdentity reads dir/relay-key.pem or creates it (0600).
func LoadOrCreateIdentity(dir string) (*Identity, error) {
	path := filepath.Join(dir, "relay-key.pem")
	if b, err := os.ReadFile(path); err == nil {
		blk, _ := pem.Decode(b)
		if blk == nil {
			return nil, errors.New("relay-key.pem: not PEM")
		}
		k, err := x509.ParseECPrivateKey(blk.Bytes)
		if err != nil {
			return nil, fmt.Errorf("relay-key.pem: %w", err)
		}
		return newIdentity(k)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, err
	}
	return newIdentity(k)
}

// NewIdentity makes a throwaway identity (tests).
func NewIdentity() (*Identity, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return newIdentity(k)
}

func newIdentity(k *ecdsa.PrivateKey) (*Identity, error) {
	dh, err := k.ECDH()
	if err != nil {
		return nil, err
	}
	return &Identity{priv: k, dh: dh, pub: dh.PublicKey().Bytes()}, nil
}

// Public returns the uncompressed public key.
func (id *Identity) Public() []byte { return append([]byte(nil), id.pub...) }

// PublicB64 is the public key as unpadded base64url (as in pairing links).
func (id *Identity) PublicB64() string { return base64.RawURLEncoding.EncodeToString(id.pub) }

// HostID names the host on the relay: derived from its public key, so the
// relay can check that whoever claims an id holds the key.
func (id *Identity) HostID() string { return HostIDOf(id.pub) }

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// HostIDOf derives a host id from an uncompressed P-256 public key.
func HostIDOf(pub []byte) string {
	h := sha256.Sum256(pub)
	return strings.ToLower(b32.EncodeToString(h[:16]))
}

// Sign signs the relay's challenge (ASN.1 ECDSA over SHA-256).
func (id *Identity) Sign(challenge []byte) ([]byte, error) {
	h := sha256.Sum256(append([]byte("crewbase-relay-host-auth:"), challenge...))
	return ecdsa.SignASN1(rand.Reader, id.priv, h[:])
}

// VerifyHost checks a host's answer to a relay challenge.
func VerifyHost(pub, challenge, sig []byte) bool {
	k, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), pub)
	if err != nil {
		return false
	}
	h := sha256.Sum256(append([]byte("crewbase-relay-host-auth:"), challenge...))
	return ecdsa.VerifyASN1(k, h[:], sig)
}

// Channel encrypts one device connection.
type Channel struct {
	send, recv cipher.AEAD
	sendN      uint64
	recvN      uint64
	sendTag    byte
	recvTag    byte
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

func deriveKeys(ikm, eD, eH, s []byte) ([]byte, error) {
	info := append(append(append([]byte(nil), eD...), eH...), s...)
	return hkdf.Key(sha256.New, ikm, []byte(salt), string(info), 64)
}

// Accept answers a device's hello: it returns the host hello to send back
// and the channel.
func (id *Identity) Accept(hello []byte) (reply []byte, ch *Channel, err error) {
	if len(hello) != 66 || hello[0] != tagClientHello {
		return nil, nil, errors.New("bad client hello")
	}
	eD, err := ecdh.P256().NewPublicKey(hello[1:])
	if err != nil {
		return nil, nil, fmt.Errorf("bad client key: %w", err)
	}
	eHk, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	s1, err := id.dh.ECDH(eD)
	if err != nil {
		return nil, nil, err
	}
	s2, err := eHk.ECDH(eD)
	if err != nil {
		return nil, nil, err
	}
	eH := eHk.PublicKey().Bytes()
	keys, err := deriveKeys(append(s1, s2...), hello[1:], eH, id.pub)
	if err != nil {
		return nil, nil, err
	}
	ch, err = newChannel(keys[32:], keys[:32], 'h', 'd') // host sends with the host→device key
	if err != nil {
		return nil, nil, err
	}
	return append([]byte{tagHostHello}, eH...), ch, nil
}

// Dial is the device side of the handshake (used by tests and the Go
// client; the web app implements the same in WebCrypto).
type Dial struct {
	e      *ecdh.PrivateKey
	static []byte
}

// NewDial starts a handshake with a host whose static key is pinned.
func NewDial(hostPub []byte) (*Dial, []byte, error) {
	if _, err := ecdh.P256().NewPublicKey(hostPub); err != nil {
		return nil, nil, fmt.Errorf("bad host key: %w", err)
	}
	e, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	return &Dial{e: e, static: hostPub}, append([]byte{tagClientHello}, e.PublicKey().Bytes()...), nil
}

// Finish completes the handshake with the host hello.
func (d *Dial) Finish(reply []byte) (*Channel, error) {
	if len(reply) != 66 || reply[0] != tagHostHello {
		return nil, errors.New("bad host hello")
	}
	eH, err := ecdh.P256().NewPublicKey(reply[1:])
	if err != nil {
		return nil, err
	}
	S, _ := ecdh.P256().NewPublicKey(d.static)
	s1, err := d.e.ECDH(S)
	if err != nil {
		return nil, err
	}
	s2, err := d.e.ECDH(eH)
	if err != nil {
		return nil, err
	}
	keys, err := deriveKeys(append(s1, s2...), d.e.PublicKey().Bytes(), reply[1:], d.static)
	if err != nil {
		return nil, err
	}
	return newChannel(keys[:32], keys[32:], 'd', 'h')
}

func newChannel(sendKey, recvKey []byte, sendTag, recvTag byte) (*Channel, error) {
	s, err := newAEAD(sendKey)
	if err != nil {
		return nil, err
	}
	r, err := newAEAD(recvKey)
	if err != nil {
		return nil, err
	}
	return &Channel{send: s, recv: r, sendTag: sendTag, recvTag: recvTag}, nil
}

func nonce(n uint64) []byte {
	b := make([]byte, 12)
	binary.BigEndian.PutUint64(b[4:], n)
	return b
}

// Seal encrypts the next outgoing frame. Not safe for concurrent use.
func (c *Channel) Seal(plain []byte) []byte {
	out := c.send.Seal(nil, nonce(c.sendN), plain, []byte{c.sendTag})
	c.sendN++
	return out
}

// Open decrypts the next incoming frame; frames must arrive in order.
func (c *Channel) Open(frame []byte) ([]byte, error) {
	p, err := c.recv.Open(nil, nonce(c.recvN), frame, []byte{c.recvTag})
	if err != nil {
		return nil, errors.New("frame failed authentication")
	}
	c.recvN++
	return p, nil
}
