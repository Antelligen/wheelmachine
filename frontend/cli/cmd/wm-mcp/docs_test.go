package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"wheelmachine/frontend/cli/api"
	"wheelmachine/frontend/cli/internal/fakeapi"
)

// No test here reaches the real docs, or writes in this computer's cache folder: search_docs reads what each test gives it, and with
// nothing given says the docs are not read.
func init() {
	theDocs = func() *docsCopy { return newDocsCopy("", "", "test", func(string, ...any) {}) }
}

// A file of the docs' shape.
func docsTestFile(words string) []byte {
	raw, err := json.Marshal(docsFile{Format: docsFormat, Skip: []string{"how", "do", "i", "a", "the", "what", "does", "mean", "error"}, Also: map[string][]string{"create": {"make"}},
		Pages: []docsPage{
			{Page: "machines", Title: "Machines", Description: "What machines are.", Parts: []docsPart{
				{Text: "# Machines\n\n> What machines are.\n\nA machine is a computer of your own, in the cloud."},
				{ID: "make", Heading: "Make one", Text: "## Make one\n\nMake a machine with `wm machines create`.\n\n```sh\n$ wm machines create --name box\n```"},
				{ID: "forks", Heading: "Forks", Text: "## Forks\n\nA fork is a new machine made from one as it is now. " + words},
				{ID: "sizes", Heading: "Sizes", Text: "## Sizes\n\nHow large one is.\n\n| Size | Memory |\n|---|---|\n| 2 CPUs | 4 GB |\n| 8 CPUs | 16 GB |"},
				{ID: "long", Heading: "Long", Text: "## Long\n\n" + strings.Repeat("Words before it. ", 60) + "Here is the longword itself. " + strings.Repeat("Words after it. ", 60)},
			}},
			{Page: "errors/not_open", Title: "not_open", Description: "Not available.", Parts: []docsPart{
				{Text: "# not_open\n\n> Not available.\n\nWhat was asked for is not open yet."},
			}},
		}})
	if err != nil {
		panic(err)
	}
	return raw
}

// The site, standing in: what it answers, and what it was asked.
type docsStandIn struct {
	*httptest.Server
	mu    sync.Mutex
	body  []byte
	away  bool
	asked []*http.Request
}

func docsSiteOf(t *testing.T, body []byte) *docsStandIn {
	t.Helper()
	s := &docsStandIn{body: body}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.asked = append(s.asked, r)
		switch {
		case s.away:
			http.Error(w, "away", http.StatusBadGateway)
		case r.URL.Path != "/"+docsFileName:
			http.NotFound(w, r)
		default:
			_, _ = w.Write(s.body)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *docsStandIn) set(body []byte, away bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.body, s.away = body, away
}

func (s *docsStandIn) times() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.asked)
}

// docsRead makes search_docs read this site for this test, keeping its copy in kept, on a clock of the test's own; and a server with
// no key at all behind it.
func docsRead(t *testing.T, s *docsStandIn, kept string) (*session, *docsCopy, func(time.Duration)) {
	t.Helper()
	d := newDocsCopy(s.URL, kept, "test", func(string, ...any) {})
	began := time.Now()
	var mu sync.Mutex
	d.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return began
	}
	before := theDocs
	theDocs = func() *docsCopy { return d }
	t.Cleanup(func() { theDocs = before })
	s2 := start(t, func() (*api.Client, error) { return nil, api.ErrNoKey })
	s2.initialize("2025-11-25")
	return s2, d, func(by time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		began = began.Add(by)
	}
}

func docsDone(t *testing.T, d *docsCopy) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(time.Millisecond) {
		d.mu.Lock()
		reading := d.reading != nil
		d.mu.Unlock()
		if !reading {
			return
		}
	}
	t.Fatal("the reading of the site did not end")
}

func docsAddresses(t *testing.T, s *docsStandIn, body map[string]any) []string {
	t.Helper()
	if body["object"] != "list" {
		t.Fatalf("not a list: %v", body)
	}
	found := []string{}
	for _, item := range body["data"].([]any) {
		one := item.(map[string]any)
		if one["title"] == "" || one["passage"] == "" {
			t.Fatalf("one found: %v", one)
		}
		found = append(found, strings.TrimPrefix(one["url"].(string), s.URL+"/"))
	}
	return found
}

// It needs no key: an agent can read how to get one before it has one. The next tool, which calls the API, says it has none.
func TestSearchDocsNeedsNoKey(t *testing.T) {
	site := docsSiteOf(t, docsTestFile(""))
	s, _, _ := docsRead(t, site, "")
	isErr, body := s.tool(2, "search_docs", map[string]any{"query": "how do I fork a machine"})
	if found := docsAddresses(t, site, body); isErr || len(found) != 1 || found[0] != "machines.html#forks" {
		t.Fatalf("searched with no key: %v", body)
	}
	isErr, who := s.tool(3, "whoami", map[string]any{})
	if e, _ := who["error"].(map[string]any); !isErr || e["code"] != api.CodeUnauthorized {
		t.Fatalf("whoami with no key: %v", who)
	}
	// and it is told nothing: no key, were there one, no word of a call
	site.mu.Lock()
	defer site.mu.Unlock()
	if len(site.asked) != 1 {
		t.Fatalf("the site was asked %d times", len(site.asked))
	}
	r := site.asked[0]
	if r.URL.Path != "/pages.json" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" || strings.Contains(fmt.Sprint(r.URL, r.Header), "fork") {
		t.Fatalf("what the site was told: %s %v", r.URL, r.Header)
	}
}

// With a key set, the site still never sees it.
func TestTheKeyNeverGoesToTheDocs(t *testing.T) {
	site := docsSiteOf(t, docsTestFile(""))
	d := newDocsCopy(site.URL, "", "test", func(string, ...any) {})
	before := theDocs
	theDocs = func() *docsCopy { return d }
	t.Cleanup(func() { theDocs = before })
	f := fakeapi.New(t)
	s := start(t, connectTo(f))
	s.initialize("2025-11-25")
	if isErr, body := s.tool(2, "search_docs", map[string]any{"query": "fork"}); isErr {
		t.Fatal(body)
	}
	if isErr, who := s.tool(3, "whoami", map[string]any{}); isErr {
		t.Fatal(who)
	}
	site.mu.Lock()
	defer site.mu.Unlock()
	for _, r := range site.asked {
		if r.Header.Get("Authorization") != "" || strings.Contains(fmt.Sprint(r.Header), f.Key) {
			t.Fatalf("the site was told: %v", r.Header)
		}
	}
}

// What it answers, what it reads, and what it takes.
func TestSearchDocs(t *testing.T) {
	site := docsSiteOf(t, docsTestFile(""))
	s, _, _ := docsRead(t, site, "")
	call := func(args map[string]any) (bool, map[string]any) {
		t.Helper()
		return s.tool(2, "search_docs", args)
	}
	isErr, body := call(map[string]any{"query": "what does not_open mean", "limit": 1})
	if isErr || fmt.Sprint(docsAddresses(t, site, body)) != "[errors/not_open.html]" {
		t.Fatalf("a code: %v", body)
	}
	one := body["data"].([]any)[0].(map[string]any)
	if one["title"] != "not_open" || one["section"] != nil || one["passage"] != "What was asked for is not open yet." {
		t.Fatalf("a page's start: %v", one)
	}
	// a word the docs say otherwise
	isErr, body = call(map[string]any{"query": "create a machine"})
	if isErr || docsAddresses(t, site, body)[0] != "machines.html#make" {
		t.Fatalf("create, for make: %v", body)
	}
	// a table's row comes under the table's first row; a line of code with what it is in, quoted as it is
	isErr, body = call(map[string]any{"query": "16 GB"})
	if isErr || body["data"].([]any)[0].(map[string]any)["passage"] != "| Size | Memory |\n| 8 CPUs | 16 GB |" {
		t.Fatalf("a table's row: %v", body)
	}
	isErr, body = call(map[string]any{"query": "--name"})
	if isErr || body["data"].([]any)[0].(map[string]any)["passage"] != "$ wm machines create --name box" {
		t.Fatalf("a line of code: %v", body)
	}
	// a long paragraph is cut around what was asked, on whole words
	isErr, body = call(map[string]any{"query": "longword"})
	passage := body["data"].([]any)[0].(map[string]any)["passage"].(string)
	if isErr || !strings.HasPrefix(passage, "...Here is the longword itself. Words after it.") || len(passage) > docsPassageMost || !strings.HasSuffix(passage, "...") {
		t.Fatalf("a long paragraph: %q", passage)
	}
	// what is not there
	for _, query := range []string{"banana smoothie", "如何分叉机器", "¿cómo bifurco?", "the"} {
		isErr, body = call(map[string]any{"query": query})
		if note, _ := body["note"].(string); isErr || len(docsAddresses(t, site, body)) != 0 || !strings.Contains(note, "in English") {
			t.Fatalf("%q: %v", query, body)
		}
	}
	// a page or a part of it, by the address an answer gave
	isErr, page := call(map[string]any{"page": site.URL + "/machines.html"})
	if isErr || page["object"] != "page" || page["title"] != "Machines" || !strings.HasPrefix(page["text"].(string), "# Machines\n\n> What machines are.") ||
		!strings.Contains(page["text"].(string), "## Sizes") || page["truncated"] != false {
		t.Fatalf("a page: %.200v", page)
	}
	isErr, part := call(map[string]any{"page": "machines.md#forks"})
	if isErr || part["section"] != "Forks" || part["url"] != site.URL+"/machines.html#forks" || !strings.HasPrefix(part["text"].(string), "## Forks\n") || strings.Contains(part["text"].(string), "## Sizes") {
		t.Fatalf("a part: %v", part)
	}
	for _, given := range []string{"deploy", "deploy.html", "https://example.com/machines.html", "machines#nothing", "../machines"} {
		isErr, failed := call(map[string]any{"page": given})
		if e, _ := failed["error"].(map[string]any); !isErr || e["code"] != "not_found" || e["status"] != 404.0 {
			t.Fatalf("%q: %v", given, failed)
		}
	}
	// what it does not take
	for _, args := range []map[string]any{{}, {"query": " "}, {"query": "a", "page": "b"}, {"query": strings.Repeat("é", docsQueryMost+1)}, {"page": strings.Repeat("é", docsAddressMost+1)},
		{"query": "a", "limit": 0}, {"query": "a", "limit": docsLimitMost + 1}, {"query": 1}, {"query": "a", "other": 1}} {
		isErr, failed := call(args)
		e, _ := failed["error"].(map[string]any)
		if !isErr || e["code"] != api.CodeInvalidRequest || strings.Contains(fmt.Sprint(e), "éé") {
			t.Fatalf("%.50v: %v", args, failed)
		}
	}
}

// A page longer than one answer gives as much as one holds, and says what to do.
func TestALongPageIsCut(t *testing.T) {
	// (each part within what one answer holds, as a part longer than that is not taken)
	words := strings.Repeat("A line of words, and é.\n", docsTextMost/30)
	long, err := json.Marshal(docsFile{Format: docsFormat, Pages: []docsPage{{Page: "long", Title: "Long", Parts: []docsPart{
		{Text: "# Long\n\n" + words},
		{ID: "more", Heading: "More", Text: "## More\n\n" + words},
		{ID: "again", Heading: "Again", Text: "## Again\n\n" + words},
		{ID: "next", Heading: "Next", Text: "## Next\n\nA part after."},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	site := docsSiteOf(t, long)
	s, _, _ := docsRead(t, site, "")
	isErr, page := s.tool(2, "search_docs", map[string]any{"page": "long"})
	if text := page["text"].(string); isErr || page["truncated"] != true || len(text) > docsTextMost || !strings.HasSuffix(text, "é.") ||
		!strings.Contains(page["note"].(string), "with query") {
		t.Fatalf("%d bytes, %v", len(text), page["note"])
	}
	if isErr, part := s.tool(3, "search_docs", map[string]any{"page": "long#next"}); isErr || part["truncated"] != false {
		t.Fatalf("its part: %v", part)
	}
}

// The site away: no copy, and it says so and when to try again; a copy, and it answers from it and says how old it is; a server started
// while it is away, and it has the copy the last one kept, in one file that nothing grows beside. The site back, and it follows.
func TestSearchDocsWhenTheSiteIsAway(t *testing.T) {
	kept := filepath.Join(t.TempDir(), "cache", "wheelmachine", "docs-pages.json")
	site := docsSiteOf(t, docsTestFile("oldword"))
	site.set(docsTestFile("oldword"), true)
	s, d, later := docsRead(t, site, kept)
	isErr, failed := s.tool(2, "search_docs", map[string]any{"query": "oldword"})
	e, _ := failed["error"].(map[string]any)
	if !isErr || e["code"] != api.CodeUnavailable || e["status"] != 503.0 || e["retry_after_s"] == nil || !strings.Contains(e["detail"].(string), "try again") {
		t.Fatalf("no copy: %v", failed)
	}
	for range 10 {
		s.tool(3, "search_docs", map[string]any{"query": "oldword"})
	}
	if n := site.times(); n != 1 {
		t.Fatalf("the site was asked %d times", n)
	}
	// back
	site.set(docsTestFile("oldword"), false)
	later(docsAgain + time.Second)
	isErr, body := s.tool(4, "search_docs", map[string]any{"query": "oldword"})
	if isErr || len(docsAddresses(t, site, body)) != 1 || body["note"] != nil {
		t.Fatalf("the site back: %v", body)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Fatalf("no copy was kept: %v", err)
	}
	// away again
	site.set(docsTestFile("oldword"), true)
	later(docsFresh + time.Second)
	s.tool(5, "search_docs", map[string]any{"query": "oldword"})
	docsDone(t, d)
	isErr, body = s.tool(6, "search_docs", map[string]any{"query": "oldword"})
	if note, _ := body["note"].(string); isErr || len(docsAddresses(t, site, body)) != 1 || !strings.Contains(note, "last read") {
		t.Fatalf("a copy, the site away: %v", body)
	}
	// back, with other words
	site.set(docsTestFile("newword"), false)
	later(docsAgain + time.Second)
	s.tool(7, "search_docs", map[string]any{"query": "newword"})
	docsDone(t, d)
	isErr, body = s.tool(8, "search_docs", map[string]any{"query": "newword"})
	if isErr || len(docsAddresses(t, site, body)) != 1 || body["note"] != nil {
		t.Fatalf("back with other words: %v", body)
	}
	var line docsKept
	if all, _ := os.ReadFile(kept); json.Unmarshal(all, &line) != nil || string(line.File) != string(docsTestFile("newword")) || line.Site != site.URL {
		t.Fatalf("the new copy was not kept: %.100s", all)
	}
	if entries, _ := os.ReadDir(filepath.Dir(kept)); len(entries) != 1 {
		t.Fatalf("beside the copy: %v", entries)
	}
	// and a server started while the site is away has that copy
	site.set(docsTestFile("newword"), true)
	s2, d2, _ := docsRead(t, site, kept)
	isErr, body = s2.tool(2, "search_docs", map[string]any{"query": "newword"})
	docsDone(t, d2)
	if isErr || len(docsAddresses(t, site, body)) != 1 {
		t.Fatalf("a server started with the site away: %v", body)
	}
}

// The words of a question and of the docs come to the same words.
func TestDocsWordsComeToTheSameWord(t *testing.T) {
	for _, same := range [][]string{
		{"fork", "forks", "forked", "forking"},
		{"machine", "machines"},
		{"limit", "limits", "limited"},
		{"copy", "copies", "copied", "copying"},
		{"retry", "retries", "retried", "retrying"},
		{"stop", "stopped", "stopping"},
		{"run", "runs", "running"},
		{"make", "makes", "making"},
		{"key", "keys"},
		{"pause", "paused", "pausing", "pauses"},
		// a past that is not -ed, and the American spelling of the docs' British
		{"keep", "keeps", "keeping", "kept"},
		{"lose", "loses", "losing", "lost"},
		{"take", "takes", "taking", "took", "taken"},
		{"organisation", "organisations", "organization", "organizations"},
		{"organise", "organised", "organize", "organized", "organizing"},
		{"authorise", "authorize", "authorized"},
		{"label", "labels", "labelled", "labeled", "labelling"},
		{"cancel", "cancelled", "canceled"},
		{"colour", "color"},
	} {
		for _, word := range same {
			if docsStem(word) != docsStem(same[0]) {
				t.Errorf("%s comes to %s, and %s to %s", word, docsStem(word), same[0], docsStem(same[0]))
			}
		}
	}
	// a word that only looks so, and what is not plain letters, are left as they are
	for word, want := range map[string]string{"status": "status", "class": "class", "string": "string", "ssh": "ssh", "máquina": "máquina", "v1": "v1", "is": "is",
		"size": "siz", "resize": "resiz", "install": "install", "shell": "shell", "pizza": "pizza"} {
		if got := docsStem(word); got != want {
			t.Errorf("%s came to %s", word, got)
		}
	}
	type word struct {
		word   string
		joined bool
	}
	var got []word
	docsEachWord("Run `rate_limited`, /v1/machines, wm-mcp and cloud:secrets:write. Done... 如何", func(w string, _ int, joined bool) {
		got = append(got, word{w, joined})
	})
	want := []word{{"run", false}, {"rate_limited", true}, {"v1/machines", true}, {"wm-mcp", true}, {"and", false}, {"cloud:secrets:write", true}, {"done", false}, {"如何", false}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("%v", got)
	}
}

// What a query asks for: two words that mean one thing are that thing, "how does this work" asks about this, and an id is no word.
func TestWhatAQueryAsksFor(t *testing.T) {
	raw, err := json.Marshal(docsFile{Format: docsFormat, Skip: []string{"how", "do", "does", "i", "a", "in", "what", "is"}, Light: []string{"add"},
		Phrases: map[string]string{"log in": "login", "request id": "request_id"}, Same: map[string]string{"vm": "machine", "vms": "machine"},
		Pages: []docsPage{{Page: "p", Title: "T", Parts: []docsPart{{Text: "# T\n\nWords, and an add."}}}}})
	if err != nil {
		t.Fatal(err)
	}
	x, err := docsIndexOf(raw)
	if err != nil {
		t.Fatal(err)
	}
	for query, want := range map[string]string{
		"how do I log in":       "login",
		"logging in":            "login",
		"log":                   "log",
		"what is a request id":  "request_id",
		"how does billing work": "bill",
		"the work folder":       "the work folder",
		"POST /v1/machines/m_01jb2xk3n4p5q6r7s8t9v0w1x2/fork": "post v1 machin fork",
		"start my VMs":     "start my machin",
		"the organization": "the organisation",
	} {
		if got := strings.Join(x.termsOf(query), " "); got != want {
			t.Errorf("%q asks for %q", query, got)
		}
	}
	if word := x.words["add"]; word == nil || word.weight != docsLight {
		t.Errorf("a light word counts for %v", word)
	}
	// a route asked of with a machine's own id, or a backup's, is the route the docs give with the names of ids
	for text, want := range map[string]string{
		"POST /v1/machines/m_01jb2xk3n4p5q6r7s8t9v0w1x2/fork":                  "post /v1/machines/{}/fork",
		"what does POST /v1/machines/{machine_id}/fork do":                     "post /v1/machines/{}/fork",
		"GET /v1/machines/m_01jb2xk3n4p5q6r7s8t9v0w1x2/backups/20261023T0935Z": "get /v1/machines/{}/backups/{}",
		"GET /v1/machines?state=running":                                       "get /v1/machines",
		"get /v1/me.":                                                          "get /v1/me",
		"how do I fork a machine":                                              "",
	} {
		if got := docsRouteOf(text); got != want {
			t.Errorf("%q names %q", text, got)
		}
	}
}

// What the site gives is held to its shape: nothing else is taken, and a page's name never climbs out of the site.
func TestTheDocsFileIsHeldToItsShape(t *testing.T) {
	if _, err := docsIndexOf(docsTestFile("")); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"not JSON":           `<!doctype html>`,
		"cut short":          string(docsTestFile("")[:40]),
		"other format":       `{"format":2,"pages":[{"page":"a","title":"T","parts":[{"text":"# T\n\nwords"}]}]}`,
		"no pages":           `{"format":1,"pages":[]}`,
		"no parts":           `{"format":1,"pages":[{"page":"a","title":"T","parts":[]}]}`,
		"no title":           `{"format":1,"pages":[{"page":"a","title":"","parts":[{"text":"# T\n\nwords"}]}]}`,
		"a name upwards":     `{"format":1,"pages":[{"page":"../a","title":"T","parts":[{"text":"# T\n\nwords"}]}]}`,
		"an address":         `{"format":1,"pages":[{"page":"https://a.example/b","title":"T","parts":[{"text":"# T\n\nwords"}]}]}`,
		"a page twice":       `{"format":1,"pages":[{"page":"a","title":"T","parts":[{"text":"# T\n\nwords"}]},{"page":"a","title":"T","parts":[{"text":"# T\n\nwords"}]}]}`,
		"a part's id":        `{"format":1,"pages":[{"page":"a","title":"T","parts":[{"id":"\"><b>","text":"## T\n\nwords"}]}]}`,
		"no words":           `{"format":1,"pages":[{"page":"a","title":"T","parts":[{"text":"# T"}]}]}`,
		"a long title":       `{"format":1,"pages":[{"page":"a","title":"` + strings.Repeat("T", docsNameMost+1) + `","parts":[{"text":"# T\n\nwords"}]}]}`,
		"a long heading":     `{"format":1,"pages":[{"page":"a","title":"T","parts":[{"text":"# T\n\nwords"},{"id":"b","text":"## ` + strings.Repeat("H", docsNameMost+1) + `\n\nwords"}]}]}`,
		"a long description": `{"format":1,"pages":[{"page":"a","title":"T","description":"` + strings.Repeat("d ", docsNameMost) + `","parts":[{"text":"# T\n\nwords"}]}]}`,
		"a long name":        `{"format":1,"pages":[{"page":"` + strings.Repeat("a", docsPathMost+1) + `","title":"T","parts":[{"text":"# T\n\nwords"}]}]}`,
		"a long id":          `{"format":1,"pages":[{"page":"a","title":"T","parts":[{"id":"` + strings.Repeat("a", docsPathMost+1) + `","text":"## T\n\nwords"}]}]}`,
		"long asked words":   `{"format":1,"pages":[{"page":"a","title":"T","parts":[{"id":"b","asked":"` + strings.Repeat("w ", docsNameMost) + `","text":"## T\n\nwords"}]}]}`,
		"many others":        `{"format":1,"also":{"aa":["` + strings.Repeat(`xx","`, docsOthersMost) + `xx"]},"pages":[{"page":"a","title":"T","parts":[{"text":"# T\n\nwords"}]}]}`,
		"many values":        `{"format":1,"more":[` + strings.Repeat("1,", docsValuesMost) + `1],"pages":[{"page":"a","title":"T","parts":[{"text":"# T\n\nwords"}]}]}`,
	} {
		if _, err := docsIndexOf([]byte(body)); err == nil {
			t.Errorf("%s was taken", name)
		}
	}
	// a newer file's fields, which this does not know, are no matter: a wm-mcp already out there goes on reading it
	if _, err := docsIndexOf([]byte(`{"format":1,"more":true,"pages":[{"page":"a","title":"T","more":1,"parts":[{"more":[],"text":"# T\n\nwords"}]}]}`)); err != nil {
		t.Errorf("a newer file: %v", err)
	}
}

// On the real docs, where they are beside this tool: the docs' build writes what this reads, a real question finds its page, and Deploy's
// page is found or not as the docs' one switch for it says.
func TestSearchDocsOnTheRealDocs(t *testing.T) {
	build := filepath.Join("..", "..", "..", "docs", "build.py")
	if _, err := os.Stat(build); errors.Is(err, os.ErrNotExist) {
		t.Skip("the docs are not beside this tool")
	}
	out := t.TempDir()
	if said, err := exec.Command("python3", "-I", "-B", build, "--out", out).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, said)
	}
	raw, err := os.ReadFile(filepath.Join(out, docsFileName))
	if err != nil {
		t.Fatal(err)
	}
	site := docsSiteOf(t, raw)
	s, _, _ := docsRead(t, site, "")
	for query, want := range map[string]string{
		"how do I fork a machine":       "machines.html#forks",
		"what does error not_open mean": "errors/not_open.html",
		"POST /v1/machines":             "reference.html#createMachine",
	} {
		isErr, body := s.tool(2, "search_docs", map[string]any{"query": query})
		if found := docsAddresses(t, site, body); isErr || len(found) == 0 || found[0] != want {
			t.Errorf("%q found %v", query, found)
		}
	}
	open := strings.Contains(string(raw), `"page":"deploy"`)
	if strings.Contains(strings.ToLower(string(raw)), "deployment") != open {
		t.Errorf("the docs' file names Deploy's page: %v, and says the word: %v", open, !open)
	}
	for _, query := range []string{"deploy my app", "wm deploy", "/v1/deployments"} {
		_, body := s.tool(3, "search_docs", map[string]any{"query": query})
		for _, address := range docsAddresses(t, site, body) {
			if !open && strings.HasPrefix(address, "deploy") {
				t.Errorf("%q found %s", query, address)
			}
		}
	}
	for _, given := range []string{"deploy", "deploy.html", "deploy.md", site.URL + "/deploy.html"} {
		isErr, answer := s.tool(4, "search_docs", map[string]any{"page": given})
		if e, _ := answer["error"].(map[string]any); isErr == open || !open && e["code"] != api.CodeNotFound {
			t.Errorf("%q: %.100v", given, answer)
		}
	}
}

// A copy kept is of its site alone: a test's address, or another's, is never taken for the docs. And a server's first answer from it says when
// the site was last read, not how old the file is.
func TestACopyKeptIsOfItsSite(t *testing.T) {
	kept := filepath.Join(t.TempDir(), "kept")
	one := docsSiteOf(t, docsTestFile("oneword"))
	first := newDocsCopy(one.URL, kept, "test", func(string, ...any) {})
	if _, err := first.answer(context.Background(), &docsArgs{Query: "oneword"}); err != nil {
		t.Fatal(err)
	}
	docsDone(t, first)
	long := time.Now().Add(-1000 * time.Hour)
	if err := os.Chtimes(kept, long, long); err != nil {
		t.Fatal(err)
	}
	one.set(docsTestFile("oneword"), true)
	again := newDocsCopy(one.URL, kept, "test", func(string, ...any) {})
	if _, err := again.answer(context.Background(), &docsArgs{Query: "oneword"}); err != nil {
		t.Fatal(err)
	}
	docsDone(t, again)
	out, err := again.answer(context.Background(), &docsArgs{Query: "oneword"})
	if err != nil {
		t.Fatal(err)
	}
	if list := out.(*docsFoundList); len(list.Data) != 1 || !strings.Contains(list.Note, "last read") || strings.Contains(list.Note, long.UTC().Format("2006-01-02")) {
		t.Fatalf("from the copy: %v", list)
	}
	other := docsSiteOf(t, docsTestFile("otherword"))
	other.set(docsTestFile("otherword"), true)
	elsewhere := newDocsCopy(other.URL, kept, "test", func(string, ...any) {})
	if _, err := elsewhere.answer(context.Background(), &docsArgs{Query: "oneword"}); err == nil || !strings.Contains(err.Error(), api.CodeUnavailable) {
		t.Fatalf("another site's copy was taken: %v", err)
	}
}
