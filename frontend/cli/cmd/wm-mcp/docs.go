package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

// search_docs: an agent's way to look in Wheelmachine's docs instead of guessing a flag, a route or a limit.
//
// Its one source is the docs' own file, pages.json, as the site serves it to anyone (the docs' build writes it). What is searched is
// therefore what is public and nothing else: a page the site leaves out is in no copy here, and no word of a call ever reaches the
// site, as the file is read whole and searched here. A file taken into a program when it is built could say more than the site does,
// or less, since the site is put up apart from the programs, and a program may be years old.
//
// The file is read when a call first needs it, never at a start, so nothing starts any slower or fails for want of it. Then a copy
// is kept, and read again when it is docsFresh old, behind the call that noticed: nobody waits for that. When the site cannot be read,
// the copy goes on being used, and the answer says how old it is. A copy is also kept in a file (docsCopy.kept), so that a
// program that starts while the site is away still has one. Only one copy is ever held, what the site gives is taken only within the
// limits below, and one reading goes at a time, so nothing here grows, and no number of calls makes the site be asked more often.

// The file on the site, and the one shape of it this reads. A wm-mcp goes on reading it for as long as it is used, so what is in it
// only ever grows.
const (
	docsPublic   = "https://docs.wheelmachine.ai"
	docsFileName = "pages.json"
	docsFormat   = 1
)

// How often the site is read, and for how long: a copy is good for docsFresh; a reading that failed is tried again after
// docsAgain; a reading takes at most docsReadLongest, and a call with no copy at all waits at most docsWaitLongest for it. A copy
// being written is done long before docsLeftOver, and one kept says it was read no later than docsAhead from now, which is past
// what this computer's clock may have been set back by.
const (
	docsFresh       = 10 * time.Minute
	docsAgain       = time.Minute
	docsReadLongest = 30 * time.Second
	docsWaitLongest = 10 * time.Second
	docsLeftOver    = 10 * time.Minute
	docsAhead       = 24 * time.Hour
)

// The most taken from the site: its file; the values in it of any kind; its pages and parts; the different words they hold, and the times
// a part holds a word, which are what an index is made of (docsHitsMost); the length of a title, a heading, a description or a part's other
// words (docsNameMost), and of a page's name or a part's id (docsPathMost, so that an address from an answer is short enough to give back as
// page); how many words each of the file's lists holds, and how many others a word may have. A part's text is at most what one answer holds.
// Each is far past what the docs are (about 750 KiB, 3,000 values, 42 pages, 600 parts, 3,000 words, 40,000 hits), and a file past any of them
// is not taken, so that what it costs to read and to keep, what a query costs on it, and what an answer can hold are bounded by what it
// holds, whatever the site says. (The docs' own tests hold them well within these.)
const (
	docsFileMost   = 8 << 20
	docsValuesMost = 100_000
	docsPagesMost  = 2_000
	docsPartsMost  = 10_000
	docsWordsMost  = 100_000
	docsHitsMost   = 1_000_000
	docsNameMost   = 1 << 10
	docsPathMost   = 120
	docsListMost   = 2_000
	docsOthersMost = 6
)

// The most a call asks for, and the most it is given: so that a call costs little however it is made, and its answer stays
// short enough for a model to read. A query's words past docsTermsMost count for nothing.
const (
	docsQueryMost   = 500
	docsAddressMost = 300
	docsTermsMost   = 32
	docsLimitUsual  = 5
	docsLimitMost   = 10
	docsPassageMost = 400
	docsTextMost    = 64 << 10
	docsWordLongest = 64
)

// How a word counts in a part. A word in the body counts by how often it is there, for less the longer the part (docsBodyLength)
// and for less each time (docsBodyRepeats), up to about 2, and in its code for docsInCode of that; in the page's title or description
// for this much more, and in what the part is called by, once at the most of these: its heading, the names its page gives it, and
// the words people ask it by. Through the file's `also`, a word counts docsAlso of itself, toward what a part scores and toward how
// much of the query it holds. A word that says what to do and not what about counts docsLight.
const (
	docsBodyRepeats = 1.2
	docsBodyLength  = 0.5
	docsInCode      = 0.3
	docsInHeading   = 2.5
	docsInWords     = 1.5
	docsInAsked     = 2.0
	docsInTitle     = 1.0
	docsInDescribed = 0.7
	docsAlso        = 0.6
	docsLight       = 0.5
)

// Which parts match. A part must hold docsEnough of a query, each word counting by how much it says up to docsHoldMost, and a word the
// docs do not have saying docsUnknown; or docsEnoughSure of it, when what it holds of the query by its own words says docsSure, so that
// one word the docs say otherwise never sinks the part that answers, nor does one common word make a match. How much of a query a part
// holds counts docsHeld times over in its worth. A query said whole in a part's heading or names, as a route or a code is, counts
// docsSaidWhole times, and so does a one-word query at the start of a page whose title has that word. Each page gives its best part, but
// a section near as good as its start (docsSection) is given in the start's place; a page gives its next part too when that is as good as
// docsAnother of its best; and the page found first by its start gives its best section, when that is as good as docsSecond of the start.
// A page with less than docsClose of the best page's worth is left out.
const (
	docsUnknown    = 3.0
	docsHoldMost   = 2.0
	docsEnough     = 0.6
	docsEnoughSure = 0.4
	docsSure       = 2.5
	docsHeld       = 2.0
	docsClose      = 0.15
	docsSaidWhole  = 3.0
	docsSection    = 0.65
	docsSecond     = 0.5
	docsAnother    = 0.6
)

// How a line counts toward a passage: a word in what opens it, a row's first cell or an opening bold, says what the line is about (docsLineSays),
// a piece of a longer name says less than the word itself (docsLinePiece), a word in the form the query says it, "stopped" and not "stop",
// says more (docsLineSaid), a word most parts hold, one that says less than docsLineCommonBelow, says docsLineCommon of itself, and code says
// less than words saying the same (docsLineCode, and docsLineUsage for a line that is one piece of code, as a command's usage is).
const (
	docsLineSays        = 2.0
	docsLinePiece       = 0.3
	docsLineSaid        = 1.25
	docsLineCommon      = 0.5
	docsLineCommonBelow = 1.5
	docsLineCode        = 0.6
	docsLineUsage       = 0.8
)

// docsFile is pages.json: every page by its parts, the words that say nothing (skip), the words that say what to do and not what about,
// which count for little (light), the words people ask with that the docs never say and the docs' word that is the same (same), the words
// people ask with and the docs' words near them (also), two words side by side that mean one thing, and which (phrases), and how much
// each page's parts count against the others (rank, 1 when it does not say).
type docsFile struct {
	Format  int                 `json:"format"`
	Skip    []string            `json:"skip"`
	Light   []string            `json:"light"`
	Same    map[string]string   `json:"same"`
	Also    map[string][]string `json:"also"`
	Phrases map[string]string   `json:"phrases"`
	Pages   []docsPage          `json:"pages"`
}

type docsPage struct {
	Page        string     `json:"page"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Rank        float64    `json:"rank"`
	Parts       []docsPart `json:"parts"`
}

// A part of a page: its heading's id and words, the names its page gives it (a route, its code, a status), the words people ask it by
// that it does not say, and its Markdown, its heading among it. Joined with a blank line between them, a page's parts are its Markdown.
type docsPart struct {
	ID      string `json:"id"`
	Heading string `json:"heading"`
	Words   string `json:"words"`
	Asked   string `json:"asked,omitempty"`
	Text    string `json:"text"`
}

// --- the call ---------------------------------------------------------------------------------------------------------

// docsArgs is what search_docs is given: a query, or a page to read.
type docsArgs struct {
	Query string `json:"query"`
	Page  string `json:"page"`
	Limit *int   `json:"limit"`
}

func (a *docsArgs) check() error {
	query, page := strings.TrimSpace(a.Query), strings.TrimSpace(a.Page)
	switch {
	case query != "" && page != "":
		return docsInvalid("give query or page, not both")
	case query == "" && page == "":
		return docsInvalid("give query, a question or some words; or page, an address from an answer")
	case utf8.RuneCountInString(a.Query) > docsQueryMost:
		return docsInvalid("query is at most %d characters", docsQueryMost)
	case utf8.RuneCountInString(a.Page) > docsAddressMost:
		return docsInvalid("page is at most %d characters", docsAddressMost)
	case a.Limit != nil && (*a.Limit < 1 || *a.Limit > docsLimitMost):
		return docsInvalid("limit is between 1 and %d", docsLimitMost)
	}
	return nil
}

// It reads only what anyone can read, so it asks for no key of its own.
func (a *docsArgs) needsNoKey() {}

// docsProblem is what a call of search_docs comes to when it comes to no answer.
type docsProblem struct {
	Status      int
	Code        string
	Detail      string
	RetryAfterS float64
}

func (e *docsProblem) Error() string { return e.Code + ": " + e.Detail }

func docsInvalid(format string, args ...any) error {
	return &docsProblem{Code: "invalid_request", Detail: fmt.Sprintf(format, args...)}
}

// One page found: its title, the part of it that matched when that is not its start, the address of that part, and what that part says
// nearest what was asked, in its own words.
type docsFound struct {
	Title   string `json:"title"`
	Section string `json:"section,omitempty"`
	URL     string `json:"url"`
	Passage string `json:"passage"`
}

type docsFoundList struct {
	Object string      `json:"object"`
	Data   []docsFound `json:"data"`
	Note   string      `json:"note,omitempty"`
}

// A page, or a part of one, to read.
type docsReading struct {
	Object    string `json:"object"`
	Title     string `json:"title"`
	Section   string `json:"section,omitempty"`
	URL       string `json:"url"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
	Note      string `json:"note,omitempty"`
}

// answer is the call done on the copy there is, or what stands in its way.
func (d *docsCopy) answer(ctx context.Context, a *docsArgs) (any, error) {
	x, note, err := d.current(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(a.Page) != "" {
		return x.read(d.site, a.Page, note)
	}
	limit := docsLimitUsual
	if a.Limit != nil {
		limit = *a.Limit
	}
	found := x.search(d.site, a.Query, limit)
	if len(found) == 0 {
		note = strings.TrimSpace("Nothing in the docs matches that. They are written in English: try other words, in English. " + note)
	}
	return &docsFoundList{Object: "list", Data: found, Note: note}, nil
}

// --- the copy ---------------------------------------------------------------------------------------------------------

// docsCopy is the docs as this program last read them.
type docsCopy struct {
	site   string               // their address, with no slash at its end; "" where they are not read
	kept   string               // the file a copy is kept in between runs; "" for none
	agent  string               // who asks, as the site's log reads it
	say    func(string, ...any) // the log; what is said is never a call's words
	now    func() time.Time     // the clock
	wait   time.Duration        // how long a call waits for a first copy
	client *http.Client         // how the site is asked

	keptOnce sync.Once

	mu      sync.Mutex
	index   *docsIndex
	sum     [sha256.Size]byte
	readAt  time.Time     // when the site gave this copy
	triedAt time.Time     // when the site was last asked, a reading done
	missed  bool          // the last reading failed
	reading chan struct{} // while a reading goes on: closed when it is done
}

func newDocsCopy(site, kept, agent string, say func(string, ...any)) *docsCopy {
	return &docsCopy{site: strings.TrimRight(site, "/"), kept: kept, agent: agent, say: say, now: time.Now, wait: docsWaitLongest,
		// (it stays at the address it was given: the docs' file is at the docs' address, and there is no other to follow)
		client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// current is the copy to answer from, and what to say of its age. A call waits for the site only when there is no copy, or the one
// kept from an earlier run is old and the site has not been asked yet; and then for docsWaitLongest at most.
func (d *docsCopy) current(ctx context.Context) (*docsIndex, string, error) {
	if d.site == "" {
		return nil, "", docsAway("the docs are not read here", 0)
	}
	d.keptOnce.Do(d.readKept)
	d.mu.Lock()
	now := d.now()
	waits := d.index == nil || d.triedAt.IsZero() && now.Sub(d.readAt) >= docsFresh
	after := docsFresh
	if d.index == nil || d.missed {
		after = docsAgain
	}
	if d.reading == nil && (d.triedAt.IsZero() || now.Sub(d.triedAt) >= after) {
		d.reading = make(chan struct{})
		go d.read(d.reading)
	}
	done := d.reading
	d.mu.Unlock()
	late := false
	if waits && done != nil {
		wait := time.NewTimer(d.wait)
		defer wait.Stop()
		select {
		case <-done:
		case <-wait.C:
			late = true
		case <-ctx.Done():
			return nil, "", ctx.Err()
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.index == nil {
		left := docsAgain - d.now().Sub(d.triedAt)
		if d.reading != nil || left < time.Second {
			left = time.Second
		}
		return nil, "", docsAway("the docs cannot be read just now", left)
	}
	note := ""
	if d.missed || late {
		note = "The docs could not be read just now, so this is from the copy last read " + d.readAt.UTC().Format("2006-01-02 15:04") + " UTC."
	}
	return d.index, note, nil
}

// What a call is told when there is nothing to answer from: to wait, and where else the docs are.
func docsAway(said string, wait time.Duration) error {
	detail := said + ": try again in a moment, or read them at " + docsPublic + "/llms.txt"
	return &docsProblem{Status: http.StatusServiceUnavailable, Code: "unavailable", Detail: detail, RetryAfterS: math.Ceil(wait.Seconds())}
}

// read reads the site once, what it read kept in the file before it counts as read. It goes on for the calls to come when the one that
// began it has gone, for at most docsReadLongest.
func (d *docsCopy) read(done chan struct{}) {
	raw, err := d.fetch()
	var x *docsIndex
	sum := docsSum(raw)
	d.mu.Lock()
	same := err == nil && d.index != nil && sum == d.sum
	d.mu.Unlock()
	if err == nil && !same {
		x, err = docsIndexOf(raw)
	}
	if err == nil {
		d.keep(raw)
	}
	d.mu.Lock()
	now := d.now()
	d.triedAt, d.missed = now, err != nil
	if err == nil {
		d.readAt = now
		if !same {
			d.index, d.sum = x, sum
		}
	}
	old, had := d.readAt, d.index != nil
	d.reading = nil
	close(done)
	d.mu.Unlock()
	switch {
	case err != nil && had:
		d.say("the docs could not be read (%v): the copy last read %s stays", err, old.UTC().Format(time.RFC3339))
	case err != nil:
		d.say("the docs could not be read (%v)", err)
	}
}

// What tells one file of the docs from another, whatever is around it.
func docsSum(raw []byte) [sha256.Size]byte { return sha256.Sum256(bytes.TrimSpace(raw)) }

func (d *docsCopy) fetch() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), docsReadLongest)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.site+"/"+docsFileName, nil)
	if err != nil {
		return nil, errors.New("its address is not one")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", d.agent)
	res, err := d.client.Do(req)
	if err != nil {
		return nil, errors.New("the site did not answer")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the site answered %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, docsFileMost+1))
	if err != nil {
		return nil, errors.New("the site's answer stopped short")
	}
	if len(raw) > docsFileMost {
		return nil, errors.New("the site's file is longer than is taken")
	}
	return raw, nil
}

// docsKept is the file a copy is kept in: which site it is of, when that site was last read, and the site's file as it came. A copy of
// another site's is no copy, so that a test's address is never taken for the docs.
type docsKept struct {
	Site   string          `json:"site"`
	ReadAt time.Time       `json:"read_at"`
	File   json.RawMessage `json:"file"`
}

// readKept takes the copy an earlier run kept, as of when it last read the site. It opens the file without waiting and reads it only if
// it is a plain file, as a pipe at that name would hold every call for good. One that cannot be read is no copy, and nor is one that says
// it was read docsAhead or more later than now: what it says of when it was read cannot be so. One read later than now, by less, was read
// now.
func (d *docsCopy) readKept() {
	if d.kept == "" {
		return
	}
	f, err := os.OpenFile(d.kept, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return
	}
	all, err := io.ReadAll(io.LimitReader(f, docsFileMost+1<<10))
	if err != nil {
		return
	}
	var kept docsKept
	now := d.now()
	if json.Unmarshal(all, &kept) != nil || kept.Site != d.site || kept.ReadAt.IsZero() || !kept.ReadAt.Before(now.Add(docsAhead)) || len(kept.File) > docsFileMost {
		return
	}
	x, err := docsIndexOf(kept.File)
	if err != nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.index == nil {
		d.index, d.sum, d.readAt = x, docsSum(kept.File), kept.ReadAt
		if d.readAt.After(now) {
			d.readAt = now
		}
	}
}

// keep writes the copy for the next run, whole or not at all, under a name of its own until it is done, as another program may be doing
// the same; and takes away what a run cut off part-way left under such a name. Failing to is no matter but for that next run, and is said.
func (d *docsCopy) keep(raw []byte) {
	if d.kept == "" {
		return
	}
	d.sweep()
	var tmp *os.File
	err := os.MkdirAll(filepath.Dir(d.kept), 0o755)
	if err == nil {
		tmp, err = os.CreateTemp(filepath.Dir(d.kept), filepath.Base(d.kept)+".*")
	}
	if err == nil {
		// (as it came, and with no "<" written any other way: this is only ever read back)
		write := json.NewEncoder(tmp)
		write.SetEscapeHTML(false)
		err = write.Encode(docsKept{Site: d.site, ReadAt: d.now().UTC(), File: raw})
		if closed := tmp.Close(); err == nil {
			err = closed
		}
		if err == nil {
			err = os.Rename(tmp.Name(), d.kept)
		}
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}
	if err != nil {
		d.say("a copy of the docs could not be kept: %v", err)
	}
}

// sweep takes away a copy that a run cut off part-way left beside the one kept, under a name of the kind keep gives one: one such file
// at most a time, and one older than docsLeftOver, which is long past any copy being written.
func (d *docsCopy) sweep() {
	dir, base := filepath.Split(d.kept)
	entries, err := os.ReadDir(filepath.Clean(dir))
	if err != nil {
		return
	}
	for _, entry := range entries {
		digits, ok := strings.CutPrefix(entry.Name(), base+".")
		if !ok || digits == "" || strings.Trim(digits, "0123456789") != "" || !entry.Type().IsRegular() {
			continue
		}
		// (by this computer's clock, which wrote the file's time)
		if info, err := entry.Info(); err == nil && time.Since(info.ModTime()) > docsLeftOver {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
			return
		}
	}
}

// --- the index --------------------------------------------------------------------------------------------------------

// docsIndex is the file as it is searched: which parts hold each word, in what way.
type docsIndex struct {
	pages   []docsPage
	byName  map[string]*docsPage
	parts   []docsIndexed
	words   map[string]*docsWord
	hits    int
	skip    map[string]bool
	light   map[string]bool
	same    map[string]string
	also    map[string][]string
	phrases map[string]string
	length  float64 // how many words a part has, on average
}

// A part as it is searched.
type docsIndexed struct {
	page   *docsPage
	part   *docsPart
	first  bool    // the page's first part: its address is the page's
	length float64 // its body's words
	said   string  // its names, as they are written, in lower case, a space each side
	head   string  // its heading, or at the page's start its title, in lower case, and without its endings if it is one word
	route  string  // the route its names give, as a query names one: "post /v1/machines/{}/fork"; "" for none
}

// A word: how much it says, by how few parts hold it, and those parts.
type docsWord struct {
	weight float64
	hits   []docsHit
}

// A part that holds a word: how many times in its body's words, how many in its code, and where else.
type docsHit struct {
	part  int32
	body  uint16
	code  uint8
	where uint8
}

const (
	docsWhereTitle uint8 = 1 << iota
	docsWhereDescribed
	docsWhereHeading
	docsWhereWords
	docsWhereAsked
	docsWhereStart // a page's start, and a word its title or its names say as itself, not as a piece of a longer name
)

var (
	docsPageShape = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*(?:/[a-z0-9_-]+)*$`)
	docsPartShape = regexp.MustCompile(`^[A-Za-z0-9_-]*$`)
)

// docsIndexOf reads the site's file, holding it to its shape: it comes from outside this program, and each address given is made
// from it. One that does not hold is not taken, and whatever copy there is stays.
func docsIndexOf(raw []byte) (*docsIndex, error) {
	if docsValuesIn(raw) > docsValuesMost {
		return nil, errors.New("the site's file has too many values")
	}
	var file docsFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, errors.New("the site's file cannot be read")
	}
	if file.Format != docsFormat {
		return nil, fmt.Errorf("the site's file is of format %d", file.Format)
	}
	if len(file.Pages) == 0 || len(file.Pages) > docsPagesMost {
		return nil, fmt.Errorf("the site's file has %d pages", len(file.Pages))
	}
	x := &docsIndex{pages: file.Pages, byName: make(map[string]*docsPage, len(file.Pages)), words: map[string]*docsWord{}}
	if err := x.takeLists(&file); err != nil {
		return nil, err
	}
	parts := 0
	for i := range x.pages {
		p := &x.pages[i]
		if len(p.Page) > docsPathMost || !docsPageShape.MatchString(p.Page) || p.Title == "" || len(p.Title) > docsNameMost || len(p.Description) > docsNameMost || x.byName[p.Page] != nil {
			return nil, errors.New("a page of the site's file is not of its shape")
		}
		if p.Rank <= 0 || p.Rank > 1 {
			p.Rank = 1
		}
		x.byName[p.Page] = p
		parts += len(p.Parts)
		if len(p.Parts) == 0 || parts > docsPartsMost {
			return nil, errors.New("the site's file has too many parts, or a page with none")
		}
	}
	x.parts = make([]docsIndexed, 0, parts)
	for i := range x.pages {
		p := &x.pages[i]
		var title, described map[string]int
		for k := range p.Parts {
			part := &p.Parts[k]
			if part.Heading == "" && k > 0 {
				part.Heading = docsHeadingOf(part.Text)
			}
			if len(part.ID) > docsPathMost || !docsPartShape.MatchString(part.ID) || len(part.Heading) > docsNameMost || len(part.Words) > docsNameMost ||
				len(part.Asked) > docsNameMost || len(part.Text) > docsTextMost {
				return nil, errors.New("a part of the site's file is not of its shape")
			}
			if k == 0 {
				// (what people ask the page by, at its start, is the page's as its title is: every part of it is of that)
				title, described = x.wordsOf(p.Title+" "+part.Asked), x.wordsOf(p.Description)
			}
			body := docsBody(p, part, k == 0)
			if body == "" && (k > 0 || p.Description == "") {
				continue // (a heading of headings: there is nothing there to give. A page's description is something to give.)
			}
			if err := x.add(p, part, k == 0, body, title, described); err != nil {
				return nil, err
			}
		}
	}
	if len(x.parts) == 0 {
		return nil, errors.New("the site's file has no words")
	}
	total := 0.0
	for _, part := range x.parts {
		total += part.length
	}
	x.length = max(total/float64(len(x.parts)), 1)
	n := float64(len(x.parts))
	for name, word := range x.words {
		// (a part that has a word in its code alone, as every example of a call has Authorization, holds it for less; and a page's title,
		// and what it is asked by, are the page's, held once and not by each of its parts)
		have := 0.0
		var page *docsPage
		for _, hit := range word.hits {
			switch {
			case hit.body > 0 || hit.where&(docsWhereHeading|docsWhereWords|docsWhereAsked) != 0:
				have++
			case hit.code > 0:
				have += docsInCode
			case x.parts[hit.part].page != page:
				have++
				page = x.parts[hit.part].page
			}
		}
		// (at least 1: a word most parts hold, such as "machine", still counts when it is what was asked)
		word.weight = max(1, math.Log(1+(n-have+0.5)/(have+0.5)))
		if x.light[name] {
			word.weight = docsLight
		}
	}
	return x, nil
}

// docsValuesIn is about how many values a file holds, found by its commas outside its strings, without taking any of it in: reading a
// value at all costs some 80 bytes, whatever is done with it after, and in a list of ones a value takes two bytes of the file.
func docsValuesIn(raw []byte) int {
	values, quoted := 0, false
	for i := 0; i < len(raw); i++ {
		switch c := raw[i]; {
		case quoted && c == '\\':
			i++
		case c == '"':
			quoted = !quoted
		case !quoted && c == ',':
			values++
		}
	}
	return values
}

// takeLists takes the file's lists of words, in an order of their own, so that the same file comes to the same lists each time.
func (x *docsIndex) takeLists(file *docsFile) error {
	bad := errors.New("a list of words in the site's file is not of its shape")
	if len(file.Skip) > docsListMost || len(file.Light) > docsListMost || len(file.Same) > docsListMost || len(file.Also) > docsListMost || len(file.Phrases) > docsListMost {
		return bad
	}
	x.skip = make(map[string]bool, len(file.Skip))
	for _, word := range file.Skip {
		if len(word) > docsWordLongest {
			return bad
		}
		x.skip[strings.ToLower(word)] = true
	}
	x.light = make(map[string]bool, len(file.Light))
	for _, word := range file.Light {
		if len(word) > docsWordLongest {
			return bad
		}
		x.light[docsStem(strings.ToLower(word))] = true
	}
	x.also = make(map[string][]string, len(file.Also))
	for _, word := range slices.Sorted(maps.Keys(file.Also)) {
		others := file.Also[word]
		if len(word) > docsWordLongest || len(others) > docsOthersMost {
			return bad
		}
		stem := docsStem(strings.ToLower(word))
		for _, other := range others {
			if len(other) > docsWordLongest {
				return bad
			}
			// (words that come to one word have one list, and it is as short as any other)
			if s := docsStem(strings.ToLower(other)); !slices.Contains(x.also[stem], s) && len(x.also[stem]) < docsOthersMost {
				x.also[stem] = append(x.also[stem], s)
			}
		}
	}
	x.same = make(map[string]string, len(file.Same))
	for _, word := range slices.Sorted(maps.Keys(file.Same)) {
		if len(word) > docsWordLongest || len(file.Same[word]) > docsWordLongest {
			return bad
		}
		x.same[docsStem(strings.ToLower(word))] = strings.ToLower(file.Same[word])
	}
	x.phrases = make(map[string]string, len(file.Phrases))
	for _, phrase := range slices.Sorted(maps.Keys(file.Phrases)) {
		first, second, found := strings.Cut(strings.ToLower(phrase), " ")
		if !found || len(phrase) > 2*docsWordLongest || len(file.Phrases[phrase]) > docsWordLongest {
			return bad
		}
		x.phrases[docsStem(first)+" "+docsStem(second)] = strings.ToLower(file.Phrases[phrase])
	}
	return nil
}

// What a part says under its heading: a page's first part begins with its title and its description, which count on their own.
func docsBody(p *docsPage, part *docsPart, first bool) string {
	text := part.Text
	if first {
		text = strings.TrimPrefix(text, "# "+p.Title)
		text = strings.TrimPrefix(strings.TrimLeft(text, "\n"), "> "+p.Description)
	} else if _, rest, found := strings.Cut(text, "\n"); found {
		text = rest
	} else {
		text = ""
	}
	return strings.TrimSpace(text)
}

// add indexes a part.
func (x *docsIndex) add(p *docsPage, part *docsPart, first bool, body string, title, described map[string]int) error {
	prose, code := docsSplitCode(body)
	head, words, asked := x.wordsOf(part.Heading), x.wordsOf(part.Words), x.wordsOf(part.Asked)
	bodied, coded := x.wordsOf(docsLink.ReplaceAllString(prose, "]")), x.wordsOf(code)
	length := 0.0
	for _, n := range bodied {
		length += float64(n)
	}
	for _, n := range coded {
		length += docsInCode * float64(n)
	}
	at := int32(len(x.parts))
	called := part.Heading
	if first {
		called = p.Title
	}
	x.parts = append(x.parts, docsIndexed{page: p, part: part, first: first, length: length,
		said: " " + strings.ToLower(part.Words) + " ", head: docsStem(strings.ToLower(called)), route: docsRouteOf(part.Words)})
	mark := func(of map[string]int, where uint8, into map[string]uint8) {
		for word := range of {
			into[word] |= where
		}
	}
	all := make(map[string]uint8, len(bodied)+len(coded)+len(head)+len(words)+len(asked)+len(title)+len(described))
	for word := range bodied {
		all[word] = 0
	}
	for word := range coded {
		all[word] = 0
	}
	mark(title, docsWhereTitle, all)
	mark(described, docsWhereDescribed, all)
	mark(head, docsWhereHeading, all)
	mark(words, docsWhereWords, all)
	mark(asked, docsWhereAsked, all)
	if first {
		docsEachWord(p.Title+" "+part.Words, func(word string, _ int, joined bool) {
			if !joined && x.counts(word) {
				all[docsStem(word)] |= docsWhereStart
			}
		})
	}
	if x.hits += len(all); x.hits > docsHitsMost {
		return errors.New("the site's file holds too many words in too many parts")
	}
	for word, where := range all {
		w := x.words[word]
		if w == nil {
			if len(x.words) >= docsWordsMost {
				return errors.New("the site's file has too many different words")
			}
			w = &docsWord{}
			x.words[word] = w
		}
		w.hits = append(w.hits, docsHit{part: at, body: uint16(min(bodied[word], math.MaxUint16)), code: uint8(min(coded[word], math.MaxUint8)), where: where})
	}
	return nil
}

// docsSplitCode is a part's words apart from its code (what is between its fences), and its code: an example of a call says
// Authorization and curl in every part that has one, and says nothing of what the part is about.
func docsSplitCode(body string) (prose, code string) {
	var words, coded strings.Builder
	fenced := false
	for line := range strings.Lines(body) {
		if strings.HasPrefix(strings.TrimLeft(line, "> "), "```") {
			fenced = !fenced
			continue
		}
		if fenced {
			coded.WriteString(line)
		} else {
			words.WriteString(line)
		}
	}
	return words.String(), coded.String()
}

// Where a Markdown link leads: its address says nothing of what the part says, and would make every page that links to the errors
// a page about errors.
var docsLink = regexp.MustCompile(`\]\([^)\s]*\)`)

// wordsOf is how many times a text holds each word that says something.
func (x *docsIndex) wordsOf(text string) map[string]int {
	out := map[string]int{}
	docsEachWord(text, func(word string, _ int, joined bool) {
		if joined {
			out[word]++
			for _, piece := range docsPieces(word) {
				if x.counts(piece) {
					out[docsStem(piece)]++
				}
			}
		} else if x.counts(word) {
			out[docsStem(word)]++
		}
	})
	return out
}

// A word of one letter says nothing, and nor does a long one with digits in it: it is an id, one of many.
func (x *docsIndex) counts(word string) bool {
	return !x.skip[word] && utf8.RuneCountInString(word) > 1 && (len(word) < 20 || !strings.ContainsAny(word, "0123456789"))
}

// What a heading says, from its line of Markdown.
func docsHeadingOf(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return strings.TrimSpace(strings.TrimLeft(line, "#"))
}

// docsEachWord calls each with every word of a text, in lower case, with where it begins. Words joined by a mark that names one
// thing - rate_limited, wm-mcp, cloud:secrets:write, /v1/machines - come whole, and joined says so.
func docsEachWord(text string, each func(word string, at int, joined bool)) {
	for i := 0; i < len(text); {
		r, n := utf8.DecodeRuneInString(text[i:])
		if !docsWordRune(r) {
			i += n
			continue
		}
		end, joined := i+n, false
		for end < len(text) {
			r, n := utf8.DecodeRuneInString(text[end:])
			if docsWordRune(r) {
				end += n
				continue
			}
			if !docsJoiner(r) || end+n >= len(text) {
				break
			}
			if next, _ := utf8.DecodeRuneInString(text[end+n:]); !docsWordRune(next) {
				break
			}
			end += n
			joined = true
		}
		// (a word longer than any asked for is no word: a key's shape, a picture's bytes)
		if end-i <= docsWordLongest {
			each(strings.ToLower(text[i:end]), i, joined)
		}
		i = end
	}
}

func docsWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func docsJoiner(r rune) bool { return r == '_' || r == '-' || r == ':' || r == '/' || r == '.' }

func docsPieces(word string) []string { return strings.FieldsFunc(word, docsJoiner) }

// docsStem takes the endings off an English word, so that forks, forked and forking are one word, and machine and machines; kept is
// keep, and organization, the docs' organisation. It need not make a word, only make the same one each time; anything but plain
// letters comes back as it is.
func docsStem(w string) string {
	if len(w) < 3 {
		return w
	}
	for i := 0; i < len(w); i++ {
		if w[i] < 'a' || w[i] > 'z' {
			return w
		}
	}
	if base, ok := docsIrregular[w]; ok {
		w = base
	}
	switch {
	case (strings.HasSuffix(w, "ies") || strings.HasSuffix(w, "ied")) && len(w) > 4:
		w = w[:len(w)-3] + "y" // copies and copied: copy
	case strings.HasSuffix(w, "sses"):
		w = w[:len(w)-2]
	case strings.HasSuffix(w, "s") && len(w) > 3 && !strings.HasSuffix(w, "ss") && !strings.HasSuffix(w, "us") && !strings.HasSuffix(w, "is"):
		w = w[:len(w)-1]
	}
	for _, ending := range []string{"ing", "ed"} {
		base, had := strings.CutSuffix(w, ending)
		if had && len(base) > 1 && strings.ContainsAny(base, "aeiouy") {
			w = base
			if n := len(w); n > 2 && w[n-1] == w[n-2] && !strings.ContainsRune("lszaeiou", rune(w[n-1])) {
				w = w[:n-1] // stopped: stop
			}
			break
		}
	}
	if strings.HasSuffix(w, "e") && len(w) > 3 {
		w = w[:len(w)-1] // make and making: mak
	}
	return docsBritish(w)
}

// The verbs whose past is not theirs with -ed: what the docs say of a machine that was stopped, and what was lost or kept, is asked
// of in the present.
var docsIrregular = map[string]string{
	"kept": "keep", "lost": "lose", "made": "make", "ran": "run", "held": "hold", "gave": "give", "given": "give", "took": "take",
	"taken": "take", "saw": "see", "seen": "see", "began": "begin", "begun": "begin", "brought": "bring",
	"bought": "buy", "sent": "send", "spent": "spend", "built": "build", "chose": "choose", "chosen": "choose", "wrote": "write",
	"written": "write", "paid": "pay", "sold": "sell", "told": "tell", "meant": "mean", "known": "know", "knew": "know",
	"shown": "show", "broke": "break", "broken": "break", "froze": "freeze", "frozen": "freeze", "hid": "hide", "hidden": "hide",
	"drew": "draw", "drawn": "draw", "grew": "grow", "grown": "grow", "threw": "throw", "thrown": "throw", "woke": "wake",
	"woken": "wake", "forgot": "forget", "forgotten": "forget", "undid": "undo", "undone": "undo", "redid": "redo", "redone": "redo",
}

// docsBritish is a word, its endings taken off, as the docs spell it: organis of organisation, as organization and organize are, and
// label of labelled; colour, behaviour, centre, catalogue.
func docsBritish(w string) string {
	if at := strings.LastIndex(w, "iz"); at >= 4 {
		if tail := w[at+2:]; tail == "" || tail == "ation" || tail == "er" {
			return w[:at] + "is" + tail
		}
	}
	if len(w) >= 6 && strings.HasSuffix(w, "ell") {
		return w[:len(w)-1]
	}
	if british, ok := docsSpelled[w]; ok {
		return british
	}
	return w
}

var docsSpelled = map[string]string{"color": "colour", "behavior": "behaviour", "favor": "favour", "center": "centr", "catalog": "catalogu"}

// --- searching --------------------------------------------------------------------------------------------------------

// termsOf is what a query asks for: its words that say something, each once. Two words side by side that the file says are one thing
// are that thing, and a word the docs never say is the docs' word for it. A joined word the docs have is asked for whole, and else by
// its pieces. "How does this work" asks about this.
func (x *docsIndex) termsOf(query string) []string {
	type asked struct {
		word   string
		joined bool
	}
	var words []asked
	docsEachWord(query, func(word string, _ int, joined bool) { words = append(words, asked{word, joined}) })
	if n := len(words); n > 2 && words[0].word == "how" && docsStem(words[n-1].word) == "work" {
		words = words[:n-1]
	}
	var terms []string
	add := func(term string) {
		if !slices.Contains(terms, term) && len(terms) < docsTermsMost {
			terms = append(terms, term)
		}
	}
	for i := 0; i < len(words); i++ {
		w := words[i]
		if i+1 < len(words) {
			if one, found := x.phrases[docsStem(w.word)+" "+docsStem(words[i+1].word)]; found {
				w = asked{word: one}
				i++
			}
		}
		if same, found := x.same[docsStem(w.word)]; found && !w.joined {
			w = asked{word: same}
		}
		switch {
		case !w.joined:
			if x.counts(w.word) {
				add(docsStem(w.word))
			}
		case x.words[w.word] != nil:
			add(w.word)
		default:
			for _, piece := range docsPieces(w.word) {
				if x.counts(piece) {
					add(docsStem(piece))
				}
			}
		}
	}
	return terms
}

// A route named in a query, which is looked for as it is written.
var docsRoute = regexp.MustCompile(`(?i)\b(?:get|post|put|patch|delete)\s+/\S+`)

var docsVersion = regexp.MustCompile(`^v[0-9]+$`)

// docsRouteOf is the route a text names, its method and path in lower case, and each of its path's ids or names of one a {}: so a route
// asked of with a machine's own id, POST /v1/machines/m_01jb2xk3n4p5q6r7s8t9v0w1x2/fork, is the route the docs give with {machine_id}.
func docsRouteOf(text string) string {
	found := docsRoute.FindString(text)
	if found == "" {
		return ""
	}
	method, path, _ := strings.Cut(strings.ToLower(found), " ")
	path, _, _ = strings.Cut(strings.TrimSpace(path), "?")
	segments := strings.Split(strings.TrimRight(path, "/.,;"), "/")
	for i, segment := range segments {
		if strings.HasPrefix(segment, "{") || strings.ContainsAny(segment, "0123456789") && !docsVersion.MatchString(segment) {
			segments[i] = "{}"
		}
	}
	return method + " " + strings.Join(segments, "/")
}

// search is the best part of each of the best pages. A part must hold enough of what was asked, by how much each word says, so that
// one common word never makes a match of a question about something else; and the more of it a part has, and has in its heading
// or its page's title, the better it stands.
func (x *docsIndex) search(site, query string, limit int) []docsFound {
	// (the address of a page of the docs, as an error's link gives one, is that page)
	at := docsWithoutScheme(strings.TrimSpace(query))
	for _, here := range []string{docsWithoutScheme(site), docsWithoutScheme(docsPublic)} {
		if name, ok := strings.CutPrefix(at, here+"/"); ok {
			if page, part, ok := x.find(site, name); ok {
				one := docsIndexed{page: page, part: part, first: part == nil}
				if part == nil {
					one.part = &page.Parts[0]
				}
				return []docsFound{{Title: page.Title, Section: one.section(), URL: docsAddress(site, page, part), Passage: x.passage(&one, nil)}}
			}
		}
	}
	terms := x.termsOf(query)
	if len(terms) == 0 {
		return []docsFound{}
	}
	hits := x.rank(query, terms)
	if len(hits) > limit {
		hits = hits[:limit]
	}
	// (a question that happens to be a heading says nothing against the other pages that answer it)
	for len(hits) > 0 && hits[len(hits)-1].score < docsClose*hits[0].plain {
		hits = hits[:len(hits)-1]
	}
	wanted := x.wanted(query, terms)
	found := make([]docsFound, 0, len(hits))
	for _, h := range hits {
		part := &x.parts[h.at]
		found = append(found, docsFound{Title: part.page.Title, Section: part.section(), URL: docsAddress(site, part.page, part.part),
			Passage: x.passage(part, wanted)})
	}
	return found
}

// A part found, and its worth: as it stands, and without what saying the query whole adds.
type docsRanked struct {
	at           int32
	score, plain float64
}

// A part's tally for a query: what it scores; how much of the query it holds, each word counting by what it says up to docsHoldMost
// and docsAlso of that through `also`; what it holds of the query by its own words, each by all it says; and by name.
type docsTally struct{ score, have, sure, named float64 }

// rank is the parts that match a query, best first: the best part of each page, or a section of it near as good as its start, and its
// next part as well when that is near as good; and when the page found first is found by its start, a section of it as good as
// docsSecond of that start, next.
func (x *docsIndex) rank(query string, terms []string) []docsRanked {
	tallies, total, weights := x.tally(terms)
	// what was said whole: a route in it, or else all of it; among a part's names only as a name, not as an everyday word
	// that happens to be in them
	said := strings.TrimSpace(query)
	if route := docsRoute.FindString(query); route != "" {
		said = route
	}
	lower := strings.ToLower(said)
	asName := lower != said || strings.ContainsAny(said, " /_-:.")
	head, wholeSaid, route := docsStem(lower), " "+lower+" ", docsRouteOf(query)
	// each page's parts that match, best first
	byPage := map[*docsPage][]docsRanked{}
	for i := range tallies {
		t := &tallies[i]
		if t.score == 0 {
			continue
		}
		held := t.have / total
		if held < docsEnough && (held < docsEnoughSure || t.sure < docsSure) {
			continue
		}
		part := &x.parts[i]
		// (a page that says the guides again counts less, but for its start, which says what is its own)
		rank := part.page.Rank
		if part.first {
			rank = 1
		}
		plain := t.score * math.Pow(held, docsHeld) * rank * (1 + t.named/weights)
		score := plain
		if part.head == head || asName && strings.Contains(part.said, wholeSaid) || route != "" && part.route == route {
			score *= docsSaidWhole
		}
		byPage[part.page] = append(byPage[part.page], docsRanked{at: int32(i), score: score, plain: plain})
	}
	byWorth := func(a, b docsRanked) int {
		switch {
		case a.score > b.score:
			return -1
		case a.score < b.score:
			return 1
		}
		return int(a.at - b.at)
	}
	ranked := make([]docsRanked, 0, 2*len(byPage))
	for _, found := range byPage {
		slices.SortFunc(found, byWorth)
		if x.parts[found[0].at].first {
			// (a section that answers near as well as the page's start says more of what was asked)
			for k, other := range found[1:] {
				if other.score >= docsSection*found[0].score {
					found[0], found[k+1] = other, found[0]
					break
				}
			}
		}
		ranked = append(ranked, found[0])
		// (and two parts of one page that each answer are two answers: one is never hidden behind the other)
		if len(found) > 1 && found[1].score >= docsAnother*found[0].score {
			ranked = append(ranked, found[1])
		}
	}
	slices.SortFunc(ranked, byWorth)
	// the page found first, by its start, may have a section near as good: both are given, the section next
	if len(ranked) > 0 && x.parts[ranked[0].at].first {
		found := byPage[x.parts[ranked[0].at].page]
		for _, other := range found {
			if other.at != ranked[0].at && other.score >= docsSecond*ranked[0].score && !slices.ContainsFunc(ranked, func(r docsRanked) bool { return r.at == other.at }) {
				ranked = slices.Insert(ranked, 1, other)
				break
			}
		}
	}
	return ranked
}

// tally is each part's tally for a query's words, and what the words come to: how much a whole query holds, and says.
func (x *docsIndex) tally(terms []string) (tallies []docsTally, total, weights float64) {
	tallies = make([]docsTally, len(x.parts))
	type best struct {
		value, named float64
		own, seen    bool
	}
	bests := make([]best, len(x.parts))
	var touched []int32
	for _, term := range terms {
		word := x.words[term]
		weight := docsUnknown
		if word != nil {
			weight = word.weight
		}
		if x.light[term] {
			weight = docsLight
		}
		// (a word the docs never say counts all it says toward what a part must hold: one rare word of theirs beside it is no match)
		hold := min(weight, docsHoldMost)
		if word == nil && !x.light[term] {
			hold = docsUnknown
		}
		total += hold
		weights += weight
		touched = touched[:0]
		better := func(at int32, value, named float64, own bool) {
			b := &bests[at]
			if !b.seen {
				b.seen = true
				touched = append(touched, at)
			}
			b.value, b.named, b.own = max(b.value, value), max(b.named, named), b.own || own
		}
		x.tallyWord(term, 1, true, len(terms) == 1, better)
		for _, also := range x.also[term] {
			x.tallyWord(also, docsAlso, false, false, better)
		}
		for _, at := range touched {
			b := &bests[at]
			t := &tallies[at]
			t.score += b.value
			t.named += b.named
			if b.own {
				t.have += hold
				t.sure += weight
			} else {
				t.have += docsAlso * hold
			}
			*b = best{}
		}
	}
	return tallies, total, weights
}

// tallyWord calls each with what a word comes to in each part that holds it, and whether that is through the word itself; alone, it
// is all that was asked.
func (x *docsIndex) tallyWord(term string, counts float64, own, alone bool, each func(at int32, value, named float64, own bool)) {
	word := x.words[term]
	if word == nil {
		return
	}
	for _, hit := range word.hits {
		worth := 0.0
		if hit.body > 0 || hit.code > 0 {
			n := (float64(hit.body) + docsInCode*float64(hit.code)) / (1 - docsBodyLength + docsBodyLength*x.parts[hit.part].length/x.length)
			worth = n * (docsBodyRepeats + 1) / (n + docsBodyRepeats)
		}
		if hit.where&docsWhereTitle != 0 {
			worth += docsInTitle
		}
		if hit.where&docsWhereDescribed != 0 {
			worth += docsInDescribed
		}
		// (a word a part is called by counts once, however many of its names it is in: a route's summary is its heading too)
		called := 0.0
		if hit.where&docsWhereHeading != 0 {
			called = docsInHeading
		}
		if hit.where&docsWhereWords != 0 {
			called = max(called, docsInWords)
		}
		if hit.where&docsWhereAsked != 0 {
			called = max(called, docsInAsked)
		}
		worth += called
		if alone && hit.where&docsWhereStart != 0 {
			worth *= docsSaidWhole
		}
		byName := 0.0
		if hit.where&(docsWhereTitle|docsWhereHeading|docsWhereWords|docsWhereAsked) != 0 {
			byName = counts * word.weight
		}
		each(hit.part, counts*word.weight*worth, byName, own)
	}
}

// What a part is called in an answer: nothing when it is the page's start.
func (p *docsIndexed) section() string {
	if p.first {
		return ""
	}
	return p.part.Heading
}

// The address of a part, or of a page when part is nil.
func docsAddress(site string, page *docsPage, part *docsPart) string {
	address := site + "/" + page.Page + ".html"
	if part != nil && part.ID != "" {
		address += "#" + part.ID
	}
	return address
}

// docsWanted is what a passage is chosen by: each word of the query and those `also` gives it, and what each is worth in a line; and
// the query's own words as they are written.
type docsWanted struct {
	at    map[string]int
	worth []float64
	said  map[string]bool
}

// wanted is what each word found in a passage is worth: what it says, and less through `also`, and a word most parts hold, such as
// "machine", for docsLineCommon of that, so that it never decides between two lines alone.
func (x *docsIndex) wanted(query string, terms []string) *docsWanted {
	w := &docsWanted{at: map[string]int{}, said: map[string]bool{}}
	want := func(name string, worth float64) {
		if worth < docsLineCommonBelow {
			worth *= docsLineCommon
		}
		if i, ok := w.at[name]; ok {
			w.worth[i] = max(w.worth[i], worth)
			return
		}
		w.at[name] = len(w.worth)
		w.worth = append(w.worth, worth)
	}
	for _, term := range terms {
		if word := x.words[term]; word != nil {
			want(term, word.weight)
		}
		for _, also := range x.also[term] {
			if word := x.words[also]; word != nil {
				want(also, word.weight*docsAlso)
			}
		}
	}
	// (as it is said: "stopped", which names a state, and not "stop"; but "projects" is "project", one or many)
	docsEachWord(query, func(word string, _ int, joined bool) {
		if !joined && x.counts(word) && len(word) > 4 && (strings.HasSuffix(word, "ed") || strings.HasSuffix(word, "ing")) {
			w.said[word] = true
		}
	})
	return w
}

// passage is the line of a part with the most of what was asked, in its own words, cut to docsPassageMost. A paragraph, a list's item
// and a table's row are each one line of the docs' Markdown; a row comes under the table's first row, and a short line with the lines
// right after it, and a line that leads on with what it leads on to. A line of a word or two (a route, the way back to the errors) is no
// passage, whatever it names. With nothing to go by, it is how the part begins.
func (x *docsIndex) passage(p *docsIndexed, wanted *docsWanted) string {
	if wanted == nil {
		wanted = &docsWanted{}
	}
	lines := strings.Split(p.part.Text, "\n")
	code := make([]bool, len(lines))
	best := make([]float64, len(wanted.worth))
	at, worth, first, fenced := -1, 0.0, -1, false
	for i, line := range lines {
		fence := strings.HasPrefix(strings.TrimLeft(line, "> "), "```")
		if fence {
			fenced = !fenced
		}
		code[i] = fenced
		if fence || strings.TrimSpace(line) == "" || !fenced && strings.HasPrefix(line, "#") || strings.HasPrefix(line, "|-") {
			continue
		}
		plain := line
		if strings.Contains(line, "](") {
			plain = docsLink.ReplaceAllString(line, "]")
		}
		if !fenced && !strings.HasPrefix(line, "|") && docsWordsIn(plain) < 3 {
			continue
		}
		if first < 0 {
			first = i
		}
		w := docsLineWorth(plain, wanted, best)
		switch {
		case fenced:
			w *= docsLineCode
		case len(line) > 2 && line[0] == '`' && strings.IndexByte(line[1:], '`') == len(line)-2:
			w *= docsLineUsage
		}
		if w > worth {
			at, worth = i, w
		}
	}
	if at < 0 {
		at = first
	}
	if at < 0 {
		return p.part.Heading
	}
	line := lines[at]
	if len(line) > docsPassageMost {
		return docsCutAround(line, wanted.at)
	}
	if strings.HasPrefix(line, "|") {
		top := at
		for top > 0 && strings.HasPrefix(lines[top-1], "|") {
			top--
		}
		if top == at {
			return docsWithWhatFollows(line, lines[at+1:]) // (a table's first row says little alone)
		}
		if len(lines[top])+len(line)+1 <= docsPassageMost {
			return lines[top] + "\n" + line
		}
		return line
	}
	text := strings.TrimPrefix(line, "> ")
	for i := at + 1; i < len(lines) && lines[i] != "" && code[i] == code[at] && !strings.HasPrefix(lines[i], "```"); i++ {
		if len(text)+len(lines[i])+1 > docsPassageMost {
			break
		}
		text += "\n" + strings.TrimPrefix(lines[i], "> ")
	}
	if strings.HasSuffix(text, ":") {
		text = docsWithWhatFollows(text, lines[at+1:])
	}
	return text
}

// A line that ends by leading on says little without what it leads on to: the list, the table or the code right after it, as much as a passage holds.
func docsWithWhatFollows(text string, lines []string) string {
	begun := false
	for _, line := range lines {
		line = strings.TrimPrefix(line, "> ")
		switch {
		case strings.HasPrefix(line, "|-"):
			continue
		case line == "" || strings.HasPrefix(line, "```"):
			if begun {
				return text
			}
			continue
		case strings.HasPrefix(line, "#") || len(text)+len(line)+1 > docsPassageMost:
			return text
		}
		text += "\n" + line
		begun = true
	}
	return text
}

func docsWordsIn(line string) int {
	n := 0
	docsEachWord(line, func(string, int, bool) { n++ })
	return n
}

// What a line holds of what was asked: each word once, at its best. output in binary_output is not what a line about a command's output is
// about, "stopped" opening a line about the stopped state says more than it does in a line about another, and "stopped" says more of
// what was asked as "stopped" than "stop" does. The sum is taken in an order of its own: in a map's, the same line could come to another
// worth each time, by the last bit, and the answer to another line. best is room for each word's best, of the words wanted.
func docsLineWorth(line string, wanted *docsWanted, best []float64) float64 {
	clear(best)
	count := func(word string, share float64) {
		if i, ok := wanted.at[word]; ok {
			best[i] = max(best[i], wanted.worth[i]*share)
		}
	}
	about := docsLineAbout(line)
	docsEachWord(line, func(word string, at int, joined bool) {
		share := 1.0
		if at < about {
			share = docsLineSays
		}
		if joined {
			count(word, share)
			for _, piece := range docsPieces(word) {
				count(docsStem(piece), docsLinePiece*share)
			}
			return
		}
		if wanted.said[word] {
			share *= docsLineSaid
		}
		count(docsStem(word), share)
	})
	worth := 0.0
	for _, w := range best {
		worth += w
	}
	return worth
}

// Where what opens a line by saying what it is about ends, or 0 when nothing does: a row's first cell, or an opening bold, as in
// "**paused**: what it means" and "- **The name.** What it means."
func docsLineAbout(line string) int {
	if strings.HasPrefix(line, "| ") {
		return max(strings.Index(line[2:], " |")+2, 0)
	}
	at := 0
	if strings.HasPrefix(line, "- ") {
		at = 2
	}
	if !strings.HasPrefix(line[at:], "**") {
		return 0
	}
	if end := strings.Index(line[at+2:], "**"); end >= 0 {
		return at + 2 + end
	}
	return 0
}

// docsCutAround is docsPassageMost bytes of a long line, from the start of the sentence that first holds what was asked, on whole words
// and whole characters, with "..." where it is cut.
func docsCutAround(line string, wanted map[string]int) string {
	first := -1
	docsEachWord(line, func(word string, at int, joined bool) {
		if first >= 0 {
			return
		}
		if docsHas(wanted, word) || docsHas(wanted, docsStem(word)) {
			first = at
			return
		}
		if joined {
			for _, piece := range docsPieces(word) {
				if docsHas(wanted, docsStem(piece)) {
					first = at
					return
				}
			}
		}
	})
	start := 0
	if first > docsPassageMost/2 {
		// back to where its sentence begins, if that is near enough to see the word; else to a word's start
		earliest := first - docsPassageMost/2
		if at := strings.LastIndex(line[earliest:first], ". "); at >= 0 {
			start = earliest + at + 2
		} else if at := strings.IndexByte(line[earliest:first], ' '); at >= 0 {
			start = earliest + at + 1
		} else {
			start = earliest
			for start < first && !utf8.RuneStart(line[start]) {
				start++
			}
		}
	}
	text, lead := line[start:], ""
	if start > 0 {
		lead = "..."
	}
	most := docsPassageMost - len(lead) - 3
	if len(text) > most {
		end := most
		for end > 0 && !utf8.RuneStart(text[end]) {
			end--
		}
		if at := strings.LastIndexByte(text[:end], ' '); at > most/2 {
			end = at
		}
		text = text[:end] + "..."
	}
	return lead + text
}

func docsHas(wanted map[string]int, word string) bool {
	_, ok := wanted[word]
	return ok
}

// --- reading ----------------------------------------------------------------------------------------------------------

// read is a page whole, or the part of it its address names, up to docsTextMost. It is looked up in the copy and never asked of the site,
// so no address given ever reaches the site, and none reaches anything the site does not give.
func (x *docsIndex) read(site, given, note string) (any, error) {
	page, part, ok := x.find(site, given)
	if !ok {
		return nil, &docsProblem{Status: http.StatusNotFound, Code: "not_found",
			Detail: "the docs have no such page: give an address from an answer, or find one with query"}
	}
	out := &docsReading{Object: "page", Title: page.Title, URL: docsAddress(site, page, part), Note: note}
	var b strings.Builder
	switch {
	case part != nil:
		out.Section = part.Heading
		b.WriteString(part.Text)
	default:
		for k := range page.Parts {
			if k > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(page.Parts[k].Text)
			if b.Len() > docsTextMost {
				break
			}
		}
	}
	text := b.String()
	if len(text) > docsTextMost {
		end := docsTextMost
		for end > 0 && !utf8.RuneStart(text[end]) {
			end--
		}
		if at := strings.LastIndexByte(text[:end], '\n'); at > 0 {
			end = at
		}
		text = text[:end]
		out.Truncated = true
		out.Note = strings.TrimSpace("This is more than one answer holds. Find the part you need with query, and give its address as page. " + note)
	}
	out.Text = text
	return out, nil
}

// docsWithoutScheme is an address without its https://.
func docsWithoutScheme(address string) string {
	if _, rest, found := strings.Cut(address, "://"); found {
		return rest
	}
	return address
}

// find is the page, and the part when it names one, that an address means: the page's address as an answer gives it, or just its
// name, with .html or .md or neither, and a folder of pages its index. An address of anywhere else means nothing.
func (x *docsIndex) find(site, given string) (*docsPage, *docsPart, bool) {
	address, id, _ := strings.Cut(strings.TrimSpace(given), "#")
	address, _, _ = strings.Cut(address, "?")
	here, bare := docsWithoutScheme(site), docsWithoutScheme(address)
	if rest, ok := strings.CutPrefix(bare, here+"/"); ok {
		address = rest
	} else if bare == here {
		address = ""
	} else if bare != address {
		return nil, nil, false
	}
	name := strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(address, "./"), "/"))
	name = strings.TrimSuffix(strings.TrimSuffix(name, ".html"), ".md")
	if name == "" || strings.HasSuffix(name, "/") {
		name += "index"
	}
	page := x.byName[name]
	if page == nil {
		// (errors, as errors/ is: the start of the pages under it)
		page = x.byName[name+"/index"]
	}
	if page == nil {
		return nil, nil, false
	}
	if id == "" {
		return page, nil, true
	}
	for k := range page.Parts {
		if page.Parts[k].ID == id {
			return page, &page.Parts[k], true
		}
	}
	return nil, nil, false
}
