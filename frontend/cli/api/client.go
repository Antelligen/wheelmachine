// Package api is a client for the Wheelmachine API (contracts/openapi.yaml): cloud machines for people and agents, paid
// by the second.
//
// A Client tries again what is safe to try again: temporary errors (429), nothing available or no answer just now (503,
// 504) and connections that failed. It waits as Retry-After says, else backs off exponentially with full jitter. A
// call that makes something carries an Idempotency-Key made once for it and sent again on every try, so a retry
// never makes two. A command run in a machine is tried again only when it cannot have run, and a link to a port
// only when it cannot have been made.
//
// The API key goes only into the Authorization header. It is never part of an error, and a Client or Config
// printed or logged shows nothing of it.
package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	mrand "math/rand/v2"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

// Version is wm's, wm-mcp's and this module's version, sent in the User-Agent and shown by `wm --version`: the one
// place it is kept. A release build stamps the same into the programs (-ldflags -X, release.py), from a tag that must
// say it too.
var Version = "0.1.4"

const (
	// DefaultBaseURL is where the API is reached when WHEELMACHINE_BASE_URL says nothing else.
	DefaultBaseURL = "https://api.wheelmachine.ai"
	// EnvBaseURL names the environment variable that holds the API's address.
	EnvBaseURL = "WHEELMACHINE_BASE_URL"
	// EnvAPIKey names the environment variable that holds the API key.
	EnvAPIKey = "WHEELMACHINE_API_KEY"
)

// ErrNoKey is returned when no API key is given: none in the environment and none stored by `wm login`.
var ErrNoKey = errors.New("no API key: run `wm login`, or set " + EnvAPIKey)

// RetryPolicy says how a Client tries again.
type RetryPolicy struct {
	// MaxAttempts counts every try, the first among them: 1 never tries again.
	MaxAttempts int
	// BaseDelay is the ceiling of the first backoff; each one after doubles it, up to MaxDelay. The wait itself is
	// drawn evenly between nothing and the ceiling (full jitter), and is never shorter than a Retry-After.
	BaseDelay time.Duration
	MaxDelay  time.Duration
	// MaxRetryAfter is the longest Retry-After waited for; an error asking for longer is returned as it is.
	MaxRetryAfter time.Duration
}

// DefaultRetryPolicy is five tries in all, backoffs from half a second up to 20 seconds, and a Retry-After of up to
// two minutes waited for.
var DefaultRetryPolicy = RetryPolicy{MaxAttempts: 5, BaseDelay: 500 * time.Millisecond, MaxDelay: 20 * time.Second,
	MaxRetryAfter: 2 * time.Minute}

// RetryEvent is given to a retry hook before each wait.
type RetryEvent struct {
	Method  string
	Path    string
	Attempt int           // the try that failed, from 1
	Wait    time.Duration // how long until the next
	Err     error         // why it failed
}

// Client calls the API. It is safe for use by several goroutines at once.
type Client struct {
	base      *url.URL
	key       secret
	http      *http.Client
	userAgent string
	retry     RetryPolicy
	onRetry   func(RetryEvent)
	now       func() time.Time
	sleep     func(context.Context, time.Duration) error
	jitter    func(ceiling time.Duration) time.Duration
	newKey    func() string
}

// Format prints a Client as where it calls, and nothing of its key, whatever the verb.
func (c Client) Format(f fmt.State, _ rune) {
	where := "?"
	if c.base != nil {
		where = c.base.String()
	}
	_, _ = io.WriteString(f, "api.Client("+where+")")
}

// secret is the API key as a Client holds it: printed, formatted, encoded or logged, it shows nothing.
type secret string

func (secret) Format(f fmt.State, _ rune)   { _, _ = io.WriteString(f, "[hidden]") }
func (secret) MarshalJSON() ([]byte, error) { return []byte(`"[hidden]"`), nil }
func (secret) LogValue() slog.Value         { return slog.StringValue("[hidden]") }

// Option sets up a Client.
type Option func(*Client) error

// WithBaseURL sets where the API is reached. It must be https, except on this computer (http://127.0.0.1:8080).
func WithBaseURL(raw string) Option {
	return func(c *Client) error {
		u, err := parseBase(raw)
		if err != nil {
			return err
		}
		c.base = u
		return nil
	}
}

// WithKey sets the API key.
func WithKey(key string) Option {
	return func(c *Client) error {
		key = strings.TrimSpace(key)
		for _, r := range key {
			if r <= ' ' || r > '~' {
				return errors.New("the API key holds characters a key never has: copy it again")
			}
		}
		c.key = secret(key)
		return nil
	}
}

// WithHTTPClient makes the calls with h rather than a client of the Client's own, which follows no redirect.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) error {
		if h == nil {
			return errors.New("WithHTTPClient: no client given")
		}
		c.http = h
		return nil
	}
}

// WithRetryPolicy sets how calls are tried again.
func WithRetryPolicy(p RetryPolicy) Option {
	return func(c *Client) error {
		if p.MaxAttempts < 1 {
			p.MaxAttempts = 1
		}
		c.retry = p
		return nil
	}
}

// WithUserAgent names the program making the calls, such as "wm/0.1.2"; the module's own token follows it.
func WithUserAgent(product string) Option {
	return func(c *Client) error {
		if product = strings.TrimSpace(product); product != "" {
			c.userAgent = product + " " + defaultUserAgent()
		}
		return nil
	}
}

// WithRetryHook calls fn before each wait for another try, to say what is happening.
func WithRetryHook(fn func(RetryEvent)) Option {
	return func(c *Client) error {
		c.onRetry = fn
		return nil
	}
}

func defaultUserAgent() string {
	return "wheelmachine-go/" + Version + " (" + runtime.GOOS + "; " + runtime.GOARCH + ")"
}

// New makes a Client. A key is needed (WithKey); the address is DefaultBaseURL unless WithBaseURL says otherwise.
func New(opts ...Option) (*Client, error) {
	c, err := build(opts...)
	if err != nil {
		return nil, err
	}
	if c.key == "" {
		return nil, ErrNoKey
	}
	return c, nil
}

// build makes a Client with or without a key: without one it makes only the calls of signing in (login.go).
func build(opts ...Option) (*Client, error) {
	c := &Client{retry: DefaultRetryPolicy, userAgent: defaultUserAgent(), now: time.Now, sleep: sleepContext,
		jitter: fullJitter, newKey: newIdempotencyKey}
	base, err := parseBase(DefaultBaseURL)
	if err != nil {
		return nil, err
	}
	c.base = base
	for _, o := range opts {
		if err := o(c); err != nil {
			return nil, err
		}
	}
	if c.http == nil {
		c.http = defaultHTTPClient()
	}
	return c, nil
}

// BaseURL is the API's address from the environment: WHEELMACHINE_BASE_URL, else DefaultBaseURL.
func BaseURL(getenv func(string) string) string {
	if v := strings.TrimSpace(getenv(EnvBaseURL)); v != "" {
		return v
	}
	return DefaultBaseURL
}

// NewFromEnv makes a Client from the environment: the address from WHEELMACHINE_BASE_URL, the key from
// WHEELMACHINE_API_KEY or else the one `wm login` stored. Options given are applied after, so they can change
// either. getenv is os.Getenv outside tests.
func NewFromEnv(getenv func(string) string, opts ...Option) (*Client, error) {
	key := strings.TrimSpace(getenv(EnvAPIKey))
	if key == "" {
		path, err := ConfigPath(getenv)
		if err == nil {
			cfg, err := LoadConfig(path)
			if err != nil {
				return nil, err
			}
			key = cfg.APIKey
		}
	}
	all := []Option{WithBaseURL(BaseURL(getenv))}
	if key != "" {
		all = append(all, WithKey(key))
	}
	return New(append(all, opts...)...)
}

// BaseURL is where this Client calls.
func (c *Client) BaseURL() string { return c.base.String() }

func parseBase(raw string) (*url.URL, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("the API's address %q is not one: give it as https://host", raw)
	}
	if u.Scheme == "http" && !loopback(u.Hostname()) {
		return nil, fmt.Errorf("the API's address %q must begin https:// (plain http is for this computer only, such as http://127.0.0.1:8080)", raw)
	}
	return u, nil
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func defaultHTTPClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConnsPerHost = 16
	return &http.Client{
		Transport: t,
		// the API never sends anyone elsewhere: a redirect is an answer to show, not one to follow with the key
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// fullJitter is a wait drawn evenly between nothing and the ceiling.
func fullJitter(ceiling time.Duration) time.Duration {
	if ceiling <= 0 {
		return 0
	}
	return time.Duration(mrand.Int64N(int64(ceiling) + 1))
}

// newIdempotencyKey is 130 random bits, as printable as a header wants.
func newIdempotencyKey() string { return "wm_" + rand.Text() }

// A call to the API, as do makes it.
type call struct {
	method, path string
	query        url.Values
	in           any       // a JSON body
	stream       io.Reader // or a file's bytes, `size` of them
	size         int64
	create       bool // makes something: carries an Idempotency-Key, the same on every try
	idemKey      string
	replay       bool          // safe to send again even when the first may have reached the API
	once         bool          // never sent again once it may have reached the API, not even when refused for a moment
	timeout      time.Duration // for each try; 0: the caller's context alone
	accept       string
	confirm      string // the confirm code a risky step carries (X-Confirm-Code), when it needs one
}

// Whether the call may be sent again after it may have reached the API: reading, writing a whole file, deleting,
// moving a machine to a state, opening a port, and anything carrying an Idempotency-Key are; running a command and
// making a link to a port are not.
func (cl *call) replayable() bool {
	switch cl.method {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete:
		return true
	}
	return cl.idemKey != "" || cl.replay
}

// errAttemptTimeout ends a try that ran past its own time.
var errAttemptTimeout = errors.New("no answer in time")

// do makes the call, trying again as the policy allows, and gives the first answer of 2xx; the caller closes its
// body. Anything else is an *Error, a *ConnError, or the context's own error when the caller gave up.
func (c *Client) do(ctx context.Context, cl call) (*http.Response, error) {
	var payload []byte
	if cl.in != nil {
		b, err := json.Marshal(cl.in)
		if err != nil {
			return nil, fmt.Errorf("the request could not be made: %w", err)
		}
		payload = b
	}
	if cl.create && cl.idemKey == "" {
		cl.idemKey = c.newKey()
	}
	// a file is sent again only when it can be wound back to where it began
	rewindable, rewind := true, func() error { return nil }
	if cl.stream != nil {
		rewindable = false
		if s, ok := cl.stream.(io.Seeker); ok {
			if at, err := s.Seek(0, io.SeekCurrent); err == nil {
				rewindable = true
				rewind = func() error {
					_, err := s.Seek(at, io.SeekStart)
					return err
				}
			}
		}
	}
	for attempt := 1; ; attempt++ {
		if attempt > 1 {
			if err := rewind(); err != nil {
				return nil, fmt.Errorf("the file could not be read again for another try: %w", err)
			}
		}
		res, sent, err := c.once(ctx, &cl, payload)
		if err == nil && res.StatusCode >= 200 && res.StatusCode < 300 {
			return res, nil
		}
		var again bool
		var refusal *Error
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// nothing reached the API, or what did may safely come again
			again = !sent || (rewindable && cl.replayable())
			err = connError(c.base.Host, err, cl.timeout)
		} else {
			refusal = errorFrom(res, c.now())
			_ = res.Body.Close()
			err = refusal
			again = rewindable && c.retryable(&cl, refusal)
		}
		if !again || attempt >= c.retry.MaxAttempts {
			return nil, err
		}
		wait := c.backoff(attempt)
		if refusal != nil && refusal.retrySet {
			if refusal.RetryAfter > c.retry.MaxRetryAfter {
				return nil, err
			}
			wait = max(wait, refusal.RetryAfter)
		}
		if c.onRetry != nil {
			c.onRetry(RetryEvent{Method: cl.method, Path: cl.path, Attempt: attempt, Wait: wait, Err: err})
		}
		if err := c.sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
}

// Which errors are temporary: a limit of calls a minute; nothing available or no answer just now, for a call that may come
// again; and, for a call with an Idempotency-Key, the first one with it still running. Something not open yet
// (not_open) never does, whatever the call and its status, and nor does a call that is only ever sent once.
func (c *Client) retryable(cl *call, e *Error) bool {
	if cl.once || e.Code == CodeNotOpen {
		return false
	}
	switch e.Status {
	case http.StatusTooManyRequests:
		// refused before anything was done; but a limit on how much an account holds does not pass by waiting
		return e.Code != CodeLimitReached || e.retrySet
	case http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return cl.replayable()
	case http.StatusConflict:
		return cl.idemKey != "" && e.retrySet
	}
	return false
}

// The ceiling doubles from BaseDelay up to MaxDelay; the wait is drawn under it.
func (c *Client) backoff(attempt int) time.Duration {
	ceiling := c.retry.MaxDelay
	if shift := attempt - 1; shift < 30 {
		if d := c.retry.BaseDelay << shift; d > 0 && d < ceiling {
			ceiling = d
		}
	}
	return c.jitter(ceiling)
}

// once is one try. `sent` says whether the request's head was written, after which it may have reached the API.
func (c *Client) once(ctx context.Context, cl *call, payload []byte) (res *http.Response, sent bool, err error) {
	var wrote atomic.Bool
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{WroteHeaders: func() { wrote.Store(true) }})
	actx, cancel := ctx, context.CancelFunc(func() {})
	if cl.timeout > 0 {
		actx, cancel = context.WithTimeoutCause(ctx, cl.timeout, errAttemptTimeout)
	}
	var body io.Reader
	switch {
	case payload != nil:
		body = bytes.NewReader(payload)
	case cl.stream != nil && cl.size > 0:
		// the transport closes a body it is given; the caller's file stays open for another try
		body = io.NopCloser(cl.stream)
	}
	req, err := http.NewRequestWithContext(actx, cl.method, c.urlFor(cl.path, cl.query), body)
	if err != nil {
		cancel()
		return nil, false, err
	}
	if cl.stream != nil {
		req.ContentLength = cl.size
		if cl.size == 0 {
			req.Body = http.NoBody
		}
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+string(c.key))
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", or(cl.accept, "application/json"))
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cl.idemKey != "" {
		req.Header.Set("Idempotency-Key", cl.idemKey)
	}
	if cl.confirm != "" {
		req.Header.Set("X-Confirm-Code", cl.confirm)
	}
	res, err = c.http.Do(req)
	if err != nil {
		if actx.Err() != nil && context.Cause(actx) == errAttemptTimeout && ctx.Err() == nil {
			err = errAttemptTimeout
		}
		cancel()
		return nil, wrote.Load(), err
	}
	res.Body = &cancelOnClose{ReadCloser: res.Body, cancel: cancel}
	return res, true, nil
}

// An answer's body that ends its try's own time when it is closed.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelOnClose) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

func (c *Client) urlFor(path string, q url.Values) string {
	u := *c.base
	u.Path = strings.TrimRight(c.base.Path, "/") + path
	u.RawPath = ""
	u.RawQuery = q.Encode()
	return u.String()
}

// decode makes the call and reads its JSON answer into out (nil: the answer is not needed).
func (c *Client) decode(ctx context.Context, cl call, out any) error {
	res, err := c.do(ctx, cl)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if out == nil || res.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
		return nil
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return fmt.Errorf("the API's answer could not be read (request %s): %w", res.Header.Get("X-Request-Id"), err)
	}
	// an answer of an API from before accounts had currencies says its amounts in dollars alone (currency.go)
	if b, ok := out.(interface{ fromBefore() }); ok {
		b.fromBefore()
	}
	return nil
}

// segment is an id as a part of a route; it never names another route.
func segment(what, id string) (string, error) {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\?#%") ||
		strings.ContainsFunc(id, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return "", &Error{Code: CodeInvalidRequest, Detail: fmt.Sprintf("%q is not a %s id", id, what)}
	}
	return id, nil
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// Ptr is a pointer to v, for the optional fields of a request: api.Ptr(0.0) asks for no wait at all.
func Ptr[T any](v T) *T { return &v }
