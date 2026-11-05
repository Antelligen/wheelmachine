package api

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// A WebSocket server for the terminal client's tests: it accepts the upgrade, sends a binary banner, says keystrokes
// back in capitals, keeps the window it was resized to, and ends with exit code 7 on a 1x1 window. It records the
// query it was asked with.
type terminalEcho struct {
	srv   *httptest.Server
	query string
	sizes []string
}

func newTerminalEcho(t *testing.T) *terminalEcho {
	e := &terminalEcho{}
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			w.WriteHeader(401)
			return
		}
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			w.WriteHeader(400)
			return
		}
		e.query = r.URL.RawQuery
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + wsGUID))
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " +
			base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
		_ = rw.Flush()
		writeServerFrame(conn, 0x2, []byte("$ "))
		for {
			op, data, err := readClientFrame(rw.Reader)
			if err != nil {
				return
			}
			switch op {
			case 0x1:
				e.sizes = append(e.sizes, string(data))
				if strings.Contains(string(data), `"cols":1`) && strings.Contains(string(data), `"rows":1`) {
					writeServerFrame(conn, 0x1, []byte(`{"exit_code":7}`))
					writeServerFrame(conn, 0x8, closePayload(1000, "exit 7"))
					return
				}
			case 0x2:
				writeServerFrame(conn, 0x2, []byte(strings.ToUpper(string(data))))
			case 0x8:
				writeServerFrame(conn, 0x8, closePayload(1000, ""))
				return
			}
		}
	}))
	t.Cleanup(e.srv.Close)
	return e
}

func closePayload(code int, reason string) []byte {
	p := make([]byte, 2+len(reason))
	binary.BigEndian.PutUint16(p, uint16(code))
	copy(p[2:], reason)
	return p
}

func writeServerFrame(w io.Writer, op byte, payload []byte) {
	var head [10]byte
	head[0] = 0x80 | op
	n := 2
	if len(payload) < 126 {
		head[1] = byte(len(payload))
	} else {
		head[1] = 126
		binary.BigEndian.PutUint16(head[2:], uint16(len(payload)))
		n = 4
	}
	_, _ = w.Write(head[:n])
	_, _ = w.Write(payload)
}

func readClientFrame(r *bufio.Reader) (byte, []byte, error) {
	var head [2]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return 0, nil, err
	}
	length := int(head[1] & 0x7f)
	if length == 126 {
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		length = int(binary.BigEndian.Uint16(ext[:]))
	}
	var mask [4]byte
	if head[1]&0x80 != 0 {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	for i := range payload {
		payload[i] ^= mask[i&3]
	}
	return head[0] & 0x0f, payload, nil
}

func TestOpenTerminalCarriesEverythingAndEnds(t *testing.T) {
	e := newTerminalEcho(t)
	c, _ := newTestClient(t, e.srv.URL)
	term, err := c.OpenTerminal(context.Background(), "m_"+strings.Repeat("a", 26), TerminalOptions{Cols: 100, Rows: 30, User: "root", Command: []string{"bash", "-l"}})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	// the banner comes as output
	if out, err := term.Receive(); err != nil || string(out) != "$ " {
		t.Fatalf("banner %q %v", out, err)
	}
	// what is typed comes back upper-cased
	if err := term.Send([]byte("ls\n")); err != nil {
		t.Fatal(err)
	}
	if out, err := term.Receive(); err != nil || string(out) != "LS\n" {
		t.Fatalf("echo %q %v", out, err)
	}
	// a resize, then the end on 1x1
	if err := term.Resize(120, 40); err != nil {
		t.Fatal(err)
	}
	if err := term.Resize(1, 1); err != nil {
		t.Fatal(err)
	}
	for {
		_, err := term.Receive()
		if IsTerminalEnd(err) {
			break
		}
		if err != nil {
			t.Fatalf("waiting for the end: %v", err)
		}
	}
	if term.End() == nil || term.End().ExitCode == nil || *term.End().ExitCode != 7 {
		t.Fatalf("end: %+v", term.End())
	}
	q, _ := url.ParseQuery(e.query)
	if q.Get("cols") != "100" || q.Get("rows") != "30" || q.Get("user") != "root" || strings.Join(q["cmd"], " ") != "bash -l" {
		t.Fatalf("asked for: %s", e.query)
	}
	if len(e.sizes) == 0 || !strings.Contains(e.sizes[0], `"cols":120`) {
		t.Fatalf("resizes: %v", e.sizes)
	}
}

func TestOpenTerminalWithABadUserIsRefusedBeforeDialing(t *testing.T) {
	c, _ := newTestClient(t, "http://127.0.0.1:0")
	if _, err := c.OpenTerminal(context.Background(), "m_"+strings.Repeat("a", 26), TerminalOptions{User: "admin"}); err == nil {
		t.Fatal("a user that is not one must be refused")
	}
}

func TestOpenTerminalRefusalComesAsTheAPIProblem(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("X-Request-Id", "req_1")
		w.WriteHeader(402)
		_, _ = io.WriteString(w, `{"code":"payment_required","detail":"the balance is used up","status":402}`)
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL)
	_, err := c.OpenTerminal(context.Background(), "m_"+strings.Repeat("a", 26), TerminalOptions{})
	ae, ok := err.(*Error)
	if !ok || ae.Status != 402 || ae.Code != "payment_required" {
		t.Fatalf("want the API's problem, got %T %v", err, err)
	}
}

func TestSignSSHCertificate(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"object":"ssh_certificate","certificate":"ssh-ed25519-cert-v01@openssh.com AAAA box","principal":"m_x","ssh_user":"m_x","expires_at":"2026-10-23T13:00:00Z","ssh_host":"ssh.wheelmachine.ai:2222"}`)
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL)
	cert, err := c.SignSSHCertificate(context.Background(), SSHCertificateRequest{PublicKey: "ssh-ed25519 AAAA me", Machine: "m_x"})
	if err != nil {
		t.Fatal(err)
	}
	if cert.SSHUser != "m_x" || cert.SSHHost != "ssh.wheelmachine.ai:2222" || !strings.Contains(cert.Certificate, "cert-v01") {
		t.Fatalf("cert: %+v", cert)
	}
	if !strings.Contains(gotBody, `"public_key":"ssh-ed25519 AAAA me"`) || !strings.Contains(gotBody, `"machine":"m_x"`) {
		t.Fatalf("body sent: %s", gotBody)
	}
}
