package deepseek

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"strings"

	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// VoiceAPIPlugin serves cloud speech for the web UI:
//
//	GET  /api/voice             {"cloud": bool, "voices": [...]}
//	POST /api/voice/transcribe  raw WAV / Ogg Opus body (≤ 8 MiB) → {"text", "durationMs"}
//	POST /api/voice/speak       {"text", "voice"} → audio/wav
//
// Audio is passed through to DeepSeek and not stored.
var VoiceAPIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/voice-api",
	Inject:      []string{gateway.ServiceKey, ServiceKey},
	Description: "Cloud speech for the web UI: dictation (DeepSeek ASR) and read-aloud (DeepSeek TTS).",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		s := kernel.Use[*Service](c, ServiceKey)
		devices := []string{"mobile"}
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/voice", Devices: devices,
			Handle: func(ctx context.Context, _ *gateway.Request) (any, error) {
				return map[string]any{"cloud": s.Available(ctx), "provider": "deepseek", "voices": Voices}, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/voice/transcribe", Devices: devices, RawBody: true, MaxBody: MaxASRBytes,
			Validate: func(r *gateway.Request) error {
				if err := checkAudio(r.Body); err != nil {
					return gateway.BadRequest("%v", err)
				}
				return nil
			},
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				ctx, cancel := context.WithTimeout(ctx, asrTimeout)
				defer cancel()
				name := "speech.wav"
				if bytes.HasPrefix(r.Body, []byte("OggS")) {
					name = "speech.ogg"
				}
				t, err := s.Client.Transcribe(ctx, name, r.Body)
				if err != nil {
					return nil, voiceErr(err)
				}
				return map[string]any{"text": strings.TrimSpace(t.Text), "durationMs": t.DurationMS}, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/voice/speak", Devices: devices, MaxBody: 256 << 10,
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				var in struct {
					Text  string `json:"text"`
					Voice string `json:"voice"`
				}
				if err := r.Decode(&in); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				if strings.TrimSpace(in.Text) == "" {
					return nil, gateway.BadRequest("text is required")
				}
				ctx, cancel := context.WithTimeout(ctx, ttsTimeout)
				defer cancel()
				audio, err := s.Client.Speak(ctx, in.Text, in.Voice)
				if err != nil {
					return nil, voiceErr(err)
				}
				return &gateway.Blob{Name: "speech.wav", Mime: "audio/wav", Data: audio, NoStore: true}, nil
			}})
		return nil
	},
})

func voiceErr(err error) error {
	if errors.Is(err, ErrNoKey) {
		return gateway.Conflict("%v", err)
	}
	var ae *APIError
	if errors.As(err, &ae) {
		switch ae.Status {
		case 400:
			return gateway.BadRequest("%v", err)
		case 429:
			return &gateway.Error{Status: 429, Code: "rate_limited", Message: err.Error()}
		}
	}
	return &gateway.Error{Status: 502, Code: "upstream", Message: err.Error()}
}

// checkAudio accepts 16-bit PCM WAV and Ogg (Opus), as the ASR API does.
func checkAudio(b []byte) error {
	switch {
	case len(b) == 0:
		return errors.New("the recording is empty")
	case bytes.HasPrefix(b, []byte("OggS")):
		return nil
	case len(b) >= 44 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WAVE":
		// find the fmt chunk
		for off := 12; off+8 <= len(b); {
			id, size := string(b[off:off+4]), int(binary.LittleEndian.Uint32(b[off+4:off+8]))
			if id == "fmt " && off+8+16 <= len(b) {
				format := binary.LittleEndian.Uint16(b[off+8:])
				bits := binary.LittleEndian.Uint16(b[off+22:])
				if format != 1 || bits != 16 {
					return errors.New("WAV must be 16-bit PCM")
				}
				return nil
			}
			off += 8 + size + size%2
		}
		return errors.New("WAV has no fmt chunk")
	}
	return errors.New("audio must be 16-bit PCM WAV or Ogg Opus")
}
