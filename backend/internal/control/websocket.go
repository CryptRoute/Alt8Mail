package control

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

type wsConn struct {
	conn   net.Conn
	reader *bufio.Reader
	mu     sync.Mutex
	closed bool
}

func upgradeWebSocket(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") ||
		!strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
		return nil, fmt.Errorf("websocket upgrade headers are missing")
	}
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if key == "" || strings.TrimSpace(r.Header.Get("Sec-WebSocket-Version")) != "13" {
		return nil, fmt.Errorf("unsupported websocket handshake")
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return nil, fmt.Errorf("this listener does not support websocket hijacking")
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return nil, err
	}
	sum := sha1.Sum([]byte(key + websocketGUID))
	accept := base64.StdEncoding.EncodeToString(sum[:])
	response := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := rw.WriteString(response); err != nil {
		conn.Close()
		return nil, err
	}
	if err := rw.Flush(); err != nil {
		conn.Close()
		return nil, err
	}
	// The HTTP server's read timeout applies to the upgrade request. Clear the
	// inherited deadlines so an idle interactive shell is not closed after the
	// two-minute server ReadTimeout.
	_ = conn.SetDeadline(time.Time{})
	return &wsConn{conn: conn, reader: rw.Reader}, nil
}

func (c *wsConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	_ = c.writeFrameLocked(0x8, []byte{0x03, 0xe8}) // 1000 normal closure
	return c.conn.Close()
}

func (c *wsConn) writeFrame(opcode byte, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return io.ErrClosedPipe
	}
	return c.writeFrameLocked(opcode, payload)
}

func (c *wsConn) writeFrameLocked(opcode byte, payload []byte) error {
	header := make([]byte, 0, 10)
	header = append(header, 0x80|opcode)
	length := len(payload)
	switch {
	case length <= 125:
		header = append(header, byte(length))
	case length <= 65535:
		header = append(header, 126, byte(length>>8), byte(length))
	default:
		header = append(header, 127)
		value := make([]byte, 8)
		binary.BigEndian.PutUint64(value, uint64(length))
		header = append(header, value...)
	}
	if err := writeAll(c.conn, header); err != nil {
		return err
	}
	return writeAll(c.conn, payload)
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func (c *wsConn) WriteText(data []byte) error   { return c.writeFrame(0x1, data) }
func (c *wsConn) WriteBinary(data []byte) error { return c.writeFrame(0x2, data) }

func (c *wsConn) WriteClose(code uint16, reason string) error {
	payload := []byte{byte(code >> 8), byte(code)}
	payload = append(payload, []byte(reason)...)
	if len(payload) > 123 {
		payload = payload[:123]
	}
	return c.writeFrame(0x8, payload)
}

// readFrame returns the next application frame. It transparently handles
// ping/pong and close frames. Clients must mask frames per RFC 6455.
func (c *wsConn) readFrame() (byte, []byte, error) {
	for {
		header := make([]byte, 2)
		if _, err := io.ReadFull(c.reader, header); err != nil {
			return 0, nil, err
		}
		fin := header[0]&0x80 != 0
		opcode := header[0] & 0x0f
		masked := header[1]&0x80 != 0
		length := int64(header[1] & 0x7f)
		switch length {
		case 126:
			extended := make([]byte, 2)
			if _, err := io.ReadFull(c.reader, extended); err != nil {
				return 0, nil, err
			}
			length = int64(binary.BigEndian.Uint16(extended))
		case 127:
			extended := make([]byte, 8)
			if _, err := io.ReadFull(c.reader, extended); err != nil {
				return 0, nil, err
			}
			length = int64(binary.BigEndian.Uint64(extended))
		}
		if length < 0 || length > 4<<20 {
			return 0, nil, fmt.Errorf("websocket frame is too large")
		}
		if !masked {
			return 0, nil, fmt.Errorf("client websocket frames must be masked")
		}
		if !fin {
			return 0, nil, fmt.Errorf("fragmented websocket frames are not supported")
		}
		mask := make([]byte, 4)
		if _, err := io.ReadFull(c.reader, mask); err != nil {
			return 0, nil, err
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(c.reader, payload); err != nil {
			return 0, nil, err
		}
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
		switch opcode {
		case 0x1, 0x2:
			return opcode, payload, nil
		case 0x8:
			_ = c.writeFrame(0x8, payload[:min(len(payload), 125)])
			return 0, nil, io.EOF
		case 0x9:
			if err := c.writeFrame(0xA, payload); err != nil {
				return 0, nil, err
			}
		case 0xA:
			continue
		default:
			return 0, nil, fmt.Errorf("unsupported websocket opcode %d", opcode)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
