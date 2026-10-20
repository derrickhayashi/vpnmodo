package protocol

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/derrickhayashi/vpnmodo/internal/jsonstrict"
)

var ErrFrame = errors.New("invalid or oversized RPC frame")

// FrameReader bounds allocation, requires LF termination, and never returns
// partial frames. Discard the connection after any framing error.
type FrameReader struct{ reader *bufio.Reader }

func NewFrameReader(r io.Reader) *FrameReader {
	return &FrameReader{reader: bufio.NewReaderSize(r, 4096)}
}
func (r *FrameReader) Read() ([]byte, error) {
	var message []byte
	for {
		part, err := r.reader.ReadSlice('\n')
		if len(message)+len(part) > MaxMessageBytes+1 {
			return nil, ErrFrame
		}
		message = append(message, part...)
		if err == bufio.ErrBufferFull {
			continue
		}
		if err == io.EOF && len(message) == 0 {
			return nil, io.EOF
		}
		if err != nil {
			return nil, ErrFrame
		}
		message = bytes.TrimSuffix(message, []byte{'\n'})
		if jsonstrict.Check(message, MaxMessageBytes) != nil || len(bytes.TrimSpace(message)) == 0 || bytes.TrimSpace(message)[0] != '{' {
			return nil, ErrFrame
		}
		return message, nil
	}
}

// FrameWriter serializes concurrent progress events and responses. Payloads are
// sensitive transport bytes; fmt of Request is redacted, not this byte slice.
type FrameWriter struct {
	writer io.Writer
	mu     sync.Mutex
}

func NewFrameWriter(w io.Writer) *FrameWriter { return &FrameWriter{writer: w} }
func (w *FrameWriter) Write(message []byte) error {
	if bytes.ContainsAny(message, "\r\n") || jsonstrict.Check(message, MaxMessageBytes) != nil || len(bytes.TrimSpace(message)) == 0 || bytes.TrimSpace(message)[0] != '{' {
		return ErrFrame
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	frame := append(append([]byte(nil), message...), '\n')
	n, err := w.writer.Write(frame)
	if err == nil && n != len(frame) {
		return io.ErrShortWrite
	}
	return err
}

func (r Request) Format(state fmt.State, verb rune) {
	_, _ = io.WriteString(state, "[RPC request redacted]")
}
