package main

import (
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
	"unicode"

	"wheelmachine/frontend/cli/api"
)

// table prints aligned columns, a header first.
type table struct{ tw *tabwriter.Writer }

func (a *app) table(header ...string) *table {
	t := &table{tw: tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)}
	t.row(header...)
	return t
}

func (t *table) row(cells ...string) {
	for i, c := range cells {
		if i > 0 {
			_, _ = io.WriteString(t.tw, "\t")
		}
		_, _ = io.WriteString(t.tw, cell(c))
	}
	_, _ = io.WriteString(t.tw, "\n")
}

func (t *table) flush() error { return t.tw.Flush() }

// detail prints a thing's fields, one a line, their values aligned.
type detail struct{ tw *tabwriter.Writer }

func (a *app) detail() *detail { return &detail{tw: tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)} }

func (d *detail) line(name, value string) { fmt.Fprintf(d.tw, "%s:\t%s\n", name, cell(value)) }

func (d *detail) flush() error { return d.tw.Flush() }

// cell is a value as one cell: nothing that breaks a line or a column, and "-" for nothing.
func cell(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func num(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// sizeOf is a machine's size in brief: 2 CPU, 4 GB, and its GPU.
func sizeOf(m api.Machine) string {
	var parts []string
	switch {
	case m.Size != nil:
		parts = append(parts, num(m.Size.CPUs)+" CPU", num(math.Round(m.Size.MemoryGB*10)/10)+" GB")
	case m.CPUs != nil && m.MemoryGB != nil:
		parts = append(parts, strconv.Itoa(*m.CPUs)+" CPU", num(*m.MemoryGB)+" GB")
	}
	if m.GPU != nil {
		parts = append(parts, gpuOf(m.GPU))
	}
	return strings.Join(parts, ", ")
}

// pausesOf says when a machine pauses by itself: after so long idle, or never.
func pausesOf(m *api.Machine) string {
	if !m.SleepWhenIdle || m.IdleAfterS == nil {
		return "never by itself"
	}
	return "after " + span(float64(*m.IdleAfterS)) + " idle (nothing reached it, its programs quiet)"
}

// networkLine is a machine's internet policy in a word: open, closed, or "allowed: name, range, ...".
func networkLine(n api.Network) string {
	switch n.Internet {
	case "closed":
		return "closed"
	case "allowed":
		if len(n.Allow) == 0 {
			return "allowed: nothing"
		}
		return "allowed: " + strings.Join(n.Allow, ", ")
	default:
		return "open"
	}
}

func gpuOf(g *api.GpuRef) string {
	name := or(g.Model, g.ID)
	if g.Count > 1 {
		name = strconv.Itoa(g.Count) + " x " + name
	}
	return name
}

func place(l *api.Location) string {
	if l == nil {
		return ""
	}
	var parts []string
	for _, p := range []string{l.City, l.Country} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ", ")
}

func labelsOf(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + labels[k]
	}
	return strings.Join(parts, ", ")
}

// age is how long ago, in its largest unit: 12s, 5m, 3h, 4d.
func age(t, now time.Time) string {
	d := max(now.Sub(t), 0)
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	}
	return strconv.Itoa(int(d.Hours()/24)) + "d"
}

// until is how long until a time still to come, in its largest unit, to the nearest one: 30d for a key made to last
// 30 days, though a moment of it has passed by the time it is shown.
func until(t, now time.Time) string {
	d := max(t.Sub(now), 0)
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Round(time.Second).Seconds())) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Round(time.Minute).Minutes())) + "m"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d.Round(time.Hour).Hours())) + "h"
	}
	return strconv.Itoa(int(d.Round(24*time.Hour).Hours()/24)) + "d"
}

// when is a time to the minute and how far from now: "(5m ago)" for one gone by, "(in 30d)" for one to come (a key's
// end).
func when(t, now time.Time) string {
	stamp := t.UTC().Format("2006-01-02 15:04 UTC")
	if t.After(now) {
		return stamp + " (in " + until(t, now) + ")"
	}
	return stamp + " (" + age(t, now) + " ago)"
}

func whenOr(t *time.Time, now time.Time, none string) string {
	if t == nil {
		return none
	}
	return when(*t, now)
}

// humanBytes is a size in binary units: 512 B, 1.5 KiB, 12.0 MiB.
func humanBytes(n float64) string {
	if n < 1024 {
		return strconv.Itoa(int(n)) + " B"
	}
	units := "KMGTPE"
	i := 0
	for n /= 1024; n >= 1024 && i < len(units)-1; i++ {
		n /= 1024
	}
	return strconv.FormatFloat(n, 'f', 1, 64) + " " + units[i:i+1] + "iB"
}

// span is a time something ran for: 45s, 12m 30s, 3h 05m, 2d 4h.
func span(seconds float64) string {
	d := time.Duration(seconds * float64(time.Second)).Round(time.Second)
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	switch {
	case h >= 48:
		return fmt.Sprintf("%dd %dh", h/24, h%24)
	case h > 0:
		return fmt.Sprintf("%dh %02dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm %02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// money is dollars, to the cent, or to a hundredth of a cent under a dollar.
func money(v float64) string { return moneyIn(v, api.CurrencyUSD) }

// moneyIn is an amount of a currency as money says dollars: "£4.10", "€0.0270", "$12.00" - US dollars for an answer
// that names no currency.
func moneyIn(v float64, currency string) string {
	if v != 0 && math.Abs(v) < 1 {
		return fmt.Sprintf("%s%.4f", symbolOf(currency), v)
	}
	return fmt.Sprintf("%s%.2f", symbolOf(currency), v)
}

// symbolOf is a currency's sign: "$", "£" or "€".
func symbolOf(currency string) string {
	switch currency {
	case api.CurrencyGBP:
		return "£"
	case api.CurrencyEUR:
		return "€"
	}
	return "$"
}

// currencyWords is a currency as a sentence names it.
func currencyWords(currency string) string {
	switch currency {
	case api.CurrencyGBP:
		return "pounds sterling (GBP)"
	case api.CurrencyEUR:
		return "euros (EUR)"
	}
	return "US dollars (USD)"
}

// withColumn puts a column - MADE BY, WHOSE - at `at` among a table's cells, when `shown`. Who made each thing, and
// whose it is, is said in an organisation alone; in a person's own account every one is theirs, and the table stays as
// it always was.
func withColumn(shown bool, at int, cells []string, value string) []string {
	if !shown {
		return cells
	}
	return slices.Insert(slices.Clone(cells), at, value)
}

// madeByOf is who made a thing, by their own account's id: "" (a "-" in a table) for a thing nobody is named for.
func madeByOf(person *string) string {
	if person == nil {
		return ""
	}
	return *person
}

// whoseOf is whose a machine of an organisation is, as the dashboard says it too: their name and their role there -
// their level where they manage its team, "Left" once they are no longer in it; "" (a "-") for nobody named.
func whoseOf(h *api.HeldBy) string {
	if h == nil {
		return ""
	}
	name := h.Name
	if name == "" {
		name = h.ID
	}
	role := "Left"
	switch {
	case h.Level != nil && levelWords(*h.Level) != "":
		role = levelWords(*h.Level)
	case h.Role != nil && *h.Role != "":
		role = strings.ToUpper((*h.Role)[:1]) + (*h.Role)[1:]
	}
	return name + " · " + role
}

// levelWords is a team manager's level as the dashboard says it: Approver, Team manager, Full manager; "" for none.
func levelWords(level string) string {
	switch level {
	case api.LevelApprover:
		return "Approver"
	case api.LevelManager:
		return "Team manager"
	case api.LevelFull:
		return "Full manager"
	}
	return ""
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// meter shows how a copy goes on a terminal: bytes, of how many, and how fast.
type meter struct {
	w     io.Writer
	label string
	total int64 // -1: not known
	now   func() time.Time

	mu    sync.Mutex
	done  int64
	began time.Time
	shown time.Time
}

func newMeter(w io.Writer, label string, total int64, now func() time.Time) *meter {
	t := now()
	return &meter{w: w, label: label, total: total, now: now, began: t, shown: t}
}

func (m *meter) add(n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.done += n
	if t := m.now(); t.Sub(m.shown) >= 200*time.Millisecond {
		m.shown = t
		m.print(false)
	}
}

func (m *meter) set(n int64) {
	m.mu.Lock()
	m.done = n
	m.mu.Unlock()
}

// finish shows the last line, ended.
func (m *meter) finish() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.print(true)
}

func (m *meter) line() string {
	s := m.label + "  " + humanBytes(float64(m.done))
	if m.total >= 0 {
		pct := 100
		if m.total > 0 {
			pct = int(m.done * 100 / m.total)
		}
		s += " of " + humanBytes(float64(m.total)) + " (" + strconv.Itoa(pct) + "%)"
	}
	if secs := m.now().Sub(m.began).Seconds(); secs > 0 {
		s += "  " + humanBytes(float64(m.done)/secs) + "/s"
	}
	return s
}

func (m *meter) print(final bool) {
	end := ""
	if final {
		end = "\n"
	}
	fmt.Fprintf(m.w, "\r%-72s%s", m.line(), end)
}

// metered counts what passes through a reader; it winds back when the reader does, for another try.
type metered struct {
	r io.Reader
	m *meter
}

func (r *metered) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.m.add(int64(n))
	return n, err
}

func (r *metered) Seek(offset int64, whence int) (int64, error) {
	s, ok := r.r.(io.Seeker)
	if !ok {
		return 0, fmt.Errorf("this stream cannot be wound back")
	}
	at, err := s.Seek(offset, whence)
	if err == nil {
		r.m.set(at)
	}
	return at, err
}

// wrap breaks words into lines of at most width characters; a line break given is kept.
func wrap(s string, width int) string {
	var b strings.Builder
	for i, para := range strings.Split(s, "\n") {
		if i > 0 {
			b.WriteString("\n")
		}
		n := 0
		for j, w := range strings.Fields(para) {
			if j > 0 && n+1+len(w) > width {
				b.WriteString("\n")
				n = 0
			} else if j > 0 {
				b.WriteString(" ")
				n++
			}
			b.WriteString(w)
			n += len(w)
		}
	}
	return b.String()
}
