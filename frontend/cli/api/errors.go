package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The stable codes an error carries in its `code`.
const (
	CodeInvalidRequest  = "invalid_request"
	CodeUnauthorized    = "unauthorized"
	CodeKeyRevoked      = "key_revoked"
	CodeKeyExpired      = "key_expired"
	CodeForbidden       = "forbidden"
	CodeNotFound        = "not_found"
	CodeConflict        = "conflict"
	CodeProjectNotEmpty = "project_not_empty"
	CodePaymentRequired = "payment_required"
	CodeRateLimited     = "rate_limited"
	CodeLimitReached    = "limit_reached"
	CodeUnavailable     = "unavailable"
	CodeTimeout         = "timeout"
	CodeTooLarge        = "too_large"
	CodeNotOpen         = "not_open"
	CodeNoApps          = "no_apps" // a machine with no apps
	CodeInternal        = "internal"
	CodeLoginDenied     = "login_denied"  // the person refused a sign-in through the browser
	CodeLoginExpired    = "login_expired" // a sign-in's code ran out, or was used
	// a risky step needs a code we e-mail the person (SendConfirmCode), and its error says for what (Error.Confirm)
	CodeConfirmRequired = "confirm_required"
	CodeConfirmWrong    = "confirm_wrong" // the code it carried is not right
)

// Error is an error from the API: an application/problem+json answer (RFC 9457) with a stable Code, or an answer
// of another kind, kept by its status alone. Its text never holds the API key.
type Error struct {
	Status     int           // the HTTP status, such as 404
	Code       string        // the stable code, such as "not_found"; empty when the answer was not a problem
	Title      string        // the code's title
	Detail     string        // what went wrong, in words a person reads
	Type       string        // a link to the page that explains the code
	RequestID  string        // this call's id: give it when you ask about a call
	RetryAfter time.Duration // how long the API asked to wait before trying again, when it said
	Limit      *LimitHit     // the limit that stopped the call, where one did: which, who can raise it, when it lifts
	Confirm    *ConfirmAsk   // what the code a risky step needs must be for, where it needs one (confirm_required)

	retrySet bool // the API said how long to wait (Retry-After, or retry_after_s)
}

func (e *Error) Error() string {
	msg := e.Detail
	if msg == "" {
		msg = e.Title
	}
	if msg == "" {
		msg = strings.ToLower(http.StatusText(e.Status))
	}
	if msg == "" {
		msg = "the API refused the call"
	}
	var said []string
	if e.Code != "" {
		said = append(said, e.Code)
	}
	if e.Status != 0 {
		said = append(said, strconv.Itoa(e.Status))
	}
	if e.RequestID != "" {
		said = append(said, "request "+e.RequestID)
	}
	if len(said) == 0 {
		return msg
	}
	return msg + " (" + strings.Join(said, ", ") + ")"
}

// HasRetryAfter reports whether the API said how long to wait before trying again.
func (e *Error) HasRetryAfter() bool { return e.retrySet }

// IsCode reports whether err is an error from the API with this code.
func IsCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// StatusOf is the HTTP status of an error from the API, or 0 for any other error.
func StatusOf(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

// ConnError is a call that got no answer: the API could not be reached, the connection broke, or no answer came in
// the time the call is given.
type ConnError struct {
	Host string // where the API was being reached
	Err  error  // why
}

func (e *ConnError) Error() string {
	return "could not reach the API at " + e.Host + ": " + e.Err.Error()
}

func (e *ConnError) Unwrap() error { return e.Err }

// connError words a failed exchange without the request's own text (its URL is in the host already).
func connError(host string, err error, timeout time.Duration) *ConnError {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if timeout > 0 && errors.Is(err, errAttemptTimeout) {
		err = fmt.Errorf("no answer within %s", timeout)
	}
	return &ConnError{Host: host, Err: err}
}

// The body of a problem+json answer.
type problem struct {
	Type        string      `json:"type"`
	Title       string      `json:"title"`
	Detail      string      `json:"detail"`
	Code        string      `json:"code"`
	RequestID   string      `json:"request_id"`
	RetryAfterS float64     `json:"retry_after_s"`
	Limit       *LimitHit   `json:"limit"`
	Confirm     *ConfirmAsk `json:"confirm"`
}

// errorFrom reads an error. A body that is not a problem (a proxy's page, say) is never shown: only its status is.
func errorFrom(res *http.Response, now time.Time) *Error {
	e := &Error{Status: res.StatusCode, RequestID: res.Header.Get("X-Request-Id")}
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	var p problem
	if json.Unmarshal(raw, &p) == nil && (p.Code != "" || p.Detail != "") {
		e.Code, e.Title, e.Detail, e.Type, e.Limit, e.Confirm = p.Code, p.Title, p.Detail, p.Type, p.Limit, p.Confirm
		if p.RequestID != "" {
			e.RequestID = p.RequestID
		}
		if p.RetryAfterS > 0 && !math.IsInf(p.RetryAfterS, 0) {
			e.RetryAfter, e.retrySet = time.Duration(math.Min(p.RetryAfterS, 1e6)*float64(time.Second)), true
		}
	}
	if d, ok := parseRetryAfter(res.Header.Get("Retry-After"), now); ok {
		e.RetryAfter, e.retrySet = d, true
	}
	return e
}

// parseRetryAfter reads Retry-After: seconds, or an HTTP date.
func parseRetryAfter(v string, now time.Time) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		if n < 0 {
			return 0, false
		}
		return time.Duration(min(n, 1e6)) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(t.Sub(now), 0), true
	}
	return 0, false
}
