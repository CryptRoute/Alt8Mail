package control

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"testing"
)

func maskedFrame(opcode byte, payload []byte) []byte {
	mask := []byte{0x12, 0x34, 0x56, 0x78}
	frame := []byte{0x80 | opcode, 0x80 | byte(len(payload))}
	frame = append(frame, mask...)
	for i, value := range payload {
		frame = append(frame, value^mask[i%4])
	}
	return frame
}

func TestReadWebSocketFrame(t *testing.T) {
	payload := []byte(`{"type":"input","data":"echo ok"}`)
	conn := &wsConn{reader: bufio.NewReader(bytes.NewReader(maskedFrame(0x1, payload)))}
	opcode, got, err := conn.readFrame()
	if err != nil {
		t.Fatal(err)
	}
	if opcode != 0x1 || !bytes.Equal(got, payload) {
		t.Fatalf("unexpected frame: opcode=%d payload=%q", opcode, got)
	}
}

func TestReadWebSocketFrameRejectsUnmasked(t *testing.T) {
	frame := []byte{0x81, byte(len("hello"))}
	frame = append(frame, []byte("hello")...)
	conn := &wsConn{reader: bufio.NewReader(bytes.NewReader(frame))}
	if _, _, err := conn.readFrame(); err == nil {
		t.Fatal("expected unmasked client frame to be rejected")
	}
}

func TestReadWebSocketFrameRejectsOversize(t *testing.T) {
	frame := make([]byte, 10)
	frame[0] = 0x81
	frame[1] = 0x80 | 127
	binary.BigEndian.PutUint64(frame[2:], uint64(8<<20))
	conn := &wsConn{reader: bufio.NewReader(bytes.NewReader(frame))}
	if _, _, err := conn.readFrame(); err == nil {
		t.Fatal("expected oversized frame to be rejected")
	}
}
