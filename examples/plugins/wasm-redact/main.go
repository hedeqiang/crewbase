//go:build wasip1

// wasm-redact is an example WebAssembly plugin: it masks internal
// ticket ids ("INC-12345") in streamed text and tool results, and asks for
// approval before any tool touches a path under "prod/".
//
// Build: GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
package main

import (
	"encoding/json"
	"regexp"
	"strings"
	"unsafe"
)

func main() {}

var ticket = regexp.MustCompile(`\bINC-\d{3,}\b`)

// keep is what the host may still read: the last input and output buffers.
var keep [][]byte

//go:wasmexport alloc
func alloc(size uint32) uint32 {
	b := make([]byte, size)
	keep = append(keep[:0], b)
	return uint32(uintptr(unsafe.Pointer(unsafe.SliceData(b))))
}

func input(ptr, n uint32) []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), n)
}

func output(v any) uint64 {
	b, _ := json.Marshal(v)
	keep = append(keep, b)
	return uint64(uintptr(unsafe.Pointer(unsafe.SliceData(b))))<<32 | uint64(len(b))
}

//go:wasmexport hook_stream_chunk
func hookStreamChunk(ptr, n uint32) uint64 {
	var in struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(input(ptr, n), &in) != nil || !ticket.MatchString(in.Text) {
		return 0 // unchanged
	}
	return output(map[string]string{"text": ticket.ReplaceAllString(in.Text, "INC-•••")})
}

//go:wasmexport hook_tool_result
func hookToolResult(ptr, n uint32) uint64 {
	var in struct {
		Content string `json:"content"`
	}
	if json.Unmarshal(input(ptr, n), &in) != nil || !ticket.MatchString(in.Content) {
		return 0
	}
	return output(map[string]string{"content": ticket.ReplaceAllString(in.Content, "INC-•••")})
}

//go:wasmexport hook_rule
func hookRule(ptr, n uint32) uint64 {
	var in struct {
		Tool    string          `json:"tool"`
		Summary string          `json:"summary"`
		Args    json.RawMessage `json:"args"`
	}
	if json.Unmarshal(input(ptr, n), &in) != nil {
		return 0
	}
	if strings.Contains(string(in.Args), "prod/") || strings.Contains(in.Summary, "prod/") {
		return output(map[string]string{"verdict": "ask", "reason": "touches prod/"})
	}
	return 0
}

// spin never returns (to test the host's time budget).
//
//go:wasmexport hook_spin
func hookSpin(ptr, n uint32) uint64 {
	for {
	}
}
