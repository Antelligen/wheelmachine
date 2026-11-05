package fakeapi

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// A terminal as the API offers one (a WebSocket upgrade), for the tests of wm ssh: what is typed comes back in
// capitals, a resize is noted (the last window kept), and a 1x1 window ends the terminal with exit code 7 - so a test
// can drive a whole terminal without a machine. The query it was asked with is kept for the test to check.
func (s *Server) terminal(w http.ResponseWriter, r *http.Request) {
	m := s.machine(w, r)
	if m == nil {
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		refuse(w, r, Refusal{Status: 426, Code: "invalid_request", Detail: "a terminal is a WebSocket"})
		return
	}
	if u := r.URL.Query().Get("user"); u != "" && u != "user" && u != "root" {
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "user is `user` or `root`"})
		return
	}
	s.mu.Lock()
	s.terminalQuery = r.URL.Query()
	s.mu.Unlock()
	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		refuse(w, r, Refusal{Status: 500, Code: "internal", Detail: err.Error()})
		return
	}
	defer conn.Close()
	accept := base64.StdEncoding.EncodeToString(sha1sum(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept + "\r\n\r\n")
	_ = rw.Flush()
	writeWSBinary(conn, []byte("$ ")) // the first output is a binary message, as a terminal's output is
	var typed string                  // keystrokes so far, for whole lines (a frame may carry more than a line, or less)
	for {
		op, data, err := readWSFrame(rw.Reader)
		if err != nil {
			return
		}
		switch op {
		case 0x1: // text: a resize
			if strings.Contains(string(data), `"resize"`) {
				s.mu.Lock()
				s.terminalResizes = append(s.terminalResizes, string(data))
				s.mu.Unlock()
				if strings.Contains(string(data), `"cols":1`) && strings.Contains(string(data), `"rows":1`) {
					writeWSText(conn, `{"exit_code":7}`)
					writeWSClose(conn, 1000, "exit 7")
					return
				}
			}
		case 0x2: // binary: keystrokes, said back in capitals - a whole line "exit <n>" ends it with that code
			writeWSBinary(conn, []byte(strings.ToUpper(string(data))))
			typed += string(data)
			for {
				at := strings.IndexAny(typed, "\r\n")
				if at < 0 {
					break
				}
				line := typed[:at]
				typed = typed[at+1:]
				if n, ok := exitLine(line); ok {
					writeWSText(conn, `{"exit_code":`+itoa(n)+`}`)
					writeWSClose(conn, 1000, "exit "+itoa(n))
					return
				}
			}
		case 0x8: // the client closed
			writeWSClose(conn, 1000, "")
			return
		}
	}
}

// TerminalQuery is what the last terminal was asked for.
func (s *Server) TerminalQuery() url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminalQuery
}

// TerminalResizes is every resize the last terminal received, as JSON.
func (s *Server) TerminalResizes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.terminalResizes...)
}

func sha1sum(s string) []byte { h := sha1.Sum([]byte(s)); return h[:] }

// A server frame, unmasked, FIN set.
func writeWSBinary(w io.Writer, payload []byte) { writeWSFrame(w, 0x2, payload) }
func writeWSText(w io.Writer, s string)         { writeWSFrame(w, 0x1, []byte(s)) }
func writeWSClose(w io.Writer, code int, reason string) {
	payload := make([]byte, 2+len(reason))
	binary.BigEndian.PutUint16(payload, uint16(code))
	copy(payload[2:], reason)
	writeWSFrame(w, 0x8, payload)
}

func writeWSFrame(w io.Writer, op byte, payload []byte) {
	var head [10]byte
	head[0] = 0x80 | op
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
	_, _ = w.Write(head[:n])
	_, _ = w.Write(payload)
}

// readWSFrame reads one client frame (always masked), FIN assumed.
func readWSFrame(r *bufio.Reader) (op byte, payload []byte, err error) {
	var head [2]byte
	if _, err = io.ReadFull(r, head[:]); err != nil {
		return
	}
	op = head[0] & 0x0f
	length := int(head[1] & 0x7f)
	switch length {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(r, ext[:]); err != nil {
			return
		}
		length = int(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(r, ext[:]); err != nil {
			return
		}
		length = int(binary.BigEndian.Uint64(ext[:]))
	}
	var mask [4]byte
	if head[1]&0x80 != 0 {
		if _, err = io.ReadFull(r, mask[:]); err != nil {
			return
		}
	}
	payload = make([]byte, length)
	if _, err = io.ReadFull(r, payload); err != nil {
		return
	}
	for i := range payload {
		payload[i] ^= mask[i&3]
	}
	return
}

// exitLine reads a line "exit <n>" typed at the terminal, as a shell takes one; ok is false otherwise.
func exitLine(line string) (int, bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(line), "exit ")
	if !found {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(rest))
	return n, err == nil
}

func itoa(n int) string { return strconv.Itoa(n) }
