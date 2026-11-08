package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"wheelmachine/frontend/cli/api"
	"wheelmachine/frontend/cli/internal/fakeapi"
)

func fastDeploy(t *testing.T) {
	t.Helper()
	was := deployPollEvery
	deployPollEvery = time.Millisecond
	t.Cleanup(func() { deployPollEvery = was })
}

func aFolder(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "My Shop")
	for name, text := range map[string]string{"package.json": `{"scripts":{"start":"node ."}}`, "index.js": "1", ".env": "S=1", "node_modules/x/a.js": "2", "lib/b.js": "3"} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// Until Deploy opens, wm's help says nothing of it, and each of its own commands says it is not open yet.
func TestHelpOffersDeployOnlyOnceItOpens(t *testing.T) {
	if api.DeployOpen {
		t.Skip("Deploy is open")
	}
	h := newHarness(t)
	if r := h.wm("", "-h").must(t, 0); strings.Contains(strings.ToLower(r.stdout), "deploy") {
		t.Fatalf("%s", r.stdout)
	}
	if r := h.wm("", "help").must(t, 0); strings.Contains(strings.ToLower(r.stdout), "deploy") {
		t.Fatalf("%s", r.stdout)
	}
	for _, args := range [][]string{{"deploy", "-h"}, {"deployments", "-h"}, {"deployments", "ls", "-h"}, {"help", "deploy"}} {
		if r := h.wm("", args...).must(t, 0); !strings.Contains(r.stdout, "Not open yet") {
			t.Fatalf("%v:\n%s", args, r.stdout)
		}
	}
}

func TestDeploySaysItIsNotOpen(t *testing.T) {
	h := newHarness(t)
	h.api.Deploy.Closed = true
	r := h.wm("", "deployments", "ls").must(t, 1)
	if !strings.Contains(r.stderr, "Deploy is not open yet") {
		t.Fatalf("%s", r.stderr)
	}
	r = h.wm("", "deploy", "https://github.com/acme/shop").must(t, 1)
	if !strings.Contains(r.stderr, "Deploy is not open yet") {
		t.Fatalf("%s", r.stderr)
	}
	// said before a folder is packed, or anything is said of it
	r = h.wm("", "deploy", aFolder(t)).must(t, 1)
	if !strings.Contains(r.stderr, "Deploy is not open yet") || strings.Contains(r.stderr, "Left out") || strings.Contains(r.stderr, "What goes") {
		t.Fatalf("%s", r.stderr)
	}
	r = h.wm("", "deployments", "redeploy", "dep_01jb2xk3n4p5q6r7s8t9v0w1x2", "--files", aFolder(t)).must(t, 1)
	if !strings.Contains(r.stderr, "Deploy is not open yet") || strings.Contains(r.stderr, "Left out") || strings.Contains(r.stderr, "What goes") {
		t.Fatalf("%s", r.stderr)
	}
}

func TestDeployingAFolderSendsItWithoutWhatStaysHere(t *testing.T) {
	fastDeploy(t)
	h := newHarness(t)
	r := h.wm("", "deploy", aFolder(t)).must(t, 0)
	asked := h.api.DeployAsked()
	if len(asked) != 1 {
		t.Fatalf("%+v", asked)
	}
	a := asked[0]
	if !a.Source.Upload || a.Name != "my-shop" || a.Build != "npm ci" || a.Start != "npm start" || a.Port != 3000 {
		t.Fatalf("%+v", a)
	}
	var d api.Deployment
	if j := h.wm("", "deployments", "get", "my-shop", "--json").must(t, 0); json.Unmarshal([]byte(j.stdout), &d) != nil {
		t.Fatalf("%s", j.stdout)
	}
	sent := h.api.DeployFiles(d.ID)
	if !slices.Contains(sent, "index.js") || !slices.Contains(sent, "lib/b.js") || slices.Contains(sent, ".env") || slices.Contains(sent, "node_modules/x/a.js") {
		t.Fatalf("%v", sent)
	}
	if !strings.Contains(r.stderr, "Left out: .env, node_modules/") || !strings.Contains(r.stderr, "Start: npm start") || !strings.Contains(r.stderr, "Building...") {
		t.Fatalf("%s", r.stderr)
	}
	if !strings.Contains(r.stdout, "live") || !strings.Contains(r.stdout, ".run.wheelmachine.ai") {
		t.Fatalf("%s", r.stdout)
	}
}

func TestDeployingTheFolderHereIsTheDefault(t *testing.T) {
	fastDeploy(t)
	h := newHarness(t)
	t.Chdir(aFolder(t))
	// (nobody here to be asked: the folder you happen to be in goes only when that is said)
	if r := h.wm("", "deploy", "-q").must(t, 2); !strings.Contains(r.stderr, "--yes") || len(h.api.DeployAsked()) != 0 {
		t.Fatalf("%s", r.stderr)
	}
	h.wm("", "deploy", "-q", "--yes").must(t, 0)
	if asked := h.api.DeployAsked(); len(asked) != 1 || !asked[0].Source.Upload {
		t.Fatalf("%+v", asked)
	}
}

func TestDeployingTwiceMakesOneMachine(t *testing.T) {
	fastDeploy(t)
	h := newHarness(t)
	dir := aFolder(t)
	t.Chdir(dir)
	h.wm("", "deploy", "--yes").must(t, 0)
	r := h.wm("", "deploy", "--yes").must(t, 2)
	if !strings.Contains(r.stderr, "my-shop") || !strings.Contains(r.stderr, "wm deployments redeploy dep_") || !strings.Contains(r.stderr, "wm deployments set dep_") || !strings.Contains(r.stderr, "--files") || !strings.Contains(r.stderr, "--name") {
		t.Fatalf("%s", r.stderr)
	}
	if asked := h.api.DeployAsked(); len(asked) != 1 {
		t.Fatalf("%+v", asked)
	}
	// and in any case, and of a repository or an address by the name it would get
	h.wm("", "deploy", "--yes", "--name", "MY-SHOP").must(t, 2)
	h.wm("", "deploy", "https://github.com/acme/shop.git").must(t, 0)
	if r := h.wm("", "deploy", "https://github.com/other/shop").must(t, 2); strings.Contains(r.stderr, "--files") {
		t.Fatalf("%s", r.stderr)
	}
	// another name is another deployment
	h.wm("", "deploy", "--yes", "--name", "second").must(t, 0)
	if asked := h.api.DeployAsked(); len(asked) != 3 {
		t.Fatalf("%+v", asked)
	}
}

// From the moment a deployment is made its id is said, whatever goes wrong next and whatever was asked, and trying again
// is pointed at it: never a second machine, charged, for the same app.
func TestADeploymentMadeIsNeverLost(t *testing.T) {
	fastDeploy(t)
	dir := aFolder(t)
	h := newHarness(t)
	h.api.Refuse = func(q *http.Request) *fakeapi.Refusal {
		if q.Method == http.MethodPut {
			return &fakeapi.Refusal{Status: 400, Code: "invalid_request", Detail: "the files are not a tar archive"}
		}
		return nil
	}
	for _, flags := range [][]string{{}, {"-q"}, {"--json"}} {
		r := h.wm("", append([]string{"deploy", dir, "--name", "shop-" + strconv.Itoa(len(flags)+len(strings.Join(flags, "")))}, flags...)...).must(t, 1)
		if !strings.Contains(r.stderr, "Made shop-") || !strings.Contains(r.stderr, "the files are not a tar archive") || !strings.Contains(r.stderr, "is made and waits for its files: wm deployments redeploy dep_") {
			t.Fatalf("%v: %s", flags, r.stderr)
		}
	}
	h.api.Refuse = nil
	made := len(h.api.DeployAsked())
	r := h.wm("", "deploy", dir, "--name", "shop-0").must(t, 2)
	if !strings.Contains(r.stderr, "wm deployments redeploy dep_") || len(h.api.DeployAsked()) != made {
		t.Fatalf("%s", r.stderr)
	}
	// what it says to do then works
	h.wm("", "deployments", "redeploy", "shop-0", "--files", dir).must(t, 0)

	// following it goes wrong
	h = newHarness(t)
	h.api.Refuse = func(q *http.Request) *fakeapi.Refusal {
		if q.Method == http.MethodGet && strings.HasPrefix(q.URL.Path, "/v1/deployments/dep_") {
			return &fakeapi.Refusal{Status: 400, Code: "invalid_request", Detail: "no"}
		}
		return nil
	}
	r = h.wm("", "deploy", "https://github.com/acme/shop").must(t, 1)
	if !strings.Contains(r.stderr, "is made: wm deployments get dep_") {
		t.Fatalf("%s", r.stderr)
	}
}

// Files wait for their machine, rather than go all the way to be refused; and once they have gone, nobody is told the run
// waits for them.
func TestFilesWaitForTheirMachine(t *testing.T) {
	fastDeploy(t)
	h := newHarness(t)
	h.api.Deploy.FilesAfter = 3
	r := h.wm("", "deploy", aFolder(t)).must(t, 0)
	if strings.Count(r.stderr, "Getting its machine ready") != 1 || strings.Contains(r.stderr, "Waiting for its files") || !strings.Contains(r.stderr, "Sending 3 files") {
		t.Fatalf("%s", r.stderr)
	}
	if !slices.Contains(h.api.DeployFiles(strings.TrimSpace(h.wm("", "deployments", "get", "my-shop", "-q").must(t, 0).stdout)), "index.js") {
		t.Fatal("nothing was sent")
	}
	// a machine that takes too long: told, with what to do
	h = newHarness(t)
	h.api.Deploy.FilesAfter = 1 << 30
	r = h.wm("", "deploy", aFolder(t), "--timeout", "5ms").must(t, 1)
	if !strings.Contains(r.stderr, "not ready yet") || !strings.Contains(r.stderr, "wm deployments redeploy dep_") {
		t.Fatalf("%s", r.stderr)
	}
	if sent := h.api.DeployFiles(strings.TrimSpace(h.wm("", "deployments", "get", "my-shop", "-q").must(t, 0).stdout)); len(sent) != 0 {
		t.Fatalf("files were sent: %v", sent)
	}
}

// What is not said is read from its files, each on its own: --start and --port say nothing of how it builds.
func TestABuildNotSaidIsRead(t *testing.T) {
	fastDeploy(t)
	h := newHarness(t)
	h.wm("", "deploy", "https://github.com/acme/shop", "--start", "go run .", "--port", "8080").must(t, 0)
	a := h.api.DeployAsked()[0]
	if a.Build != "npm ci" || a.Start != "go run ." || a.Port != 8080 {
		t.Fatalf("%+v", a)
	}
}

func TestWhatGoesIsShownFirstAndWhatStaysIsAlwaysSaid(t *testing.T) {
	fastDeploy(t)
	h := newHarness(t)
	dir := aFolder(t)
	r := h.wm("", "deploy", dir, "--name", "a").must(t, 0)
	if !strings.Contains(r.stderr, "What goes from "+dir+": 3 files, ") || !strings.Contains(r.stderr, "index.js, lib/ (1 file), package.json") {
		t.Fatalf("%s", r.stderr)
	}
	r = h.wm("", "deploy", dir, "--name", "b", "--json").must(t, 0)
	var d struct {
		ID      string   `json:"id"`
		LeftOut []string `json:"left_out"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &d); err != nil || !slices.Equal(d.LeftOut, []string{".env", "node_modules/"}) || d.ID == "" {
		t.Fatalf("%v %s", err, r.stdout)
	}
	r = h.wm("", "deploy", dir, "--name", "c", "-q").must(t, 0)
	if !strings.Contains(r.stderr, "Left out: .env, node_modules/") || !strings.HasPrefix(r.stdout, "dep_") || strings.Count(r.stdout, "\n") != 1 {
		t.Fatalf("%s %s", r.stdout, r.stderr)
	}
	// nothing goes, and nothing need be known of the account
	dry := newHarness(t)
	delete(dry.env, api.EnvAPIKey)
	r = dry.wm("", "deploy", dir, "--dry-run").must(t, 0)
	if !slices.Equal(lines(r.stdout), []string{"index.js", "lib/b.js", "package.json"}) || !strings.Contains(r.stderr, "Left out: .env, node_modules/") || len(dry.api.DeployAsked()) != 0 {
		t.Fatalf("%s\n%s", r.stdout, r.stderr)
	}
	r = dry.wm("", "deploy", dir, "--dry-run", "--json").must(t, 0)
	var what struct {
		Files   int      `json:"files"`
		Bytes   int64    `json:"bytes"`
		Paths   []string `json:"paths"`
		LeftOut []string `json:"left_out"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &what); err != nil || what.Files != 3 || len(what.Paths) != 3 || len(what.LeftOut) != 2 {
		t.Fatalf("%v %s", err, r.stdout)
	}
	dry.wm("", "deploy", "https://github.com/acme/shop", "--dry-run").must(t, 2)
}

func TestAFolderLeavesOutWhatItIsToldTo(t *testing.T) {
	fastDeploy(t)
	h := newHarness(t)
	dir := aFolder(t)
	for name, text := range map[string]string{".gitignore": "secrets.txt\ngenerated/\n", "secrets.txt": "S", "generated/a.js": "1", "old.bak": "1", "id_rsa": "K", ".npmrc": "T", "a.pem": "P"} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h.wm("", "deploy", dir, "--name", "a", "--exclude", "*.bak", "--exclude", "lib/").must(t, 0)
	sent := h.api.DeployFiles(strings.TrimSpace(h.wm("", "deployments", "get", "a", "-q").must(t, 0).stdout))
	for _, gone := range []string{"secrets.txt", "generated/a.js", "old.bak", "lib/b.js", "id_rsa", ".npmrc", "a.pem", ".env"} {
		if slices.Contains(sent, gone) {
			t.Errorf("%s went: %v", gone, sent)
		}
	}
	h.wm("", "deploy", dir, "--name", "b", "--no-gitignore").must(t, 0)
	sent = h.api.DeployFiles(strings.TrimSpace(h.wm("", "deployments", "get", "b", "-q").must(t, 0).stdout))
	if !slices.Contains(sent, "secrets.txt") || !slices.Contains(sent, "generated/a.js") || slices.Contains(sent, "id_rsa") {
		t.Fatalf("%v", sent)
	}
	// and redeploying goes by the same words
	h.wm("", "deployments", "redeploy", "a", "--files", dir, "--exclude", "index.js").must(t, 0)
	sent = h.api.DeployFiles(strings.TrimSpace(h.wm("", "deployments", "get", "a", "-q").must(t, 0).stdout))
	if slices.Contains(sent, "index.js") || !slices.Contains(sent, "lib/b.js") {
		t.Fatalf("%v", sent)
	}
	h.wm("", "deployments", "redeploy", "a", "--exclude", "index.js").must(t, 2)
	h.wm("", "deploy", "https://github.com/acme/shop", "--exclude", "x").must(t, 2)
}

func TestAHomeFolderOrAFolderOfSettingsIsNotSent(t *testing.T) {
	fastDeploy(t)
	h := newHarness(t)
	home := h.env["HOME"]
	settings := filepath.Join(h.env["XDG_CONFIG_HOME"], "wheelmachine")
	for _, dir := range []string{home, filepath.Join(home, ".ssh"), settings} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"deploy", dir, "--yes"}, {"deployments", "redeploy", "x", "--files", dir, "--yes"}, {"deploy", dir, "--dry-run"}} {
			if r := h.wm("", args...).must(t, 2); !strings.Contains(r.stderr, dir) {
				t.Fatalf("%v: %s", args, r.stderr)
			}
		}
	}
	t.Chdir(home)
	if r := h.wm("", "deploy", "--yes").must(t, 2); !strings.Contains(r.stderr, "home folder") {
		t.Fatalf("%s", r.stderr)
	}
	if asked := h.api.DeployAsked(); len(asked) != 0 {
		t.Fatalf("%+v", asked)
	}
}

func TestAFolderNamedByALinkIsSent(t *testing.T) {
	fastDeploy(t)
	h := newHarness(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(aFolder(t), link); err != nil {
		t.Fatal(err)
	}
	h.wm("", "deploy", link).must(t, 0)
	if !slices.Contains(h.api.DeployFiles(strings.TrimSpace(h.wm("", "deployments", "get", "link", "-q").must(t, 0).stdout)), "index.js") {
		t.Fatal("nothing went")
	}
}

// What an app wrote, or a repository suggests, goes to a terminal without what the terminal would act on.
func TestEscapeSequencesAreNotPassedOn(t *testing.T) {
	fastDeploy(t)
	h := newHarness(t)
	h.api.Deploy.Guess.Start = "npm start \x1b]52;c;QUJD\x07\x1b[31m!"
	h.api.Deploy.Log = "one\x1b[2J\r\ntwo\x1b]0;title\x07\x9b\tthree"
	r := h.wm("", "deploy", "https://github.com/acme/shop").must(t, 0)
	if strings.Contains(r.stderr, "\x1b") || !strings.Contains(r.stderr, "Start: npm start !") {
		t.Fatalf("%q", r.stderr)
	}
	r = h.wm("", "deployments", "logs", "shop").must(t, 0)
	if strings.Contains(r.stdout, "\x1b") || strings.Contains(r.stdout, "\x9b") || !strings.HasPrefix(r.stdout, "one\ntwo") || !strings.Contains(r.stdout, "\tthree\n") {
		t.Fatalf("%q", r.stdout)
	}
	// --json has it as it is, escaped by JSON
	r = h.wm("", "deployments", "logs", "shop", "--json").must(t, 0)
	if strings.Contains(r.stdout, "\x1b") || !strings.Contains(r.stdout, `\u001b[2J`) {
		t.Fatalf("%q", r.stdout)
	}
}

func TestASecretsValueIsNotSent(t *testing.T) {
	h := newHarness(t)
	r := h.wm("", "deploy", "https://github.com/acme/shop", "--secret", "KEY=hunter2").must(t, 2)
	if strings.Contains(r.stderr, "hunter2") || !strings.Contains(r.stderr, "not its value") {
		t.Fatalf("%s", r.stderr)
	}
	if _, asked := h.api.Last(http.MethodPost, "/v1/deployments"); asked {
		t.Fatal("it was sent")
	}
}

func TestDeployingARepositoryFindsItsConnection(t *testing.T) {
	fastDeploy(t)
	h := newHarness(t)
	h.api.Deploy.Repos = []api.DeployRepo{{Connection: "code_a", Repo: "acme/shop"}, {Connection: "code_a", Repo: "acme/both"}, {Connection: "code_b", Repo: "acme/both"}}
	h.wm("", "deploy", "acme/shop", "--branch", "main").must(t, 0)
	if a := h.api.DeployAsked()[0]; a.Source.Connection != "code_a" || a.Source.Repo != "acme/shop" || a.Branch != "main" {
		t.Fatalf("%+v", a)
	}
	r := h.wm("", "deploy", "acme/both").must(t, 2)
	if !strings.Contains(r.stderr, "code_a, code_b") {
		t.Fatalf("%s", r.stderr)
	}
	h.wm("", "deploy", "acme/both", "--connection", "code_b").must(t, 0)
	if a := h.api.DeployAsked()[1]; a.Source.Connection != "code_b" {
		t.Fatalf("%+v", a)
	}
	r = h.wm("", "deploy", "acme/none").must(t, 1)
	if !strings.Contains(r.stderr, "no repository acme/none") {
		t.Fatalf("%s", r.stderr)
	}
}

func TestDeployingAnAddressAsksNothingItWasTold(t *testing.T) {
	fastDeploy(t)
	h := newHarness(t)
	h.wm("", "deploy", "https://github.com/acme/shop", "--name", "shop", "--build", "", "--start", "go run .", "--port", "8080", "--cpus", "4",
		"--secret", "A,B", "--always-on", "--region", "helsinki").must(t, 0)
	a := h.api.DeployAsked()[0]
	if a.Source.URL != "https://github.com/acme/shop" || a.Name != "shop" || a.Build != "" || a.Start != "go run ." || a.Port != 8080 || a.CPUs != 4 ||
		!slices.Equal(a.Secrets, []string{"A", "B"}) || !a.KeepAwake || a.Region != "helsinki" {
		t.Fatalf("%+v", a)
	}
	if _, asked := h.api.Last(http.MethodPost, "/v1/deployments/detect"); asked {
		t.Fatal("it was told how it builds and starts")
	}
}

func TestDeployThatCannotReadHowItStartsAsks(t *testing.T) {
	h := newHarness(t)
	h.api.Deploy.Guess.Start = ""
	r := h.wm("", "deploy", "https://github.com/acme/shop").must(t, 2)
	if !strings.Contains(r.stderr, "--start and --port") || len(h.api.DeployAsked()) != 0 {
		t.Fatalf("%s", r.stderr)
	}
}

func TestDeployWordsItCannotUse(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{
		{"deploy", "./no-such"},
		{"deploy", "shop"},
		{"deploy", "a", "b"},
		{"deploy", "https://github.com/acme/shop", "--connection", "code_a"},
		{"deploy", aFolder(t), "--branch", "main"},
		{"deploy", "https://github.com/acme/shop", "--port", "70000"},
		{"deployments", "logs", "x", "--kind", "other"},
		{"deployments", "set", "x"},
		{"deployments", "set", "x", "--port", "0"},
		{"deployments", "no-such"},
	} {
		h.wm("", args...).must(t, 2)
	}
	if len(h.api.DeployAsked()) != 0 {
		t.Fatal("something was made")
	}
}

func TestADeployThatFailsSaysWhyAndExitsSo(t *testing.T) {
	fastDeploy(t)
	h := newHarness(t)
	h.api.Deploy.Fails = "the build ended with exit code 1"
	r := h.wm("", "deploy", "https://github.com/acme/shop").must(t, 1)
	if !strings.Contains(r.stdout, "failed") || !strings.Contains(r.stdout, "the build ended with exit code 1") || !strings.Contains(r.stderr, "wm deployments logs dep_") {
		t.Fatalf("%s\n%s", r.stdout, r.stderr)
	}
}

func TestDeployWithoutWaitingAnswersAtOnce(t *testing.T) {
	h := newHarness(t)
	h.api.Deploy.Stuck = true
	r := h.wm("", "deploy", "https://github.com/acme/shop", "--no-wait", "--json").must(t, 0)
	var d api.Deployment
	if json.Unmarshal([]byte(r.stdout), &d) != nil || d.State != api.DeployBuilding || r.stderr != "" && strings.Contains(r.stderr, "Fetching") {
		t.Fatalf("%s %s", r.stdout, r.stderr)
	}
	// and one given a time says it is still going, with how to follow it
	fastDeploy(t)
	r = h.wm("", "deploy", "https://github.com/acme/other", "--timeout", "5ms").must(t, 1)
	if !strings.Contains(r.stderr, "still deploying") {
		t.Fatalf("%s", r.stderr)
	}
}

func TestDeploymentsAreListedReadChangedAndDeleted(t *testing.T) {
	fastDeploy(t)
	h := newHarness(t)
	h.api.Deploy.Log = "built\n"
	h.wm("", "deploy", "https://github.com/acme/shop", "--name", "shop").must(t, 0)
	h.wm("", "deploy", "https://github.com/acme/blog", "--name", "blog").must(t, 0)
	got := lines(h.wm("", "deployments", "ls").must(t, 0).stdout)
	if len(got) != 3 || !strings.HasPrefix(got[0], "ID") || !strings.Contains(got[1], "blog") || !strings.Contains(got[2], "shop") {
		t.Fatalf("%q", got)
	}
	ids := lines(h.wm("", "deployments", "ls", "-q").must(t, 0).stdout)
	if len(ids) != 2 || !strings.HasPrefix(ids[0], "dep_") {
		t.Fatalf("%q", ids)
	}
	var list []api.Deployment
	if r := h.wm("", "deployments", "ls", "--json").must(t, 0); json.Unmarshal([]byte(r.stdout), &list) != nil || len(list) != 2 {
		t.Fatalf("%s", r.stdout)
	}
	if r := h.wm("", "deployments", "get", "shop", "-q").must(t, 0); r.stdout != ids[1]+"\n" {
		t.Fatalf("%q", r.stdout)
	}
	if r := h.wm("", "deployments", "get", "no-such").must(t, 1); !strings.Contains(r.stderr, "no-such") {
		t.Fatalf("%s", r.stderr)
	}
	if r := h.wm("", "deployments", "logs", "shop").must(t, 0); r.stdout != "built\n" {
		t.Fatalf("%q", r.stdout)
	}
	if _, ok := h.api.Last(http.MethodGet, "/v1/deployments/"+ids[1]+"/logs"); !ok {
		t.Fatal("no log was asked for")
	}
	r := h.wm("", "deployments", "redeploy", "shop").must(t, 0)
	if !strings.Contains(r.stdout, "live") {
		t.Fatalf("%s", r.stdout)
	}
	runs := lines(h.wm("", "deployments", "runs", "shop").must(t, 0).stdout)
	if len(runs) != 3 || !strings.Contains(runs[1], "redeploy") || !strings.Contains(runs[2], "first") {
		t.Fatalf("%q", runs)
	}
	var d api.Deployment
	if r := h.wm("", "deployments", "set", "shop", "--port", "9000", "--always-on", "--json").must(t, 0); json.Unmarshal([]byte(r.stdout), &d) != nil || d.Port != 9000 || !d.KeepAwake {
		t.Fatalf("%s", r.stdout)
	}
	if r := h.wm("", "deployments", "set", "shop", "--always-on=false", "--json").must(t, 0); json.Unmarshal([]byte(r.stdout), &d) != nil || d.Port != 9000 || d.KeepAwake {
		t.Fatalf("%s", r.stdout)
	}
	// deleted only once it is said to be
	h.wm("no\n", "deployments", "rm", "shop").must(t, 2)
	if got := lines(h.wm("", "deployments", "ls", "-q").must(t, 0).stdout); len(got) != 2 {
		t.Fatalf("%q", got)
	}
	if r := h.wm("", "deployments", "rm", "--yes", "shop", "blog").must(t, 0); len(lines(r.stdout)) != 2 {
		t.Fatalf("%q", r.stdout)
	}
	if r := h.wm("", "deployments", "ls").must(t, 0); len(lines(r.stdout)) != 1 {
		t.Fatalf("%q", r.stdout)
	}
}

func TestRedeployingSendsNewFiles(t *testing.T) {
	fastDeploy(t)
	h := newHarness(t)
	dir := aFolder(t)
	h.wm("", "deploy", dir, "--name", "shop").must(t, 0)
	if err := os.WriteFile(filepath.Join(dir, "new.js"), []byte("4"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.wm("", "deployments", "redeploy", "shop", "--files", dir).must(t, 0)
	id := strings.TrimSpace(h.wm("", "deployments", "get", "shop", "-q").must(t, 0).stdout)
	if !slices.Contains(h.api.DeployFiles(id), "new.js") {
		t.Fatalf("%v", h.api.DeployFiles(id))
	}
}

func TestTheRepositoriesThatCanBeDeployedAreListed(t *testing.T) {
	h := newHarness(t)
	h.api.Deploy.Repos = []api.DeployRepo{{Connection: "code_a", Provider: "github", Repo: "acme/shop", Shared: true}, {Connection: "code_b", Provider: "gitlab", Repo: "acme/blog"}}
	got := lines(h.wm("", "deployments", "repos").must(t, 0).stdout)
	if len(got) != 3 || !strings.Contains(got[1], "acme/shop") || !strings.HasSuffix(got[1], "yes") || !strings.HasSuffix(got[2], "no") {
		t.Fatalf("%q", got)
	}
	if got := lines(h.wm("", "deployments", "repos", "--connection", "code_b", "-q").must(t, 0).stdout); len(got) != 1 || got[0] != "acme/blog" {
		t.Fatalf("%q", got)
	}
}
