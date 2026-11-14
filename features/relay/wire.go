package relay

import (
	"encoding/binary"
	"errors"
)

// Frames between the relay and a host (binary WebSocket messages):
//
//	type (1 byte) ‖ stream id (4 bytes, big-endian) ‖ payload
//
// The relay assigns a stream id per device connection. Payloads are the
// device's bytes as-is (handshake, then sealed frames): the relay never
// looks inside them.
const (
	FrameOpen  byte = 1 // a device connected (no payload)
	FrameData  byte = 2
	FrameClose byte = 3 // either side ends the stream
)

// MaxFrame caps one message (an attachment upload is up to 30 MB, base64
// in JSON inside the sealed frame).
const MaxFrame = 48 << 20

// EncodeFrame builds a relay frame.
func EncodeFrame(typ byte, sid uint32, payload []byte) []byte {
	b := make([]byte, 5+len(payload))
	b[0] = typ
	binary.BigEndian.PutUint32(b[1:5], sid)
	copy(b[5:], payload)
	return b
}

// DecodeFrame splits a relay frame.
func DecodeFrame(b []byte) (typ byte, sid uint32, payload []byte, err error) {
	if len(b) < 5 {
		return 0, 0, nil, errors.New("short frame")
	}
	return b[0], binary.BigEndian.Uint32(b[1:5]), b[5:], nil
}

// Host authentication on the relay (text messages, JSON):
//
//	relay → host  {"challenge": <base64 32 bytes>}
//	host → relay  {"sig": <base64 ASN.1 ECDSA>}
//	relay → host  {"ok": true, "id": <host id>}  or {"error": "..."}
type authMsg struct {
	Challenge []byte `json:"challenge,omitempty"`
	Sig       []byte `json:"sig,omitempty"`
	OK        bool   `json:"ok,omitempty"`
	ID        string `json:"id,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Tunnel messages inside the sealed channel (JSON).
//
// device → host: a request, or a cancel for one.
type tReq struct {
	ID     int64             `json:"id"`
	M      string            `json:"m,omitempty"` // method
	P      string            `json:"p,omitempty"` // path with query, /api/... only
	H      map[string]string `json:"h,omitempty"`
	B      []byte            `json:"b,omitempty"`
	Cancel bool              `json:"cancel,omitempty"`
}

// host → device: the head (status + headers), then body chunks, then end.
// Streaming responses (/api/events) send chunks as they are flushed.
type tResp struct {
	ID  int64             `json:"id"`
	S   int               `json:"s,omitempty"`
	H   map[string]string `json:"h,omitempty"`
	B   []byte            `json:"b,omitempty"`
	End bool              `json:"end,omitempty"`
}
