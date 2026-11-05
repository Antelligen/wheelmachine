package api

import (
	"context"
	"io"
	"iter"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// The account's audit: what was done in it, by whom - the person, and the key or sign-in they did it with - to which
// thing, and from where. It is read with a key that holds every scope (cloud:*), and in an organisation by its owners
// and admins alone: anyone else is refused (CodeForbidden).

// The formats the audit is exported in.
const (
	AuditCSV  = "csv"  // every field quoted; one a spreadsheet would run begun with ', so that it is shown, never run
	AuditJSON = "json" // an array of events, the newest first
)

// AuditQuery narrows the audit. Every field may be left out.
type AuditQuery struct {
	Person string     // one person's events, by their id (Person.ID)
	Action string     // the events whose action begins with this, such as "machine." or "member."
	Since  *time.Time // from this time on
	Until  *time.Time // before this time
	Limit  int        // a list's events a page, 1 to 200; 0: the API's 50. Not for an export
	After  string     // where a list's page begins: the Next of the page before. Not for an export
}

func (q AuditQuery) query() url.Values {
	v := url.Values{}
	if q.Person != "" {
		v.Set("person", q.Person)
	}
	if q.Action != "" {
		v.Set("action", q.Action)
	}
	if q.Since != nil {
		v.Set("since", q.Since.UTC().Format(time.RFC3339Nano))
	}
	if q.Until != nil {
		v.Set("until", q.Until.UTC().Format(time.RFC3339Nano))
	}
	return v
}

// Audit is one page of the account's audit, the newest first.
func (c *Client) Audit(ctx context.Context, q AuditQuery) (*Page[AuditEvent], error) {
	v := q.query()
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.After != "" {
		v.Set("after", q.After)
	}
	var page Page[AuditEvent]
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/audit", query: v, timeout: callTimeout}, &page); err != nil {
		return nil, err
	}
	return &page, nil
}

// AllAudit walks the audit the query asks for, the newest first, asking for each page only when the one before it is
// used up. Stop early by breaking out of the loop.
func (c *Client) AllAudit(ctx context.Context, q AuditQuery) iter.Seq2[AuditEvent, error] {
	return all(q.After, func(after string) (*Page[AuditEvent], error) {
		p := q
		p.After = after
		return c.Audit(ctx, p)
	})
}

// ExportAudit is the audit the query asks for as a file, whole, as it comes: CSV (AuditCSV) or JSON (AuditJSON). An
// export covers a year at most - the last one when Since is left out - and 100,000 events at most; asking for more is
// refused (CodeInvalidRequest) in the API's own words. Close it when done.
func (c *Client) ExportAudit(ctx context.Context, q AuditQuery, format string) (io.ReadCloser, error) {
	if format != AuditCSV && format != AuditJSON {
		return nil, &Error{Code: CodeInvalidRequest, Detail: "an export is csv or json, not " + strconv.Quote(format)}
	}
	v := q.query()
	v.Set("format", format)
	accept := "text/csv"
	if format == AuditJSON {
		accept = "application/json"
	}
	// no time of its own for a try: the file streams for as long as it takes, under the caller's context, as a
	// machine's file does
	res, err := c.do(ctx, call{method: http.MethodGet, path: "/v1/audit/export", query: v, accept: accept})
	if err != nil {
		return nil, err
	}
	return res.Body, nil
}
