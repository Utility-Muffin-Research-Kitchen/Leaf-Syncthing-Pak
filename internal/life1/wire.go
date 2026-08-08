// Package life1 implements the framed JSON transport shared by CTL-1 and
// LIFE-1. Protocol-specific message validation belongs above this package.
package life1

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	// SemanticMaxPayload is the CTL-1/LIFE-1 protocol ceiling.
	SemanticMaxPayload = 64 * 1024
	// TransportMaxPayload is Jawaka's existing generic IPC frame ceiling.
	TransportMaxPayload = 16 * 1024 * 1024
	framePrefixBytes    = 4
)

var (
	ErrFrameTooShort  = errors.New("life1: frame is shorter than its length prefix")
	ErrTransportLimit = errors.New("life1: frame exceeds the IPC transport ceiling")
	ErrSemanticLimit  = errors.New("life1: frame exceeds the CTL-1/LIFE-1 semantic ceiling")
	ErrLengthMismatch = errors.New("life1: frame length does not match its prefix")
	ErrInvalidJSON    = errors.New("life1: payload is not valid JSON")
)

// Encode frames an already-encoded JSON message. It deliberately preserves
// the JSON bytes: their UTF-8 byte length, not a character count, is written
// into the network-order prefix.
func Encode(message json.RawMessage) ([]byte, error) {
	if len(message) > SemanticMaxPayload {
		return nil, ErrSemanticLimit
	}
	if !json.Valid(message) {
		return nil, ErrInvalidJSON
	}

	frame := make([]byte, framePrefixBytes+len(message))
	binary.BigEndian.PutUint32(frame[:framePrefixBytes], uint32(len(message)))
	copy(frame[framePrefixBytes:], message)
	return frame, nil
}

// Decode validates one complete frame and returns a private copy of its JSON
// payload. The transport limit is checked before allocation or JSON parsing.
func Decode(frame []byte) (json.RawMessage, error) {
	if len(frame) < framePrefixBytes {
		return nil, ErrFrameTooShort
	}

	declared := uint64(binary.BigEndian.Uint32(frame[:framePrefixBytes]))
	if declared > TransportMaxPayload {
		return nil, ErrTransportLimit
	}
	if declared > SemanticMaxPayload {
		return nil, ErrSemanticLimit
	}
	if declared != uint64(len(frame)-framePrefixBytes) {
		return nil, ErrLengthMismatch
	}

	payload := frame[framePrefixBytes:]
	if !json.Valid(payload) {
		return nil, ErrInvalidJSON
	}
	return append(json.RawMessage(nil), payload...), nil
}

// Read reads one complete frame without allocating beyond the LIFE-1 semantic
// ceiling. The generic Jawaka transport permits larger frames, but those are a
// protocol error for this connection.
func Read(reader io.Reader) (json.RawMessage, error) {
	var prefix [framePrefixBytes]byte
	if _, err := io.ReadFull(reader, prefix[:]); err != nil {
		return nil, err
	}

	declared := binary.BigEndian.Uint32(prefix[:])
	if declared > TransportMaxPayload {
		return nil, ErrTransportLimit
	}
	if declared > SemanticMaxPayload {
		return nil, ErrSemanticLimit
	}

	payload := make([]byte, int(declared))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, fmt.Errorf("life1: read payload: %w", err)
	}
	if !json.Valid(payload) {
		return nil, ErrInvalidJSON
	}
	return json.RawMessage(payload), nil
}

// Write writes one complete frame, retrying short writes rather than assuming
// a Unix socket accepts it atomically.
func Write(writer io.Writer, message json.RawMessage) error {
	frame, err := Encode(message)
	if err != nil {
		return err
	}
	for len(frame) > 0 {
		written, err := writer.Write(frame)
		if err != nil {
			return err
		}
		if written <= 0 || written > len(frame) {
			return io.ErrShortWrite
		}
		frame = frame[written:]
	}
	return nil
}
