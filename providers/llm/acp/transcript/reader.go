package transcript

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"sync"
	"time"
)

// Defaults of a Reader.
const (
	// DefaultMaxBytes is how much of a file is parsed: a bigger file is
	// read from this far before its end (older lines are not seen).
	DefaultMaxBytes = 1 << 30
	// DefaultMaxLine skips longer lines (a pasted image, a huge result).
	DefaultMaxLine = 64 << 20
	// cacheFiles bounds the per-file parse state kept.
	cacheFiles = 16
)

// Reader parses session files incrementally: each file's parse state is
// cached and only lines appended since the last read are parsed (a file
// that shrank or was replaced is parsed again). Safe for concurrent use.
type Reader struct {
	MaxBytes int64
	MaxLine  int

	mu    sync.Mutex
	files map[string]*fileState
}

// NewReader returns a Reader with the default limits.
func NewReader() *Reader { return &Reader{} }

// fileState is one file's parse progress and what was learned from it.
type fileState struct {
	key     string
	size    int64
	mod     time.Time
	off     int64 // parsed up to here (just after a newline)
	start   int64 // where parsing began (> 0: a window of a big file)
	lastUse time.Time
	mu      sync.Mutex
	claude  *claudeState
	codex   *codexState
}

// span locates one line in a file.
type span struct {
	off int64
	n   int
}

func (r *Reader) maxBytes() int64 {
	if r.MaxBytes > 0 {
		return r.MaxBytes
	}
	return DefaultMaxBytes
}

func (r *Reader) maxLine() int {
	if r.MaxLine > 0 {
		return r.MaxLine
	}
	return DefaultMaxLine
}

// state returns the cached state of key (path + session), creating it.
func (r *Reader) state(key string) *fileState {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.files == nil {
		r.files = map[string]*fileState{}
	}
	fs := r.files[key]
	if fs == nil {
		fs = &fileState{key: key}
		r.files[key] = fs
		for len(r.files) > cacheFiles {
			var old *fileState
			for _, f := range r.files {
				if f != fs && (old == nil || f.lastUse.Before(old.lastUse)) {
					old = f
				}
			}
			delete(r.files, old.key)
		}
	}
	fs.lastUse = time.Now()
	return fs
}

// advance parses the lines appended to f since the last call. reset is
// called before a fresh parse (first read, or the file shrank / was
// replaced); line gets every complete line (without its newline) and
// where it starts.
func (r *Reader) advance(fs *fileState, f *os.File, reset func(), line func(b []byte, at span)) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	size, mod := fi.Size(), fi.ModTime()
	if size < fs.off || (fs.off > 0 && mod.Before(fs.mod)) || fs.size == 0 && fs.off == 0 {
		fs.off, fs.start = 0, 0
		reset()
		if size > r.maxBytes() {
			fs.start = size - r.maxBytes()
			fs.off = fs.start
		}
	}
	fs.size, fs.mod = size, mod
	if fs.off >= size {
		return nil
	}
	if _, err := f.Seek(fs.off, io.SeekStart); err != nil {
		return err
	}
	br := bufio.NewReaderSize(io.LimitReader(f, size-fs.off), 1<<20)
	pos := fs.off
	skipFirst := fs.start > 0 && fs.off == fs.start // mid-line: drop the partial one
	maxLine := int64(r.maxLine())
	var buf []byte
	var lineLen int64 // bytes of the current line so far
	for {
		chunk, err := br.ReadSlice('\n')
		lineLen += int64(len(chunk))
		if lineLen <= maxLine+1 {
			buf = append(buf, chunk...)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			break // no newline: an incomplete last line, parsed next time
		}
		switch {
		case skipFirst:
			skipFirst = false
		case lineLen <= maxLine+1:
			if b := bytes.TrimRight(buf, "\r\n"); len(b) > 0 {
				line(b, span{off: pos, n: len(b)})
			}
		}
		pos += lineLen
		buf, lineLen = buf[:0], 0
	}
	fs.off = pos
	return nil
}

// readSpan reads one line back.
func readSpan(f *os.File, s span) ([]byte, error) {
	b := make([]byte, s.n)
	if _, err := f.ReadAt(b, s.off); err != nil {
		return nil, err
	}
	return b, nil
}
