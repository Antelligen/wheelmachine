package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Signing a program in through the browser, as `wm login` does: StartDeviceLogin asks the API for a sign-in and gets
// a short code for the person; they open VerificationURI, sign in with GitHub or Google, compare the code and approve;
// Wait then gives the program a new key of their account, once. No key is needed to start, and none is sent.

// DeviceLoginRequest is what a program asks for: what its key will be called ("wm on laptop") and what it may do
// (every scope when none is said). The person may give it less.
type DeviceLoginRequest struct {
	Name   string   `json:"name,omitempty"`
	Scopes []string `json:"scopes,omitempty"`
	// Account is the organisation the program asks its key to be made in, by its id (acc_...); the person's own account
	// when left out. The approval page shows it first, and the person still chooses: the key may be made elsewhere.
	Account string `json:"account,omitempty"`
}

// DeviceLogin is a sign-in on its way. Show the person UserCode and open VerificationURI, where they type it - the
// code is never carried in the address, so a link someone sends cannot approve their own code. The device code that
// fetches the key stays inside, and printing a DeviceLogin shows nothing of it.
type DeviceLogin struct {
	UserCode        string
	VerificationURI string
	ExpiresAt       time.Time
	Interval        time.Duration // how often to ask; Wait keeps to it

	device secret
	c      *Client
}

// Format prints a DeviceLogin as the code the person types, and nothing of the program's own code.
func (d DeviceLogin) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, "api.DeviceLogin("+d.UserCode+")")
}

// ErrLoginPending is what Poll gives while the person has not decided yet.
var ErrLoginPending = errors.New("the sign-in waits for the person to approve it in the browser")

// StartDeviceLogin starts a sign-in through the browser. Options are a Client's; a key given is not used.
func StartDeviceLogin(ctx context.Context, req DeviceLoginRequest, opts ...Option) (*DeviceLogin, error) {
	if req.Account != "" && !IsAccountID(req.Account) {
		return nil, &Error{Code: CodeInvalidRequest, Detail: fmt.Sprintf("%q is not an organisation's id: acc_ and 26 letters and digits", req.Account)}
	}
	c, err := build(opts...)
	if err != nil {
		return nil, err
	}
	c.key = ""
	var out struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
	}
	// a start that may have reached the API is started again: the first one runs out unused
	cl := call{method: http.MethodPost, path: "/v1/login/devices", in: req, replay: true, timeout: callTimeout}
	if err := c.decode(ctx, cl, &out); err != nil {
		return nil, err
	}
	if out.DeviceCode == "" || out.UserCode == "" || out.VerificationURI == "" {
		return nil, errors.New("the API's answer to a sign-in has no code in it")
	}
	return &DeviceLogin{UserCode: out.UserCode, VerificationURI: out.VerificationURI,
		ExpiresAt: c.now().Add(time.Duration(out.ExpiresIn) * time.Second), Interval: time.Duration(max(out.Interval, 1)) * time.Second,
		device: secret(out.DeviceCode), c: c}, nil
}

// Poll asks once whether the person approved: the new key when they did (its secret in this answer only),
// ErrLoginPending while they have not decided, and an *Error with CodeLoginDenied or CodeLoginExpired when it is over.
func (d *DeviceLogin) Poll(ctx context.Context) (*KeyCreated, error) {
	// never sent again once it may have reached the API: the key is given once, and a second ask would find it used
	cl := call{method: http.MethodPost, path: "/v1/login/devices/token", in: map[string]string{"device_code": string(d.device)},
		timeout: callTimeout}
	res, err := d.c.do(ctx, cl)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusAccepted {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
		return nil, ErrLoginPending
	}
	var k KeyCreated
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&k); err != nil || k.Secret == "" {
		return nil, fmt.Errorf("the API's answer with the key could not be read (request %s)", res.Header.Get("X-Request-Id"))
	}
	return &k, nil
}

// Wait asks every Interval until the person approves (the new key), refuses, or the code runs out (an *Error), or
// ctx ends. Asked to slow down, it asks less often; the API not answering for a moment (no key was given then: the
// sign-in stays approved for the next ask) is waited out the same way, until the code runs out.
func (d *DeviceLogin) Wait(ctx context.Context) (*KeyCreated, error) {
	every := d.Interval
	for {
		if err := d.c.sleep(ctx, every); err != nil {
			return nil, err
		}
		k, err := d.Poll(ctx)
		switch {
		case err == nil:
			return k, nil
		case errors.Is(err, ErrLoginPending):
		case IsCode(err, CodeRateLimited):
			every += 5 * time.Second
		case (IsCode(err, CodeUnavailable) || IsCode(err, CodeTimeout)) && d.c.now().Before(d.ExpiresAt):
		default:
			return nil, err
		}
	}
}
