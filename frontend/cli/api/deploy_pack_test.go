package api

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, text := range files {
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

// What an archive holds: its files' texts by path, its folders with a text of "/", and its modes.
func unpack(t *testing.T, p *PackedFolder) (map[string]string, map[string]int64) {
	t.Helper()
	zipped, err := gzip.NewReader(p.File)
	if err != nil {
		t.Fatal(err)
	}
	found, modes := map[string]string{}, map[string]int64{}
	archive := tar.NewReader(zipped)
	for {
		head, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return found, modes
		}
		if err != nil {
			t.Fatal(err)
		}
		modes[head.Name] = head.Mode
		if head.Typeflag == tar.TypeDir {
			found[head.Name] = "/"
			continue
		}
		text, err := io.ReadAll(archive)
		if err != nil {
			t.Fatal(err)
		}
		found[head.Name] = string(text)
	}
}

func TestAFolderIsPackedWithoutWhatStaysHere(t *testing.T) {
	dir := writeTree(t, map[string]string{"run.sh": "#!/bin/sh\n", "src/a.txt": "a", ".env": "S=1", ".env.local": "S=2", "src/.env": "S=3",
		".git/config": "x", "node_modules/m/i.js": "x", "src/node_modules/n.js": "x", ".venv/bin/p": "x", "venv/bin/p": "x", "src/__pycache__/a.pyc": "x",
		"environment.js": "kept", "env/a.txt": "kept"})
	if err := os.Chmod(filepath.Join(dir, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("run.sh", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	p, err := PackFolder(dir, FolderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	found, modes := unpack(t, p)
	want := map[string]string{"run.sh": "#!/bin/sh\n", "src/": "/", "src/a.txt": "a", "environment.js": "kept", "env/": "/", "env/a.txt": "kept"}
	if len(found) != len(want) {
		t.Fatalf("%v", found)
	}
	for name, text := range want {
		if found[name] != text {
			t.Fatalf("%s: %q in %v", name, found[name], found)
		}
	}
	if modes["run.sh"] != 0o755 || modes["src/a.txt"] != 0o644 {
		t.Fatalf("%v", modes)
	}
	if p.Files != 4 {
		t.Fatalf("%d files", p.Files)
	}
	if out := []string{".env", ".env.local", ".git/", ".venv/", "link", "node_modules/", "src/.env", "src/__pycache__/", "src/node_modules/", "venv/"}; !slices.Equal(p.LeftOut, out) {
		t.Fatalf("%v", p.LeftOut)
	}
	if info, err := os.Stat(p.File.Name()); err != nil || info.Size() != p.Size {
		t.Fatalf("%v %v", info, err)
	}
	name := p.File.Name()
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Fatal("the archive is still there")
	}
}

func TestAFolderOverWhatTheAPITakesIsToldBeforeAnythingIsSent(t *testing.T) {
	for what, c := range map[string]struct {
		files map[string]string
		says  string
	}{
		"nothing":    {map[string]string{".env": "S=1"}, "has no files"},
		"long path":  {map[string]string{strings.Repeat(strings.Repeat("a", 30)+"/", 9) + "c": "x"}, "at most 255 bytes"},
		"deep":       {map[string]string{strings.Repeat("a/", 33) + "c": "x"}, "folders deep"},
		"bad letter": {map[string]string{"a\tb": "x"}, "no control characters"},
		"backslash":  {map[string]string{`a\b`: "x"}, "no control characters"},
	} {
		t.Run(what, func(t *testing.T) {
			if _, err := PackFolder(writeTree(t, c.files), FolderOptions{}); err == nil || !strings.Contains(err.Error(), c.says) {
				t.Fatalf("%v", err)
			}
		})
	}
	if _, err := PackFolder(filepath.Join(t.TempDir(), "no-such"), FolderOptions{}); err == nil {
		t.Fatal("no such folder")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PackFolder(file, FolderOptions{}); err == nil || !strings.Contains(err.Error(), "is not a folder") {
		t.Fatalf("%v", err)
	}
}

func TestAFolderTooBigIsToldItsLimits(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(deployFileMost + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := PackFolder(dir, FolderOptions{}); err == nil || !strings.Contains(err.Error(), "at most 100 MB") {
		t.Fatalf("%v", err)
	}
	if err := os.Remove(big); err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		f, err := os.Create(filepath.Join(dir, "part"+string(rune('a'+i))))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(deployFileMost); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	if _, err := PackFolder(dir, FolderOptions{}); err == nil || !strings.Contains(err.Error(), "at most 500 MB") {
		t.Fatalf("%v", err)
	}
}

func TestAFolderSaysHowItBuildsByTheFilesAtItsTop(t *testing.T) {
	dir := writeTree(t, map[string]string{"package.json": `{"name":"x"}`, "yarn.lock": "long", "src/go.mod": "not at its top", "README.md": "not read",
		"Cargo.toml": strings.Repeat("x", deployDetectMost+1)})
	ask := FolderDetect(dir, FolderOptions{})
	if ask.Source != nil || len(ask.Files) != 3 {
		t.Fatalf("%+v", ask)
	}
	if text := ask.Files["package.json"]; text == nil || *text != `{"name":"x"}` {
		t.Fatalf("%v", text)
	}
	for _, name := range []string{"yarn.lock", "Cargo.toml"} {
		if text, there := ask.Files[name]; !there || text != nil {
			t.Fatalf("%s: %v %v", name, text, there)
		}
	}
}

func TestAFoldersNameIsOneAMachineCanTake(t *testing.T) {
	for dir, want := range map[string]string{"/x/My Shop!": "my-shop", "/x/shop": "shop", "/x/---": "", "/x/" + strings.Repeat("a", 50): strings.Repeat("a", 40)} {
		if got := FolderName(dir); got != want {
			t.Fatalf("%s: %q", dir, got)
		}
	}
}

// The cases every packer shares: testdata/deploy_left_out.json.
type leftOutCases struct {
	Gitignores map[string]string `json:"gitignores"`
	Files      []string          `json:"files"`
	Goes       []string          `json:"goes"`
	LeftOut    []string          `json:"left_out"`
	Exclude    struct {
		Patterns []string `json:"patterns"`
		Files    []string `json:"files"`
		Goes     []string `json:"goes"`
		LeftOut  []string `json:"left_out"`
	} `json:"exclude"`
	NoGitignore struct {
		Goes []string `json:"goes"`
	} `json:"no_gitignore"`
	Patterns [][]any `json:"patterns"`
}

func readLeftOutCases(t *testing.T) leftOutCases {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "deploy_left_out.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c leftOutCases
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func (c leftOutCases) tree(t *testing.T, more []string) string {
	t.Helper()
	files := map[string]string{}
	for _, name := range append(slices.Clone(c.Files), more...) {
		files[name] = "x"
	}
	for name, text := range c.Gitignores {
		files[name] = text
	}
	return writeTree(t, files)
}

func packedPaths(t *testing.T, dir string, o FolderOptions) ([]string, []string) {
	t.Helper()
	p, err := PackFolder(dir, o)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	found, _ := unpack(t, p)
	var went []string
	for name := range found {
		if !strings.HasSuffix(name, "/") {
			went = append(went, name)
		}
	}
	slices.Sort(went)
	if !slices.Equal(went, p.Paths) {
		t.Fatalf("it says %v and holds %v", p.Paths, went)
	}
	return went, p.LeftOut
}

func sortedWith(a []string, b ...string) []string {
	out := append(slices.Clone(a), b...)
	slices.Sort(out)
	return out
}

func TestAPackerGoesByTheCasesItShares(t *testing.T) {
	c := readLeftOutCases(t)
	went, left := packedPaths(t, c.tree(t, nil), FolderOptions{})
	if !slices.Equal(went, c.Goes) {
		t.Errorf("went: %v\nwant: %v", went, c.Goes)
	}
	if !slices.Equal(left, c.LeftOut) {
		t.Errorf("left out: %v\nwant: %v", left, c.LeftOut)
	}
	went, left = packedPaths(t, c.tree(t, c.Exclude.Files), FolderOptions{Exclude: c.Exclude.Patterns})
	goes := sortedWith(c.Goes, c.Exclude.Goes...)
	goes = slices.DeleteFunc(goes, func(s string) bool { return slices.Contains(c.Exclude.LeftOut, s) })
	if !slices.Equal(went, goes) {
		t.Errorf("went with exclude: %v\nwant: %v", went, goes)
	}
	if want := sortedWith(c.LeftOut, c.Exclude.LeftOut...); !slices.Equal(left, want) {
		t.Errorf("left out with exclude: %v\nwant: %v", left, want)
	}
	// without its .gitignore files, what they named goes, and nothing that stays whatever is said
	went, _ = packedPaths(t, c.tree(t, nil), FolderOptions{NoGitignore: true})
	if want := sortedWith(c.Goes, c.NoGitignore.Goes...); !slices.Equal(went, want) {
		t.Errorf("went without .gitignore: %v\nwant: %v", went, want)
	}
}

func TestAGitignoreLineMatchesAsItDoesInEveryPacker(t *testing.T) {
	for _, row := range readLeftOutCases(t).Patterns {
		pattern, path, isDir, want := row[0].(string), row[1].(string), row[2].(bool), row[3].(bool)
		if got := ignoredBy(parseIgnore("", pattern), path, isDir); got != want {
			t.Errorf("%q on %q (folder %v): %v", pattern, path, isDir, got)
		}
	}
}

// A .gitignore of a folder down the tree speaks only of what is in there; and one that is a link is not read.
func TestAGitignoreIsNotFollowedOutOfTheFolder(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(outside, []byte("a.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := writeTree(t, map[string]string{"a.txt": "x"})
	if err := os.Symlink(outside, filepath.Join(dir, ".gitignore")); err != nil {
		t.Fatal(err)
	}
	went, left := packedPaths(t, dir, FolderOptions{})
	if !slices.Equal(went, []string{"a.txt"}) || !slices.Equal(left, []string{".gitignore"}) {
		t.Fatalf("%v %v", went, left)
	}
}

// What says how a folder builds is read from the folder alone: a link to anywhere is not followed, and what stays here is
// not read for it.
func TestDetectFollowsNoLinkOutOfTheFolder(t *testing.T) {
	planted := filepath.Join(t.TempDir(), "environ")
	if err := os.WriteFile(planted, []byte("web: WHEELMACHINE_API_KEY=planted"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := writeTree(t, map[string]string{"package.json": `{"name":"x"}`, "go.mod": "module x", ".gitignore": "go.mod\n"})
	for _, name := range []string{"Procfile", "requirements.txt", "index.html"} {
		if err := os.Symlink(planted, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	ask := FolderDetect(dir, FolderOptions{})
	if len(ask.Files) != 1 || ask.Files["package.json"] == nil {
		t.Fatalf("%v", ask.Files)
	}
	if ask = FolderDetect(dir, FolderOptions{NoGitignore: true}); len(ask.Files) != 2 || ask.Files["go.mod"] == nil {
		t.Fatalf("%v", ask.Files)
	}
	if ask = FolderDetect(dir, FolderOptions{Exclude: []string{"package.json"}}); len(ask.Files) != 0 {
		t.Fatalf("%v", ask.Files)
	}
}

func TestAFolderNamedByALinkIsThatFolder(t *testing.T) {
	dir := writeTree(t, map[string]string{"package.json": "{}"})
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if went, _ := packedPaths(t, link, FolderOptions{}); !slices.Equal(went, []string{"package.json"}) {
		t.Fatalf("%v", went)
	}
	if ask := FolderDetect(link, FolderOptions{}); ask.Files["package.json"] == nil {
		t.Fatalf("%v", ask.Files)
	}
}

// The folder temporary files go in can be sent, without the archive being made there.
func TestTheArchiveIsNotPackedIntoItself(t *testing.T) {
	dir := writeTree(t, map[string]string{"a.txt": "x"})
	t.Setenv("TMPDIR", dir)
	if went, _ := packedPaths(t, dir, FolderOptions{}); !slices.Equal(went, []string{"a.txt"}) {
		t.Fatalf("%v", went)
	}
}

func TestAFolderUnreadableIsSaidNotLeftBehindSilently(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	dir := writeTree(t, map[string]string{"a.txt": "x", "shut/b.txt": "x"})
	if err := os.Chmod(filepath.Join(dir, "shut"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "shut"), 0o755) })
	if _, err := PackFolder(dir, FolderOptions{}); err == nil || !strings.Contains(err.Error(), "shut") {
		t.Fatalf("%v", err)
	}
}

func TestAHomeFolderAndAFolderOfSettingsAreRefused(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "home", "me")
	settings := filepath.Join(home, ".config", "wheelmachine")
	project := filepath.Join(home, "shop")
	configs := filepath.Join(base, "elsewhere", "conf")
	for _, dir := range []string{settings, project, filepath.Join(home, ".ssh"), filepath.Join(home, "Code", ".aws", "x"), filepath.Join(configs, "wheelmachine"), filepath.Join(base, "holds", "conf", "wheelmachine")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(project, "up")
	if err := os.Symlink(home, link); err != nil {
		t.Fatal(err)
	}
	env := func(k string) string { return map[string]string{"HOME": home}[k] }
	for dir, refused := range map[string]bool{
		home: true, filepath.Dir(home): true, base: true, "/": true, link: true,
		settings: true, filepath.Join(home, ".config"): true, filepath.Join(home, ".ssh"): true, filepath.Join(home, "Code", ".aws", "x"): true,
		project: false,
	} {
		if err := FolderRefused(dir, env); (err != nil) != refused {
			t.Errorf("%s: %v", dir, err)
		}
	}
	// wm's settings wherever they are said to be: the folder they are in, and one that holds them
	env = func(k string) string {
		return map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(base, "holds", "conf")}[k]
	}
	for dir, refused := range map[string]bool{filepath.Join(base, "holds", "conf", "wheelmachine"): true, filepath.Join(base, "holds"): true, project: false} {
		if err := FolderRefused(dir, env); (err != nil) != refused {
			t.Errorf("%s: %v", dir, err)
		}
	}
	if err := FolderRefused(filepath.Join(base, "no-such"), env); err == nil {
		t.Error("no such folder")
	}
}

func TestANameIsTheOneTheAPIGives(t *testing.T) {
	for _, c := range [][3]string{{"acme/shop", "", "shop"}, {"acme/Shop.git", "", "shop"}, {"", "https://github.com/acme/my_app.git", "my-app"}, {"", "https://github.com/acme/shop/", "shop"}, {"", "", "app"}} {
		if got := SourceName(c[0], c[1]); got != c[2] {
			t.Errorf("%v: %q", c, got)
		}
	}
}
