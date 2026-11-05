package api

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// A small WebSocket client (RFC 6455), the client's side, with the standard library alone - enough for the terminal:
// the opening handshake over HTTP, text and binary messages out (masked, as a client's must be) and in, pings
// answered, and the closing handshake. No extension and no subprotocol is asked for. It is used only against our own
// API over this one connection; it is not a general WebSocket library.

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// The kinds of message that cross a WebSocket.
type wsOpcode byte

const (
	wsContinuation wsOpcode = 0x0
	wsText         wsOpcode = 0x1
	wsBinary       wsOpcode = 0x2
	wsClose        wsOpcode = 0x8
	wsPing         wsOpcode = 0x9
	wsPong         wsOpcode = 0xa
)

// The most a single message carries in from the server: a terminal's output comes in its own message, well under this.
const wsReadMost = 4 << 20

type wsConn struct {
	conn net.Conn
	r    *bufio.Reader
	// one writer at a time: output and pings and the close share the socket
	wmu sync.Mutex
	// the HTTP status the handshake answered, when it was not 101 (for the error)
	closed bool
}

// wsClosed is returned by readMessage when the server closed the connection.
type wsClosed struct {
	code   int
	reason string
}

func (e *wsClosed) Error() string {
	if e.reason != "" {
		return fmt.Sprintf("the terminal closed (%d): %s", e.code, e.reason)
	}
	return fmt.Sprintf("the terminal closed (%d)", e.code)
}

// wsDial opens a WebSocket to a ws:// or wss:// URL, with the headers given. The base URL and key come from the
// caller. ctx bounds the dial and the handshake; the connection then lives on its own.
func wsDial(ctx context.Context, rawURL string, header http.Header) (*wsConn, *http.Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, nil, err
	}
	secure := u.Scheme == "wss"
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), map[bool]string{true: "443", false: "80"}[secure])
	}
	var d net.Dialer
	raw, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, nil, err
	}
	conn := raw
	if secure {
		tconn := tls.Client(raw, &tls.Config{ServerName: u.Hostname()})
		if err := tconn.HandshakeContext(ctx); err != nil {
			raw.Close()
			return nil, nil, err
		}
		conn = tconn
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	var keyBytes [16]byte
	_, _ = rand.Read(keyBytes[:])
	key := base64.StdEncoding.EncodeToString(keyBytes[:])
	var b strings.Builder
	path := u.RequestURI()
	fmt.Fprintf(&b, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n", path, u.Host)
	fmt.Fprintf(&b, "Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n", key)
	for name, values := range header {
		for _, v := range values {
			fmt.Fprintf(&b, "%s: %s\r\n", name, v)
		}
	}
	b.WriteString("\r\n")
	if _, err := io.WriteString(conn, b.String()); err != nil {
		conn.Close()
		return nil, nil, err
	}
	br := bufio.NewReader(conn)
	res, err := http.ReadResponse(br, &http.Request{Method: "GET"})
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	if res.StatusCode != http.StatusSwitchingProtocols {
		// the caller reads the body for the API's problem; the connection is of no use
		res.Body = &connBody{ReadCloser: res.Body, conn: conn}
		return nil, res, nil
	}
	want := base64.StdEncoding.EncodeToString(sha1Sum(key + wsGUID))
	if res.Header.Get("Sec-WebSocket-Accept") != want {
		conn.Close()
		return nil, nil, errors.New("the server's WebSocket handshake did not match")
	}
	_ = conn.SetDeadline(time.Time{})
	return &wsConn{conn: conn, r: br}, res, nil
}

func sha1Sum(s string) []byte {
	h := sha1.Sum([]byte(s))
	return h[:]
}

// connBody lets a failed handshake's body be read and then closes the raw connection with it.
type connBody struct {
	io.ReadCloser
	conn net.Conn
}

func (b *connBody) Close() error {
	err := b.ReadCloser.Close()
	b.conn.Close()
	return err
}

// writeMessage sends one whole message of this kind, masked as a client's must be.
func (c *wsConn) writeMessage(op wsOpcode, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	var head [14]byte
	head[0] = 0x80 | byte(op) // FIN, this opcode
	n := 2
	switch {
	case len(payload) < 126:
		head[1] = byte(len(payload))
	case len(payload) < 1<<16:
		head[1] = 126
		binary.BigEndian.PutUint16(head[2:], uint16(len(payload)))
		n = 4
	default:
		head[1] = 127
		binary.BigEndian.PutUint64(head[2:], uint64(len(payload)))
		n = 10
	}
	head[1] |= 0x80 // masked
	var mask [4]byte
	_, _ = rand.Read(mask[:])
	copy(head[n:], mask[:])
	n += 4
	_ = c.conn.SetWriteDeadline(time.Now().Add(20 * time.Second))
	if _, err := c.conn.Write(head[:n]); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	masked := make([]byte, len(payload))
	for i, b := range payload {
		masked[i] = b ^ mask[i&3]
	}
	_, err := c.conn.Write(masked)
	return err
}

// readMessage returns the next whole data message (text or binary), answering pings and joining fragments on the way.
// A close from the server comes back as *wsClosed.
func (c *wsConn) readMessage() (wsOpcode, []byte, error) {
	var message []byte
	var messageOp wsOpcode
	have := false
	for {
		fin, op, payload, err := c.readFrame()
		if err != nil {
			return 0, nil, err
		}
		switch op {
		case wsPing:
			if err := c.writeMessage(wsPong, payload); err != nil {
				return 0, nil, err
			}
			continue
		case wsPong:
			continue
		case wsClose:
			code, reason := 1005, ""
			if len(payload) >= 2 {
				code = int(binary.BigEndian.Uint16(payload))
				reason = string(payload[2:])
			}
			c.sendClose(code)
			return 0, nil, &wsClosed{code: code, reason: reason}
		case wsContinuation:
			if !have {
				return 0, nil, errors.New("a continuation with nothing to continue")
			}
			message = append(message, payload...)
		case wsText, wsBinary:
			if have {
				return 0, nil, errors.New("a new message began before the last one ended")
			}
			have, messageOp, message = true, op, payload
		}
		if len(message) > wsReadMost {
			return 0, nil, errors.New("a message larger than any the terminal sends")
		}
		if fin && (op == wsText || op == wsBinary || op == wsContinuation) {
			return messageOp, message, nil
		}
	}
}

// readFrame reads one frame: its FIN bit, opcode and payload. A server's frames are never masked.
func (c *wsConn) readFrame() (fin bool, op wsOpcode, payload []byte, err error) {
	var head [2]byte
	if _, err = io.ReadFull(c.r, head[:]); err != nil {
		return
	}
	fin = head[0]&0x80 != 0
	op = wsOpcode(head[0] & 0x0f)
	masked := head[1]&0x80 != 0
	length := int(head[1] & 0x7f)
	switch length {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(c.r, ext[:]); err != nil {
			return
		}
		length = int(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(c.r, ext[:]); err != nil {
			return
		}
		n := binary.BigEndian.Uint64(ext[:])
		if n > wsReadMost {
			err = errors.New("a frame larger than any the terminal sends")
			return
		}
		length = int(n)
	}
	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(c.r, mask[:]); err != nil {
			return
		}
	}
	payload = make([]byte, length)
	if _, err = io.ReadFull(c.r, payload); err != nil {
		return
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i&3]
		}
	}
	return
}

// close sends a close frame and shuts the connection; sending a close is best-effort.
func (c *wsConn) close() error {
	c.sendClose(1000)
	c.wmu.Lock()
	c.closed = true
	c.wmu.Unlock()
	return c.conn.Close()
}

func (c *wsConn) sendClose(code int) {
	var payload [2]byte
	binary.BigEndian.PutUint16(payload[:], uint16(code))
	_ = c.writeMessage(wsClose, payload[:])
}
