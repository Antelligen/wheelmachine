package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testKey = "ak_live_abcdefghijklmnopqrstuvwxyz234567abcdefgh"

// waits records the waits between tries instead of waiting.
type waits struct {
	mu sync.Mutex
	d  []time.Duration
}

func (w *waits) sleep(ctx context.Context, d time.Duration) error {
	w.mu.Lock()
	w.d = append(w.d, d)
	w.mu.Unlock()
	return ctx.Err()
}

func (w *waits) all() []time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]time.Duration(nil), w.d...)
}

func newTestClient(t *testing.T, base string, opts ...Option) (*Client, *waits) {
	t.Helper()
	c, err := New(append([]Option{WithBaseURL(base), WithKey(testKey)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	w := &waits{}
	c.sleep = w.sleep
	c.jitter = func(time.Duration) time.Duration { return 0 }
	return c, w
}

func writeProblem(w http.ResponseWriter, status int, code, detail string, retryAfter string) {
	if retryAfter != "" {
		w.Header().Set("Retry-After", retryAfter)
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("X-Request-Id", "req_header")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "https://docs.example/errors/" + code, "title": "t", "status": status,
		"code": code, "detail": detail, "request_id": "req_body"})
}

func machineJSON(w http.ResponseWriter, status int, id string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"id":%q,"object":"machine","name":"","project":"prj_1","state":"running","gpu":null,"size":null,"location":null,"labels":{},"files_sealed":true,"running_seconds":0,"created_at":"2026-10-23T12:00:00Z","last_used_at":null}`, id)
}

func TestRetryHonoursRetryAfter(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) <= 2 {
			writeProblem(w, 503, CodeUnavailable, "not available just now", "2")
			return
		}
		machineJSON(w, 200, "m_00000000000000000000000001")
	}))
	defer srv.Close()
	c, w := newTestClient(t, srv.URL)
	m, err := c.GetMachine(context.Background(), "m_00000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if m.State != StateRunning || n.Load() != 3 {
		t.Fatalf("state %q after %d tries", m.State, n.Load())
	}
	if got := w.all(); len(got) != 2 || got[0] != 2*time.Second || got[1] != 2*time.Second {
		t.Fatalf("waits %v, want two of 2s", got)
	}
}

func TestBackoffIsFullJitterUnderADoublingCeiling(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) <= 4 {
			writeProblem(w, 429, CodeRateLimited, "this key's limit of 600 calls a minute is reached", "")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"mumbai","city":"Mumbai","country":"IN"}]}`)
	}))
	defer srv.Close()
	c, w := newTestClient(t, srv.URL, WithRetryPolicy(RetryPolicy{MaxAttempts: 5, BaseDelay: 100 * time.Millisecond,
		MaxDelay: 250 * time.Millisecond, MaxRetryAfter: time.Minute}))
	var ceilings []time.Duration
	c.jitter = func(ceiling time.Duration) time.Duration {
		ceilings = append(ceilings, ceiling)
		return ceiling / 2
	}
	var events []RetryEvent
	c.onRetry = func(e RetryEvent) { events = append(events, e) }
	regions, err := c.Regions(context.Background())
	if err != nil || len(regions) != 1 {
		t.Fatalf("regions %v, %v", regions, err)
	}
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 250 * time.Millisecond, 250 * time.Millisecond}
	if fmt.Sprint(ceilings) != fmt.Sprint(want) {
		t.Fatalf("ceilings %v, want %v", ceilings, want)
	}
	if got := w.all(); got[0] != 50*time.Millisecond || got[3] != 125*time.Millisecond {
		t.Fatalf("waits %v: each is drawn under its ceiling", got)
	}
	if len(events) != 4 || events[0].Attempt != 1 || !IsCode(events[0].Err, CodeRateLimited) {
		t.Fatalf("retry events %+v", events)
	}
}

func TestFullJitterStaysUnderItsCeiling(t *testing.T) {
	for range 1000 {
		if d := fullJitter(time.Second); d < 0 || d > time.Second {
			t.Fatalf("%v is outside [0, 1s]", d)
		}
	}
	if fullJitter(0) != 0 {
		t.Fatal("no ceiling, no wait")
	}
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		writeProblem(w, 504, CodeTimeout, "that took too long; try again in a moment", "")
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL, WithRetryPolicy(RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}))
	_, err := c.Me(context.Background())
	if StatusOf(err) != 504 || n.Load() != 3 {
		t.Fatalf("err %v after %d tries", err, n.Load())
	}
}

func TestIdempotencyKeyIsMadeOnceAndSentOnEveryTry(t *testing.T) {
	var mu sync.Mutex
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		n := len(keys)
		mu.Unlock()
		if n == 1 || n == 3 {
			writeProblem(w, 503, CodeUnavailable, "not available just now", "1")
			return
		}
		machineJSON(w, 201, "m_0000000000000000000000000"+strconv.Itoa(n))
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL)
	if _, err := c.CreateMachine(context.Background(), MachineCreate{Name: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateMachine(context.Background(), MachineCreate{Name: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateMachine(context.Background(), MachineCreate{Name: "a", IdempotencyKey: "mine-1"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(keys) != 5 {
		t.Fatalf("%d requests, want 5", len(keys))
	}
	if keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("a retry must carry the first try's key: %q then %q", keys[0], keys[1])
	}
	if keys[2] == keys[0] || keys[2] != keys[3] {
		t.Fatalf("each create has its own key, kept across its tries: %q", keys)
	}
	if keys[4] != "mine-1" {
		t.Fatalf("a key given is used as it is, got %q", keys[4])
	}
	for _, k := range keys[:4] {
		if len(k) > 255 || strings.ContainsFunc(k, func(r rune) bool { return r < 0x21 || r > 0x7e }) {
			t.Fatalf("key %q is not a printable header of at most 255 characters", k)
		}
	}
}

func TestIdempotentCallStillRunningIsWaitedFor(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			writeProblem(w, 409, CodeConflict, "the first call with this Idempotency-Key is still running", "1")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"id":"prj_00000000000000000000000001","object":"project","name":"x","created_at":"2026-10-23T12:00:00Z"}`)
	}))
	defer srv.Close()
	c, w := newTestClient(t, srv.URL)
	p, err := c.CreateProject(context.Background(), ProjectCreate{Name: "x"})
	if err != nil || p.Name != "x" || n.Load() != 2 || w.all()[0] != time.Second {
		t.Fatalf("project %v, err %v, %d tries, waits %v", p, err, n.Load(), w.all())
	}
}

func TestCommandIsNotSentAgainWhenItMayHaveRun(t *testing.T) {
	var n atomic.Int32
	status := 503
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		if r.Header.Get("Idempotency-Key") != "" {
			t.Error("a command never carries an Idempotency-Key")
		}
		writeProblem(w, status, CodeUnavailable, "we could not run the command just now", "1")
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL)
	for _, s := range []int{503, 504} {
		n.Store(0)
		status = s
		_, err := c.Exec(context.Background(), "m_00000000000000000000000001", ExecRequest{Command: []string{"true"}})
		if StatusOf(err) != s || n.Load() != 1 {
			t.Fatalf("%d: err %v after %d tries; a command that may have run is never sent twice", s, err, n.Load())
		}
	}
}

func TestCommandRefusedBeforeRunningIsTriedAgain(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			writeProblem(w, 429, CodeRateLimited, "too many calls", "1")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"exit_code":3,"stdout":"hi\n","stderr":"","duration_ms":4,"truncated":false}`)
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL)
	res, err := c.Exec(context.Background(), "m_00000000000000000000000001", ExecRequest{Shell: "echo hi; exit 3"})
	if err != nil || res.ExitCode != 3 || res.Stdout != "hi\n" || n.Load() != 2 {
		t.Fatalf("res %+v, err %v, %d tries", res, err, n.Load())
	}
}

func portJSON(port int, public bool) string {
	return fmt.Sprintf(`{"object":"port","machine":"m_00000000000000000000000001","port":%d,"public":%t,`+
		`"url":"https://%d-00000000000000000000000001.run.wheelmachine.ai","created_at":"2026-10-23T12:00:00Z"}`, port, public, port)
}

// linkJSON is a link to port 8080, made at noon. Its url is never printed by a test, even this one's.
const linkJSON = `{"object":"port_link","machine":"m_00000000000000000000000001","port":8080,` +
	`"url":"https://8080-00000000000000000000000001.run.wheelmachine.ai/.wheelmachine/link/t0k3n","expires_at":"2026-10-23T12:05:00Z","access_s":600}`

func TestPortCalls(t *testing.T) {
	type sent struct{ method, path, idemKey, body string }
	var mu sync.Mutex
	var got []sent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, sent{r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"), string(b)})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + strings.TrimPrefix(r.URL.Path, "/v1/machines/m_00000000000000000000000001") {
		case "POST /ports":
			w.WriteHeader(201)
			_, _ = io.WriteString(w, portJSON(8080, true))
		case "GET /ports":
			fmt.Fprintf(w, `{"object":"list","data":[%s,%s]}`, portJSON(3000, false), portJSON(8080, true))
		case "DELETE /ports/8080":
			w.WriteHeader(204)
		case "POST /ports/8080/link":
			w.WriteHeader(201)
			_, _ = io.WriteString(w, linkJSON)
		default:
			writeProblem(w, 404, CodeNotFound, "no such route", "")
		}
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL)
	ctx := context.Background()
	id := "m_00000000000000000000000001"
	noon := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

	p, err := c.OpenPort(ctx, id, 8080, true)
	if err != nil || p.Port != 8080 || !p.Public || p.Machine != id || p.URL != "https://8080-00000000000000000000000001.run.wheelmachine.ai" ||
		!p.CreatedAt.Equal(noon) {
		t.Fatalf("port %+v, %v", p, err)
	}
	ports, err := c.ListPorts(ctx, id)
	if err != nil || len(ports) != 2 || ports[0].Port != 3000 || ports[0].Public || ports[1].Port != 8080 || !ports[1].Public {
		t.Fatalf("ports %+v, %v", ports, err)
	}
	if err := c.ClosePort(ctx, id, 8080); err != nil {
		t.Fatal(err)
	}
	l, err := c.LinkPort(ctx, id, 8080, PortLinkCreate{AccessS: 600, Path: "/app?tab=logs"})
	if err != nil {
		t.Fatal(err)
	}
	if l.Port != 8080 || l.Machine != id || l.AccessS != 600 || !l.ExpiresAt.Equal(noon.Add(5*time.Minute)) ||
		!strings.HasPrefix(l.URL, "https://8080-00000000000000000000000001.run.wheelmachine.ai/.wheelmachine/link/") {
		t.Fatalf("link to port %d, for %ds, until %v, its url as sent %v", l.Port, l.AccessS, l.ExpiresAt,
			l.URL != "" && strings.Contains(linkJSON, `"url":"`+l.URL+`"`))
	}
	if _, err := c.LinkPort(ctx, id, 8080, PortLinkCreate{}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	route := "/v1/machines/" + id + "/ports"
	want := []sent{
		{http.MethodPost, route, "", `{"port":8080,"public":true}`},
		{http.MethodGet, route, "", ""},
		{http.MethodDelete, route + "/8080", "", ""},
		{http.MethodPost, route + "/8080/link", "", `{"access_s":600,"path":"/app?tab=logs"}`},
		{http.MethodPost, route + "/8080/link", "", `{}`},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("sent %q\nwant %q", got, want)
	}
}

func TestAPortThatIsNotOneIsRefusedBeforeAnyCall(t *testing.T) {
	c, _ := newTestClient(t, "http://127.0.0.1:1")
	ctx := context.Background()
	id := "m_00000000000000000000000001"
	for what, err := range map[string]error{
		"open 0":         second(c.OpenPort(ctx, id, 0, false)),
		"open 65536":     second(c.OpenPort(ctx, id, 65536, true)),
		"close -1":       c.ClosePort(ctx, id, -1),
		"link 70000":     second(c.LinkPort(ctx, id, 70000, PortLinkCreate{})),
		"open, no id":    second(c.OpenPort(ctx, "", 8080, false)),
		"list elsewhere": second(c.ListPorts(ctx, "m_1/../../keys")),
		"link elsewhere": second(c.LinkPort(ctx, "m_1?x=1", 8080, PortLinkCreate{})),
	} {
		var e *Error
		if !errors.As(err, &e) || e.Code != CodeInvalidRequest || e.Status != 0 {
			t.Fatalf("%s: err %v; refused here, never sent", what, err)
		}
	}
	if err := c.ClosePort(ctx, id, 0); !strings.Contains(err.Error(), "0 is not a port: a port is 1 to 65535") {
		t.Fatalf("message %q", err)
	}
}

// second is a call's error alone.
func second[T any](_ T, err error) error { return err }

func TestNotOpenIsNeverWaitedFor(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		n.Add(1)
		writeProblem(w, 503, CodeNotOpen, "web addresses for machines are not open yet", "1")
	}))
	defer srv.Close()
	c, w := newTestClient(t, srv.URL)
	ctx := context.Background()
	id := "m_00000000000000000000000001"
	// all but the link are tried again after a 503 of another code: a move to a state, reads, a delete, a create
	for what, call := range map[string]func() error{
		"open a port":    func() error { return second(c.OpenPort(ctx, id, 8080, false)) },
		"list ports":     func() error { return second(c.ListPorts(ctx, id)) },
		"close a port":   func() error { return c.ClosePort(ctx, id, 8080) },
		"link a port":    func() error { return second(c.LinkPort(ctx, id, 8080, PortLinkCreate{})) },
		"get a machine":  func() error { return second(c.GetMachine(ctx, id)) },
		"make a machine": func() error { return second(c.CreateMachine(ctx, MachineCreate{})) },
	} {
		n.Store(0)
		if err := call(); !IsCode(err, CodeNotOpen) || StatusOf(err) != 503 || n.Load() != 1 {
			t.Fatalf("%s: err %v after %d tries; what is not open yet does not come by waiting", what, err, n.Load())
		}
	}
	if got := w.all(); len(got) != 0 {
		t.Fatalf("waited %v", got)
	}
}

func TestAPortIsOpenedAgainAfterAFailedTryButALinkIsNeverAskedForTwice(t *testing.T) {
	var n atomic.Int32
	var fail atomic.Value // how the first try of each call fails
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if n.Add(1) == 1 {
			switch fail.Load() {
			case "hang up":
				hangUp(w)
			case "502":
				w.WriteHeader(502) // a proxy's answer, after the API may have made the link
			case "503":
				writeProblem(w, 503, CodeUnavailable, "the machine could not be reached just now", "1")
			case "504":
				writeProblem(w, 504, CodeTimeout, "that took too long; try again in a moment", "")
			case "429":
				writeProblem(w, 429, CodeRateLimited, "this key's limit of 600 calls a minute is reached", "1")
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		if strings.HasSuffix(r.URL.Path, "/link") {
			_, _ = io.WriteString(w, linkJSON)
		} else {
			_, _ = io.WriteString(w, portJSON(8080, false))
		}
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL)
	ctx := context.Background()
	id := "m_00000000000000000000000001"
	for _, how := range []string{"503", "504", "hang up", "429"} {
		n.Store(0)
		fail.Store(how)
		if p, err := c.OpenPort(ctx, id, 8080, false); err != nil || p.Port != 8080 || n.Load() != 2 {
			t.Fatalf("%s: opening a port is safe to send again: err %v after %d tries", how, err, n.Load())
		}
	}
	for _, how := range []string{"502", "503", "504", "hang up"} {
		n.Store(0)
		fail.Store(how)
		if _, err := c.LinkPort(ctx, id, 8080, PortLinkCreate{}); err == nil || n.Load() != 1 {
			t.Fatalf("%s: a link that may have been made is never asked for again: err %v after %d tries", how, err, n.Load())
		}
	}
	n.Store(0)
	fail.Store("429")
	if l, err := c.LinkPort(ctx, id, 8080, PortLinkCreate{}); err != nil || l.Port != 8080 || n.Load() != 2 {
		t.Fatalf("a link refused before anything was made is asked for again: err %v after %d tries", err, n.Load())
	}
}

func TestLimitOfMachinesIsNotWaitedFor(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		writeProblem(w, 429, CodeLimitReached, "an account holds at most 100 machines through the API; delete one first", "")
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL)
	_, err := c.CreateMachine(context.Background(), MachineCreate{})
	if !IsCode(err, CodeLimitReached) || n.Load() != 1 {
		t.Fatalf("err %v after %d tries", err, n.Load())
	}
}

func TestRetryAfterLongerThanAllowedIsReturned(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		writeProblem(w, 503, CodeUnavailable, "not available just now", "3600")
	}))
	defer srv.Close()
	c, w := newTestClient(t, srv.URL)
	_, err := c.Me(context.Background())
	var e *Error
	if !errors.As(err, &e) || e.RetryAfter != time.Hour || !e.HasRetryAfter() || n.Load() != 1 || len(w.all()) != 0 {
		t.Fatalf("err %v after %d tries", err, n.Load())
	}
}

func TestRetryAfterAsADate(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	d, ok := parseRetryAfter(now.Add(90*time.Second).Format(http.TimeFormat), now)
	if !ok || d != 90*time.Second {
		t.Fatalf("got %v %v", d, ok)
	}
	if _, ok := parseRetryAfter("soon", now); ok {
		t.Fatal("words are not a Retry-After")
	}
}

// hangUp closes the connection under the first request that reaches it.
func hangUp(w http.ResponseWriter) {
	conn, _, err := w.(http.Hijacker).Hijack()
	if err == nil {
		_ = conn.Close()
	}
}

func TestBrokenConnectionIsTriedAgainForAReadButNotForACommand(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if n.Add(1) == 1 || r.Method == http.MethodPost {
			hangUp(w)
			return
		}
		machineJSON(w, 200, "m_00000000000000000000000001")
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL)
	if _, err := c.GetMachine(context.Background(), "m_00000000000000000000000001"); err != nil || n.Load() != 2 {
		t.Fatalf("a read is tried again after a broken connection: err %v after %d tries", err, n.Load())
	}
	n.Store(10)
	_, err := c.Exec(context.Background(), "m_00000000000000000000000001", ExecRequest{Command: []string{"date"}})
	var ce *ConnError
	if !errors.As(err, &ce) || n.Load() != 11 {
		t.Fatalf("a command whose connection broke after it was sent is not sent again: err %v, %d tries", err, n.Load()-10)
	}
}

func TestNothingListeningIsTriedAgainEvenForACommand(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	c, w := newTestClient(t, "http://"+addr, WithRetryPolicy(RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}))
	_, err = c.Exec(context.Background(), "m_00000000000000000000000001", ExecRequest{Command: []string{"date"}})
	var ce *ConnError
	if !errors.As(err, &ce) || len(w.all()) != 2 {
		t.Fatalf("err %v after %d waits: a command that never left is safe to send again", err, len(w.all()))
	}
	if !strings.Contains(err.Error(), "could not reach the API at "+addr) {
		t.Fatalf("message %q", err)
	}
}

func TestAttemptTimeoutIsWorded(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	c, _ := newTestClient(t, srv.URL, WithRetryPolicy(RetryPolicy{MaxAttempts: 1}))
	err := c.decode(context.Background(), call{method: http.MethodPost, path: "/v1/x", timeout: 50 * time.Millisecond}, nil)
	if err == nil || !strings.Contains(err.Error(), "no answer within 50ms") {
		t.Fatalf("err %v", err)
	}
}

func TestCallerCancelEndsTheWait(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, 503, CodeUnavailable, "not available", "30")
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL)
	c.sleep = sleepContext
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	began := time.Now()
	_, err := c.Me(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(began) > 5*time.Second {
		t.Fatalf("err %v after %v", err, time.Since(began))
	}
}

func TestProblemBecomesATypedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, 404, CodeNotFound, "no such machine", "")
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL)
	_, err := c.GetMachine(context.Background(), "m_00000000000000000000000001")
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("err %T %v", err, err)
	}
	if e.Status != 404 || e.Code != CodeNotFound || e.Detail != "no such machine" || e.RequestID != "req_body" ||
		e.Type != "https://docs.example/errors/not_found" {
		t.Fatalf("error %+v", e)
	}
	if got, want := err.Error(), "no such machine (not_found, 404, request req_body)"; got != want {
		t.Fatalf("message %q, want %q", got, want)
	}
	if !IsCode(err, CodeNotFound) || IsCode(err, CodeConflict) || StatusOf(errors.New("x")) != 0 {
		t.Fatal("IsCode / StatusOf")
	}
}

func TestAnAnswerThatIsNotAProblemIsKeptByItsStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "req_proxy")
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(502)
		_, _ = io.WriteString(w, "<html>upstream is down</html>")
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL)
	_, err := c.Me(context.Background())
	if got, want := err.Error(), "bad gateway (502, request req_proxy)"; got != want {
		t.Fatalf("message %q, want %q: a page that is not a problem is never shown", got, want)
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	var elsewhere atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { elsewhere.Store(true) }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL)
	if _, err := c.Me(context.Background()); StatusOf(err) != 302 || elsewhere.Load() {
		t.Fatalf("err %v; the key must never be carried elsewhere", err)
	}
}

func TestHeaders(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"me","account":{"id":"acc_1"},"key":{"id":"key_00000000000000a1","object":"key","name":"n","scopes":["cloud:*"],"projects":[],"shown":"x","state":"active","rate_per_minute":null,"created_at":"2026-10-23T12:00:00Z","expires_at":null,"last_used_at":null},"standing":{"can_spend":true,"balance_usd":null}}`)
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL, WithUserAgent("wm/9.9"))
	me, err := c.Me(context.Background())
	if err != nil || me.Account.ID != "acc_1" || me.Standing.BalanceUSD != nil {
		t.Fatalf("me %+v, %v", me, err)
	}
	if got.Get("Authorization") != "Bearer "+testKey || got.Get("Accept") != "application/json" ||
		!strings.HasPrefix(got.Get("User-Agent"), "wm/9.9 wheelmachine-go/") {
		t.Fatalf("headers %v", got)
	}
}

func TestPagesAreWalkedUntilNextIsNull(t *testing.T) {
	ids := []string{"m_5", "m_4", "m_3", "m_2", "m_1"}
	var afters []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		after := r.URL.Query().Get("after")
		afters = append(afters, after)
		if r.URL.Query().Get("limit") != "2" || r.URL.Query()["label"][0] != "a=1" || r.URL.Query()["label"][1] != "b=2" {
			t.Errorf("query %v", r.URL.Query())
		}
		start := 0
		for i, id := range ids {
			if id == after {
				start = i + 1
			}
		}
		end := min(start+2, len(ids))
		var data []string
		for _, id := range ids[start:end] {
			data = append(data, fmt.Sprintf(`{"id":%q,"object":"machine","state":"running","labels":{},"created_at":"2026-10-23T12:00:00Z"}`, id))
		}
		next := "null"
		if end < len(ids) {
			next = strconv.Quote(ids[end-1])
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"object":"list","data":[%s],"next":%s}`, strings.Join(data, ","), next)
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL)
	p := ListMachinesParams{Limit: 2, Labels: map[string]string{"b": "2", "a": "1"}}
	var got []string
	for m, err := range c.AllMachines(context.Background(), p) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, m.ID)
	}
	if strings.Join(got, " ") != strings.Join(ids, " ") || strings.Join(afters, ",") != ",m_4,m_2" {
		t.Fatalf("got %v with afters %q", got, afters)
	}
	afters, got = nil, nil
	for m, err := range c.AllMachines(context.Background(), p) {
		if err != nil {
			t.Fatal(err)
		}
		if got = append(got, m.ID); len(got) == 3 {
			break
		}
	}
	if len(afters) != 2 {
		t.Fatalf("a page is asked for only when it is needed: %d asked", len(afters))
	}
}

func TestPagesPassOverAnEmptyPageAndStopOnALoop(t *testing.T) {
	pages := map[string]Page[int]{
		"":  {Data: nil, Next: Ptr("a")},
		"a": {Data: []int{1, 2}, Next: Ptr("b")},
		"b": {Data: []int{3}, Next: Ptr("a")},
	}
	var got []int
	for v, err := range all("", func(after string) (*Page[int], error) {
		p := pages[after]
		return &p, nil
	}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, v)
	}
	if fmt.Sprint(got) != "[1 2 3]" {
		t.Fatalf("got %v", got)
	}
	boom := errors.New("boom")
	for _, err := range all("", func(string) (*Page[int], error) { return nil, boom }) {
		if !errors.Is(err, boom) {
			t.Fatalf("err %v", err)
		}
	}
}

func TestFilesAreStreamed(t *testing.T) {
	var mu sync.Mutex
	stored := map[string][]byte{}
	var lengths []int64
	var tries atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Query().Get("path")
		switch r.Method {
		case http.MethodPut:
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			lengths = append(lengths, r.ContentLength)
			mu.Unlock()
			if r.Header.Get("Content-Type") != "application/octet-stream" {
				t.Errorf("content type %q", r.Header.Get("Content-Type"))
			}
			if path == "work/flaky.bin" && tries.Add(1) == 1 {
				writeProblem(w, 503, CodeUnavailable, "the machine's files could not be reached just now", "1")
				return
			}
			mu.Lock()
			stored[path] = b
			mu.Unlock()
			w.WriteHeader(204)
		case http.MethodGet:
			mu.Lock()
			b, ok := stored[path]
			mu.Unlock()
			if !ok {
				writeProblem(w, 404, CodeNotFound, "no such file in the machine", "")
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(b)))
			_, _ = w.Write(b)
		}
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL)
	ctx := context.Background()
	id := "m_00000000000000000000000001"

	big := bytes.Repeat([]byte("0123456789abcdef"), 1<<16) // 1 MiB
	local := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(local, big, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(local)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := c.WriteFile(ctx, id, "work/flaky.bin", f, int64(len(big))); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored["work/flaky.bin"], big) || tries.Load() != 2 {
		t.Fatalf("the file came whole after %d tries: the second one wound the file back", tries.Load())
	}
	if err := c.WriteFile(ctx, id, "home/empty", bytes.NewReader(nil), 0); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if lengths[0] != int64(len(big)) || lengths[2] != 0 {
		t.Fatalf("Content-Length sent %v", lengths)
	}
	mu.Unlock()

	rf, err := c.ReadFile(ctx, id, "work/flaky.bin")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rf)
	_ = rf.Close()
	if err != nil || rf.Size != int64(len(big)) || !bytes.Equal(got, big) {
		t.Fatalf("read %d of %d bytes, %v", len(got), rf.Size, err)
	}
	if _, err := c.ReadFile(ctx, id, "home/none"); !IsCode(err, CodeNotFound) {
		t.Fatalf("err %v", err)
	}
	if err := c.WriteFile(ctx, id, "home/x", strings.NewReader("x"), -1); !IsCode(err, CodeInvalidRequest) {
		t.Fatalf("a file of no known length is refused here: %v", err)
	}
}

func TestAFileThatCannotBeWoundBackIsNotSentTwice(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		n.Add(1)
		writeProblem(w, 503, CodeUnavailable, "not available", "1")
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL)
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte("abc"))
		_ = pw.Close()
	}()
	if err := c.WriteFile(context.Background(), "m_00000000000000000000000001", "work/a", pr, 3); StatusOf(err) != 503 || n.Load() != 1 {
		t.Fatalf("err %v after %d tries", err, n.Load())
	}
}

func TestWaitAsksAtMostTheLongestWait(t *testing.T) {
	var q string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.RawQuery
		machineJSON(w, 200, "m_00000000000000000000000001")
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL)
	if _, err := c.WaitForMachine(context.Background(), "m_00000000000000000000000001", StatePaused, 20*time.Minute); err != nil {
		t.Fatal(err)
	}
	if q != "state=paused&timeout_s=600" {
		t.Fatalf("query %q", q)
	}
}

func TestAnIDNeverNamesAnotherRoute(t *testing.T) {
	c, _ := newTestClient(t, "http://127.0.0.1:1")
	for _, id := range []string{"", "..", "m_1/../../keys", "m_1?x=1", "a b"} {
		if _, err := c.GetMachine(context.Background(), id); !IsCode(err, CodeInvalidRequest) {
			t.Fatalf("%q: err %v", id, err)
		}
	}
}

func TestTheKeyIsNeverShown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, 401, CodeUnauthorized, "this key is not known", "")
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL)
	_, err := c.Me(context.Background())
	var logs bytes.Buffer
	slog.New(slog.NewTextHandler(&logs, nil)).Info("x", "client", c, "key", c.key, "config", Config{APIKey: testKey})
	cfg := Config{APIKey: testKey}
	printed := strings.Join([]string{err.Error(), fmt.Sprintf("%v %+v %#v %s %q %x", c, c, c, c, c, c),
		fmt.Sprintf("%v %+v %#v %s", *c, *c, c.key, c.key), fmt.Sprintf("%v %+v %#v %s", cfg, &cfg, cfg, cfg), logs.String()}, "\n")
	if strings.Contains(printed, testKey) || strings.Contains(printed, "abcdefghijklmnopqrstuvwxyz") {
		t.Fatalf("the key was shown:\n%s", printed)
	}
	if b, _ := json.Marshal(struct{ K secret }{c.key}); strings.Contains(string(b), testKey) {
		t.Fatalf("encoded: %s", b)
	}
}

func TestKeysThatAreNotKeysAreRefusedBeforeAnyCall(t *testing.T) {
	for _, k := range []string{"ak_live_with space", "ak_live_\nx", "ak_live_é"} {
		if _, err := New(WithKey(k)); err == nil || strings.Contains(err.Error(), k) {
			t.Fatalf("%q: err %v", k, err)
		}
	}
	if _, err := New(); !errors.Is(err, ErrNoKey) {
		t.Fatalf("err %v", err)
	}
}

func TestBaseURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://api.wheelmachine.ai": true, "https://api.wheelmachine.ai/": true, "http://127.0.0.1:8080": true,
		"http://localhost:9": true, "http://[::1]:9": true, "http://api.wheelmachine.ai": false, "ftp://x": false,
		"api.wheelmachine.ai": false, "https://user:pw@x": false, "https://x/?a=1": false,
	} {
		if _, err := parseBase(raw); (err == nil) != ok {
			t.Errorf("%q: err %v", raw, err)
		}
	}
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL+"/edge/")
	if _, err := c.Regions(context.Background()); err != nil || path != "/edge/v1/regions" {
		t.Fatalf("path %q, %v", path, err)
	}
}

func TestConfigIsKeptPrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wheelmachine", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"api_key":"old"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveConfig(path, Config{APIKey: testKey}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", st.Mode().Perm())
	}
	cfg, err := LoadConfig(path)
	if err != nil || cfg.APIKey != testKey {
		t.Fatalf("loaded %v, %v", cfg.APIKey == testKey, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("a file left beside the settings: %v", entries)
	}
	fresh := filepath.Join(dir, "new", "wheelmachine", "config.json")
	if err := SaveConfig(fresh, Config{APIKey: testKey}); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(filepath.Dir(fresh)); runtime.GOOS != "windows" && st.Mode().Perm() != 0o700 {
		t.Fatalf("folder mode %v, want 0700", st.Mode().Perm())
	}
	if removed, err := RemoveConfig(path); !removed || err != nil {
		t.Fatalf("removed %v, %v", removed, err)
	}
	if removed, err := RemoveConfig(path); removed || err != nil {
		t.Fatalf("removed twice %v, %v", removed, err)
	}
	if cfg, err := LoadConfig(path); err != nil || cfg.APIKey != "" {
		t.Fatalf("no file is no key: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "wm login") {
		t.Fatalf("err %v", err)
	}
}

func TestConfigPath(t *testing.T) {
	env := map[string]string{"XDG_CONFIG_HOME": "/x/conf", "HOME": "/h"}
	get := func(k string) string { return env[k] }
	if p, _ := ConfigPath(get); p != filepath.Join("/x/conf", "wheelmachine", "config.json") {
		t.Fatalf("got %q", p)
	}
	env["XDG_CONFIG_HOME"] = "relative/conf" // not absolute: ignored, as XDG says
	if p, _ := ConfigPath(get); p != filepath.Join("/h", ".config", "wheelmachine", "config.json") {
		t.Fatalf("got %q", p)
	}
}

func TestNewFromEnv(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"XDG_CONFIG_HOME": dir, EnvBaseURL: "http://127.0.0.1:8080"}
	get := func(k string) string { return env[k] }
	if _, err := NewFromEnv(get); !errors.Is(err, ErrNoKey) {
		t.Fatalf("err %v", err)
	}
	stored := "ak_live_" + strings.Repeat("s", 40)
	if err := SaveConfig(filepath.Join(dir, "wheelmachine", "config.json"), Config{APIKey: stored}); err != nil {
		t.Fatal(err)
	}
	c, err := NewFromEnv(get)
	if err != nil || string(c.key) != stored || c.BaseURL() != "http://127.0.0.1:8080" {
		t.Fatalf("stored key not used: %v", err)
	}
	env[EnvAPIKey] = testKey
	if c, err := NewFromEnv(get); err != nil || string(c.key) != testKey {
		t.Fatalf("the environment's key comes first: %v", err)
	}
	if c, err := NewFromEnv(get, WithKey(stored)); err != nil || string(c.key) != stored {
		t.Fatalf("an option given comes last: %v", err)
	}
	env[EnvBaseURL] = "http://example.com"
	if _, err := NewFromEnv(get); err == nil {
		t.Fatal("a plain-http address elsewhere is refused")
	}
}

// A secret's value goes in the body of the call that sets it and nowhere else: an error, tried again or not, says
// nothing of it, and a name that is not one is refused before anything is called.
func TestASecretsValueIsInTheBodyAlone(t *testing.T) {
	const value = "made-up-value-0c4e"
	var calls atomic.Int32
	var bodies []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		if strings.Contains(r.URL.String(), value) || strings.Contains(fmt.Sprint(r.Header), value) {
			t.Errorf("the value is in the request's line or headers")
		}
		writeProblem(w, 503, "unavailable", "not available just now", "")
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL)
	_, err := c.SetSecret(context.Background(), "API_TOKEN", value)
	var e *Error
	if !errors.As(err, &e) || e.Status != 503 || strings.Contains(err.Error(), value) || strings.Contains(fmt.Sprintf("%+v %#v", err, e), value) {
		t.Fatalf("%v", err)
	}
	// (a secret set again is the same: it is tried again)
	if n := calls.Load(); n != int32(DefaultRetryPolicy.MaxAttempts) {
		t.Fatalf("%d tries", n)
	}
	for _, b := range bodies {
		if b != `{"value":"`+value+`"}` {
			t.Fatalf("sent %q", b)
		}
	}
	before := calls.Load()
	for _, name := range []string{"", "api_token", "1ST", "A/B", "A?B", strings.Repeat("A", 65)} {
		if _, err := c.SetSecret(context.Background(), name, value); !IsCode(err, CodeInvalidRequest) || strings.Contains(err.Error(), value) {
			t.Fatalf("%q: %v", name, err)
		}
		if err := c.DeleteSecret(context.Background(), name); !IsCode(err, CodeInvalidRequest) {
			t.Fatalf("%q: %v", name, err)
		}
	}
	if calls.Load() != before {
		t.Fatal("a name that is not one was sent")
	}
}

// A program signing in through the browser sends no key, even one it was given, keeps the device code out of anything
// printed, asks at the pace the API says - slower when asked to slow down - and is given the key once.
func TestADeviceLoginSendsNoKeyAndWaitsForThePerson(t *testing.T) {
	var asks atomic.Int32
	var keys []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, r.Header.Get("Authorization"))
		mu.Unlock()
		switch r.URL.Path {
		case "/v1/login/devices":
			_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "wmd_secret-device-code", "user_code": "BCDF-GHJK",
				"verification_uri": "https://app.example/device", "expires_in": 600, "interval": 5})
		case "/v1/login/devices/token":
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in["device_code"] != "wmd_secret-device-code" {
				writeProblem(w, 404, "not_found", "no such sign-in", "")
				return
			}
			switch asks.Add(1) {
			case 1:
				w.WriteHeader(202)
				_, _ = w.Write([]byte(`{"object":"device_login","state":"pending","interval":5}`))
			case 2:
				writeProblem(w, 429, "rate_limited", "ask once every 5 seconds", "")
			case 3:
				w.WriteHeader(201)
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "key_0000000000000001", "object": "key", "name": "wm on box",
					"scopes": []string{ScopeAll}, "secret": testKey})
			default:
				writeProblem(w, 410, CodeLoginExpired, "this sign-in was used already", "")
			}
		}
	}))
	defer srv.Close()
	w := &waits{}
	d, err := StartDeviceLogin(context.Background(), DeviceLoginRequest{Name: "wm on box"}, WithBaseURL(srv.URL), WithKey(testKey),
		WithRetryPolicy(RetryPolicy{MaxAttempts: 1}))
	if err != nil {
		t.Fatal(err)
	}
	d.c.sleep = w.sleep
	if printed := fmt.Sprintf("%v %+v %#v", d, d, *d); strings.Contains(printed, "secret-device-code") {
		t.Fatalf("the device code is printed: %s", printed)
	}
	if d.UserCode != "BCDF-GHJK" || d.Interval != 5*time.Second {
		t.Fatalf("%+v", d)
	}
	k, err := d.Wait(context.Background())
	if err != nil || k.Secret != testKey || k.Name != "wm on box" {
		t.Fatalf("%v %v", k, err)
	}
	if got := w.all(); len(got) != 3 || got[0] != 5*time.Second || got[1] != 5*time.Second || got[2] != 10*time.Second {
		t.Fatalf("waited %v: the pace said, then slower when asked", got)
	}
	for _, h := range keys {
		if h != "" {
			t.Fatalf("a key was sent: %q", h)
		}
	}
	if _, err := d.Poll(context.Background()); !IsCode(err, CodeLoginExpired) {
		t.Fatalf("asked again: %v", err)
	}
}

// Turning an app on and making a link into one answer what is shown once - a link, an app's own token - so neither is
// asked for again once it may have reached the API; turning one off is the same however often it is sent, so it is.
// What is not an app's name is refused before any call.
func TestAnAppsTurningOnAndLinkAreNeverAskedForTwiceButTurningOffIs(t *testing.T) {
	var n atomic.Int32
	var fail atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if n.Add(1) == 1 {
			switch fail.Load() {
			case "hang up":
				hangUp(w)
			case "503":
				writeProblem(w, 503, CodeUnavailable, "the machine could not be reached just now", "1")
			case "429":
				writeProblem(w, 429, CodeRateLimited, "this key's limit of 600 calls a minute is reached", "1")
			case "no apps":
				writeProblem(w, 409, CodeNoApps, "this machine has no apps; make a new machine to have them", "")
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"app","machine":"m_00000000000000000000000001","name":"desktop","title":"Desktop","kind":"web",`+
			`"on":true,"running":true,"port":6901,"path":"/","url":"https://6901-00000000000000000000000001.run.wheelmachine.ai","command":null,`+
			`"link":"https://6901-00000000000000000000000001.run.wheelmachine.ai/.wheelmachine/link/x","expires_at":"2026-10-23T12:05:00Z","access_s":1800}`)
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL)
	ctx := context.Background()
	id := "m_00000000000000000000000001"
	for _, how := range []string{"503", "hang up"} {
		for what, call := range map[string]func() error{
			"on":   func() error { return second(c.TurnAppOn(ctx, id, "desktop", AppLinkCreate{})) },
			"link": func() error { return second(c.LinkApp(ctx, id, "desktop", AppLinkCreate{AccessS: 600})) },
		} {
			n.Store(0)
			fail.Store(how)
			if err := call(); err == nil || n.Load() != 1 {
				t.Fatalf("%s, %s: what may have been made is never asked for again: err %v after %d tries", how, what, err, n.Load())
			}
		}
		n.Store(0)
		if p, err := c.TurnAppOff(ctx, id, "desktop"); err != nil || p.Name != "desktop" || n.Load() != 2 {
			t.Fatalf("%s: turning off is safe to send again: err %v after %d tries", how, err, n.Load())
		}
	}
	n.Store(0)
	fail.Store("429")
	if p, err := c.TurnAppOn(ctx, id, "desktop", AppLinkCreate{}); err != nil || p.Link == "" || p.ExpiresAt == nil || *p.Port != 6901 || n.Load() != 2 {
		t.Fatalf("refused before anything was done, it is asked again: err %v after %d tries", err, n.Load())
	}
	n.Store(0)
	fail.Store("no apps")
	if _, err := c.ListApps(ctx, id); !IsCode(err, CodeNoApps) || StatusOf(err) != 409 || n.Load() != 1 {
		t.Fatalf("an old machine: %v after %d tries", err, n.Load())
	}
	for _, bad := range []string{"", "Desktop", "../keys", "desk top", strings.Repeat("a", 33)} {
		var e *Error
		if err := second(c.TurnAppOn(ctx, id, bad, AppLinkCreate{})); !errors.As(err, &e) || e.Code != CodeInvalidRequest || e.Status != 0 {
			t.Fatalf("%q: %v; refused here, never sent", bad, err)
		}
	}
}

// The organisations and the audit are asked for as the API takes them: the narrowing in the query, times in RFC 3339 in
// UTC, pages walked by their next. An export is the file as it streams, in the format asked; a format that is not one,
// and an account's id that is not one, are refused before any call. An organisation's key, which lists no
// organisations, is refused in the API's own words.
func TestOrgsAndTheAuditAreAskedAsTheAPITakesThem(t *testing.T) {
	var mu sync.Mutex
	var asked []*http.Request
	var orgCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.Clone(context.Background()))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/orgs":
			if orgCalls.Add(1) > 1 {
				writeProblem(w, 403, CodeForbidden, "an organisation's key does not list the other organisations of the person who made it", "")
				return
			}
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"acc_01jb2xk3n4p5q6r7s8t9v0w1x2","object":"org","name":"Acme","role":"admin",`+
				`"primary_owner":false,"created_at":"2026-10-01T00:00:00Z","people":4}]}`)
		case "/v1/audit":
			if r.URL.Query().Get("after") == "" {
				_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"3","object":"audit_event","at":"2026-10-23T11:00:00.25Z","action":"member.role",`+
					`"target":"acc_bob","person":{"id":"acc_ada","name":"Ada"},"by":"ses_1","address":"203.0.113.7","request_id":"req_1","detail":{"role":"admin"}},`+
					`{"id":"2","object":"audit_event","at":"2026-10-23T10:00:00Z","action":"machine.pause","target":"m_1","person":null,"by":"system",`+
					`"address":"","request_id":"","detail":{}}],"next":"2"}`)
				return
			}
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"1","object":"audit_event","at":"2026-10-22T10:00:00Z","action":"machine.create",`+
				`"target":"m_1","person":{"id":"acc_ada","name":"Ada"},"by":"key_00000000000000a1","address":"203.0.113.7","request_id":"req_0","detail":{}}],"next":null}`)
		case "/v1/audit/export":
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
			_, _ = io.WriteString(w, "\"id\",\"at\"\r\n\"1\",\"2026-10-22T10:00:00Z\"\r\n")
		}
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL)
	ctx := context.Background()

	orgs, err := c.Orgs(ctx)
	if err != nil || len(orgs) != 1 || orgs[0].Name != "Acme" || orgs[0].Role != RoleAdmin || orgs[0].People != 4 || orgs[0].PrimaryOwner {
		t.Fatalf("orgs %+v %v", orgs, err)
	}
	if _, err := c.Orgs(ctx); !IsCode(err, CodeForbidden) || StatusOf(err) != 403 || !strings.Contains(err.Error(), "does not list the other organisations") {
		t.Fatalf("an organisation's key: %v", err)
	}

	// three hours ahead of UTC: said in UTC, to the second and below it
	east := time.FixedZone("east", 3*60*60)
	since, until := time.Date(2026, 10, 1, 3, 0, 0, 0, east), time.Date(2026, 10, 23, 12, 0, 0, 500, time.UTC)
	q := AuditQuery{Person: "acc_ada", Action: "member.", Since: &since, Until: &until, Limit: 2}
	var ids []string
	for e, err := range c.AllAudit(ctx, q) {
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, e.ID)
	}
	if strings.Join(ids, " ") != "3 2 1" {
		t.Fatalf("walked %v", ids)
	}
	mu.Lock()
	first, second := asked[2].URL.Query(), asked[3].URL.Query()
	mu.Unlock()
	if first.Get("person") != "acc_ada" || first.Get("action") != "member." || first.Get("since") != "2026-10-01T00:00:00Z" ||
		first.Get("until") != "2026-10-23T12:00:00.0000005Z" || first.Get("limit") != "2" || first.Get("after") != "" || second.Get("after") != "2" {
		t.Fatalf("asked %v then %v", first, second)
	}
	page, err := c.Audit(ctx, AuditQuery{})
	if err != nil || page.After() != "2" || len(page.Data) != 2 || page.Data[0].Person.Name != "Ada" || page.Data[1].Person != nil ||
		page.Data[1].By != "system" || page.Data[0].Detail["role"] != "admin" || !page.Data[0].At.Equal(time.Date(2026, 10, 23, 11, 0, 0, 250e6, time.UTC)) {
		t.Fatalf("page %+v %v", page, err)
	}

	body, err := c.ExportAudit(ctx, AuditQuery{Action: "machine."}, AuditCSV)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(body)
	_ = body.Close()
	mu.Lock()
	export := asked[len(asked)-1]
	mu.Unlock()
	if err != nil || string(got) != "\"id\",\"at\"\r\n\"1\",\"2026-10-22T10:00:00Z\"\r\n" || export.URL.Query().Get("format") != "csv" ||
		export.URL.Query().Get("action") != "machine." || export.Header.Get("Accept") != "text/csv" {
		t.Fatalf("export %q %v, asked %v %v", got, err, export.URL.Query(), export.Header)
	}

	mu.Lock()
	before := len(asked)
	mu.Unlock()
	var e *Error
	if _, err := c.ExportAudit(ctx, AuditQuery{}, "xml"); !errors.As(err, &e) || e.Code != CodeInvalidRequest || e.Status != 0 {
		t.Fatalf("a format that is not one: %v", err)
	}
	for _, bad := range []string{"Acme", "acc_123", "acc_01JB2XK3N4P5Q6R7S8T9V0W1X2", "prj_01jb2xk3n4p5q6r7s8t9v0w1x2"} {
		if _, err := StartDeviceLogin(ctx, DeviceLoginRequest{Account: bad}, WithBaseURL(srv.URL)); !errors.As(err, &e) || e.Code != CodeInvalidRequest {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	mu.Lock()
	after := len(asked)
	mu.Unlock()
	if after != before {
		t.Fatal("what is not right is refused before any call")
	}
	if !IsAccountID("acc_01jb2xk3n4p5q6r7s8t9v0w1x2") || IsAccountID("acc_01jb2xk3n4p5q6r7s8t9v0w1x") {
		t.Fatal("an account's id is acc_ and 26 letters and digits")
	}
}

// A fork is answered with the fork, or - a member's, in an organisation - with the request that waits for an OK (202):
// told apart by the answer's object and read whole either way, each asked with an Idempotency-Key. An answer that cannot
// be read as the one it says it is, is an error that names the call.
func TestAForkIsAnsweredWithTheMachineOrWithTheRequestThatWaits(t *testing.T) {
	var answer atomic.Value
	answer.Store("machine")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/machines/m_00000000000000000000000001/fork" || r.Header.Get("Idempotency-Key") == "" {
			writeProblem(w, 400, CodeInvalidRequest, "not the fork's call", "")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "req_fork")
		switch answer.Load() {
		case "machine":
			machineJSON(w, 201, "m_00000000000000000000000002")
			return
		case "request":
			w.Header().Set("Location", "/v1/requests/rq_01jb2xk3n4p5q6r7s8t9v0w1x2")
			w.WriteHeader(202)
			_, _ = io.WriteString(w, `{"id":"rq_01jb2xk3n4p5q6r7s8t9v0w1x2","object":"request","kind":"fork","way":"give",`+
				`"machine":{"id":"m_00000000000000000000000001","name":"web"},"holder":{"id":"acc_ada","name":"Ada"},`+
				`"receiver":{"id":"acc_ada","name":"Ada"},"asked_by":{"id":"acc_ada","name":"Ada"},"from_team":"prj_1","to_team":null,`+
				`"gpu":false,"why":"","state":"waiting","waiting_on":{"who":"manager","name":null},"decided_by":null,"decision_why":"",`+
				`"result":null,"failure":null,"restarted":false,"keys_revoked":0,"created_at":"2026-11-01T12:00:00Z",`+
				`"expires_at":"2026-11-02T12:00:00Z","done_at":null}`)
		default:
			w.WriteHeader(202)
			_, _ = io.WriteString(w, `{"id":7,"object":"request"}`)
		}
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL)
	ctx := context.Background()
	made, err := c.ForkMachine(ctx, "m_00000000000000000000000001", MachineFork{Name: Ptr("web-2")})
	if err != nil || made.Request != nil || made.Machine == nil || made.Machine.ID != "m_00000000000000000000000002" || made.Machine.State != StateRunning {
		t.Fatalf("a fork made at once: %+v %v", made, err)
	}
	answer.Store("request")
	asked, err := c.ForkMachine(ctx, "m_00000000000000000000000001", MachineFork{})
	if err != nil || asked.Machine != nil || asked.Request == nil {
		t.Fatalf("a fork that waits: %+v %v", asked, err)
	}
	if q := asked.Request; q.ID != "rq_01jb2xk3n4p5q6r7s8t9v0w1x2" || q.Kind != RequestFork || q.Holder.ID != q.Receiver.ID || q.Machine == nil ||
		q.Machine.Name != "web" || q.State != "waiting" || q.StateWords() != "Waiting for a full manager" || q.Result != nil {
		t.Fatalf("the request: %+v", q)
	}
	answer.Store("broken")
	if _, err := c.ForkMachine(ctx, "m_00000000000000000000000001", MachineFork{}); err == nil || !strings.Contains(err.Error(), "(request req_fork)") {
		t.Fatalf("an answer that cannot be read: %v", err)
	}
}

// A backup is made with an Idempotency-Key and its note, and listed - a machine's, or every one - at the most asked or
// the API's own; a fork from one says which and at what size, and a member's request for it says them back.
func TestBackupsAndForksFromThemAreAskedAsTheAPITakesThem(t *testing.T) {
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		asked = append(asked, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		mu.Unlock()
		backup := `{"id":"20261102T093500Z-3f2a0001","object":"backup","machine":"m_00000000000000000000000001","note":"before",` +
			`"created_at":"2026-11-02T09:35:00Z"}`
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.Header.Get("Idempotency-Key") == "":
			writeProblem(w, 400, CodeInvalidRequest, "made without an Idempotency-Key", "")
		case r.URL.Path == "/v1/machines/m_00000000000000000000000001/fork":
			w.WriteHeader(202)
			_, _ = io.WriteString(w, `{"id":"rq_01jb2xk3n4p5q6r7s8t9v0w1x2","object":"request","kind":"fork","way":"give",`+
				`"machine":{"id":"m_00000000000000000000000001","name":"web"},"holder":{"id":"acc_ada","name":"Ada"},`+
				`"receiver":{"id":"acc_ada","name":"Ada"},"asked_by":{"id":"acc_ada","name":"Ada"},"state":"waiting",`+
				`"waiting_on":{"who":"asker","name":"Ada"},"result":null,"backup":"20261102T093500Z-3f2a0001","cpus":8,`+
				`"created_at":"2026-11-02T12:00:00Z","expires_at":"2026-11-03T12:00:00Z"}`)
		case r.Method == http.MethodPost:
			w.WriteHeader(201)
			_, _ = io.WriteString(w, backup)
		default:
			_, _ = io.WriteString(w, `{"object":"list","data":[`+backup+`]}`)
		}
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL)
	ctx := context.Background()
	b, err := c.BackupMachine(ctx, "m_00000000000000000000000001", BackupCreate{Note: "before"})
	if err != nil || b.ID != "20261102T093500Z-3f2a0001" || b.Object != "backup" || b.Note != "before" ||
		!b.CreatedAt.Equal(time.Date(2026, 11, 2, 9, 35, 0, 0, time.UTC)) {
		t.Fatalf("a backup: %+v %v", b, err)
	}
	if list, err := c.ListMachineBackups(ctx, "m_00000000000000000000000001", 0); err != nil || len(list.Data) != 1 || list.Data[0].ID != b.ID {
		t.Fatalf("a machine's backups: %+v %v", list, err)
	}
	if list, err := c.ListBackups(ctx, 200); err != nil || list.Object != "list" || len(list.Data) != 1 {
		t.Fatalf("every backup: %+v %v", list, err)
	}
	f, err := c.ForkMachine(ctx, "m_00000000000000000000000001", MachineFork{Backup: "latest", CPUs: 8})
	if err != nil || f.Request == nil || f.Request.Backup == nil || *f.Request.Backup != b.ID || f.Request.CPUs == nil || *f.Request.CPUs != 8 {
		t.Fatalf("a member's fork from a backup: %+v %v", f, err)
	}
	if _, err := c.ListMachineBackups(ctx, "../backups", 0); !IsCode(err, CodeInvalidRequest) {
		t.Fatalf("an id that is not one: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{
		`POST /v1/machines/m_00000000000000000000000001/backups {"note":"before"}`,
		"GET /v1/machines/m_00000000000000000000000001/backups ",
		"GET /v1/backups?limit=200 ",
		`POST /v1/machines/m_00000000000000000000000001/fork {"backup":"latest","cpus":8}`,
	}
	if strings.Join(asked, "\n") != strings.Join(want, "\n") {
		t.Fatalf("asked:\n%s", strings.Join(asked, "\n"))
	}
}
