package api

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// As much as the API takes of a deployment's files. A folder over them is told so here, before anything is sent.
const (
	deployFilesMost = 20_000
	deployFileMost  = 100_000_000
	deployAllMost   = 500_000_000
	deployPathMost  = 255
	deployDepthMost = 32
	// the most of a file read to say how the folder builds and starts
	deployDetectMost = 64 << 10
)

// What stays on this computer, wherever it is in the folder and in any case: a file or a folder of one of these names, or
// a name beginning or ending so. Another copy of the code's history and what its build makes again, and keys, tokens and
// a person's settings: a deployment's secrets are given by name. The Python library and the dashboard's packer hold the
// same lists, and frontend/docs/tests holds the three alike.
var (
	deployLeftOutNames = []string{".aws", ".azure", ".bash_history", ".boto", ".cache", ".config", ".docker", ".dockercfg", ".gcloud", ".git",
		".git-credentials", ".gitconfig", ".gnupg", ".hg", ".htpasswd", ".kube", ".local", ".my.cnf", ".mysql_history", ".netrc",
		".node_repl_history", ".npmrc", ".pgpass", ".psql_history", ".pypirc", ".python_history", ".s3cfg", ".ssh", ".svn", ".terraform",
		".vault-token", ".venv", ".wheelmachine", ".zsh_history", "__pycache__", "_netrc", "credentials.json", "credentials.toml",
		"node_modules", "secrets.json", "secrets.toml", "secrets.yaml", "secrets.yml", "service-account.json", "serviceaccountkey.json", "terraform.tfvars", "venv"}
	deployLeftOutPrefixes = []string{".env", "id_dsa", "id_ecdsa", "id_ed25519", "id_rsa"}
	deployLeftOutSuffixes = []string{".env", ".jks", ".kdbx", ".key", ".keystore", ".p12", ".pem", ".pfx", ".ppk", ".tfstate", ".tfstate.backup", ".tfvars"}
)

// Whether a file or a folder of this name stays here, whatever else is said.
func deployNameLeftOut(name string) bool {
	low := strings.ToLower(name)
	if slices.Contains(deployLeftOutNames, low) {
		return true
	}
	has := func(list []string, test func(s, part string) bool) bool {
		return slices.ContainsFunc(list, func(part string) bool { return test(low, part) })
	}
	return has(deployLeftOutPrefixes, strings.HasPrefix) || has(deployLeftOutSuffixes, strings.HasSuffix)
}

// The most of a .gitignore read: one longer is no list of paths a person wrote.
const deployIgnoreMost = 1 << 20

// FolderOptions says what else of a folder stays here.
type FolderOptions struct {
	Exclude     []string // patterns, written as in a .gitignore, for the folder's top
	NoGitignore bool     // what the folder's .gitignore files name goes too (never what stays here whatever is said)
}

// What of a folder stays here: its rules, as they are read on the way in.
type folderRules struct {
	root    string
	exclude []ignoreRule
	ignore  []ignoreRule
	use     bool
}

func newFolderRules(root string, o FolderOptions) *folderRules {
	r := &folderRules{root: root, use: !o.NoGitignore}
	for _, pattern := range o.Exclude {
		r.exclude = append(r.exclude, parseIgnore("", strings.TrimPrefix(pattern, "./"))...)
	}
	return r
}

// Reads a folder's .gitignore, as it is gone into: name is the folder's path in the folder sent, "" for its top. One that
// is a link is not read: it could be anything.
func (r *folderRules) enter(name string) {
	if !r.use {
		return
	}
	base := ""
	if name != "" {
		base = name + "/"
	}
	path := filepath.Join(r.root, filepath.FromSlash(base), ".gitignore")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > deployIgnoreMost {
		return
	}
	if text, err := readRegular(path, info, deployIgnoreMost); err == nil {
		r.ignore = append(r.ignore, parseIgnore(base, text)...)
	}
}

// Whether a file or a folder stays here, by its path in the folder sent.
func (r *folderRules) leftOut(name string, isDir bool) bool {
	return deployNameLeftOut(name[strings.LastIndexByte(name, '/')+1:]) || ignoredBy(r.exclude, name, isDir) || ignoredBy(r.ignore, name, isDir)
}

// A file read whole, up to most bytes, if it is still the regular file that was looked at: not one put in its place, or a
// link to somewhere else.
func readRegular(path string, seen os.FileInfo, most int64) (string, error) {
	f, err := openRegular(path, seen)
	if err != nil {
		return "", err
	}
	defer f.Close()
	text, err := io.ReadAll(io.LimitReader(f, most))
	return string(text), err
}

func openRegular(path string, seen os.FileInfo) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	now, err := f.Stat()
	if err == nil && (!now.Mode().IsRegular() || !os.SameFile(seen, now)) {
		err = fmt.Errorf("%s changed while it was being read", path)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// FolderRefused says why a folder must not be sent whole, nil for one that can be: the home folder and anything above it,
// which hold everything a person has, and a folder that is, or is inside, one of the names that stay here, or that holds
// wm's settings. A link is the folder it leads to. getenv is os.Getenv outside tests.
func FolderRefused(dir string, getenv func(string) string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if abs, err = filepath.EvalSymlinks(abs); err != nil {
		return err
	}
	home := getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if home == "" {
		// (a program started with little in its environment still has a home)
		if me, err := user.Current(); err == nil {
			home = me.HomeDir
		}
	}
	if home != "" {
		if resolved, err := filepath.EvalSymlinks(home); err == nil {
			home = resolved
		}
		if IsInside(abs, home) {
			return fmt.Errorf("%s holds your home folder, with all you keep there: say which folder of it to send", abs)
		}
	} else if filepath.Dir(abs) == abs {
		return fmt.Errorf("%s is everything on this computer: say which folder to send", abs)
	}
	for _, step := range strings.Split(filepath.ToSlash(abs), "/") {
		if step != "" && deployNameLeftOut(step) {
			return fmt.Errorf("%s is in %s, which stays on this computer", abs, step)
		}
	}
	if path, err := ConfigPath(getenv); err == nil {
		settings := filepath.Dir(path)
		if resolved, err := filepath.EvalSymlinks(settings); err == nil {
			settings = resolved
		}
		if IsInside(abs, settings) || IsInside(settings, abs) {
			return fmt.Errorf("%s holds wm's settings, with its API key: say another folder", abs)
		}
	}
	return nil
}

// IsInside says whether path is folder, or inside it. Neither is followed through links: EvalSymlinks both first.
func IsInside(folder, path string) bool {
	rel, err := filepath.Rel(folder, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// PackedFolder is a folder packed to send as a deployment's files: a gzipped tar archive in a temporary file. Close
// removes it.
type PackedFolder struct {
	File    *os.File // read from its start
	Size    int64    // of the archive
	Files   int
	Bytes   int64    // of the files in it
	Paths   []string // what is in it, by its path in the folder: its files, sorted
	LeftOut []string // what was not packed, by its path in the folder; a folder ends in a slash
}

// Close removes the archive.
func (p *PackedFolder) Close() error {
	err := p.File.Close()
	if rerr := os.Remove(p.File.Name()); err == nil {
		err = rerr
	}
	return err
}

// PackFolder packs a folder to send as a deployment's files. Left out, each named in LeftOut, are the names that stay here
// whatever is said (deployLeftOutNames and the others), what its .gitignore files and the options name, and links and
// anything else that is not a file or a folder, which the API does not take. dir may be a link to the folder.
func PackFolder(dir string, o FolderOptions) (*PackedFolder, error) {
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", dir)
	}
	tmp, err := os.CreateTemp("", "wm-deploy-*.tar.gz")
	if err != nil {
		return nil, err
	}
	p := &PackedFolder{File: tmp}
	if err := p.write(root, o); err != nil {
		_ = p.Close()
		return nil, err
	}
	end, err := tmp.Seek(0, io.SeekEnd)
	if err == nil {
		_, err = tmp.Seek(0, io.SeekStart)
	}
	if err != nil {
		_ = p.Close()
		return nil, err
	}
	p.Size = end
	sort.Strings(p.LeftOut)
	sort.Strings(p.Paths)
	return p, nil
}

func (p *PackedFolder) write(dir string, o FolderOptions) error {
	zipped := gzip.NewWriter(p.File)
	archive := tar.NewWriter(zipped)
	rules := newFolderRules(dir, o)
	// (the archive itself, should the folder sent be the one temporary files go in)
	own, err := p.File.Stat()
	if err != nil {
		return err
	}
	err = filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			rules.enter("")
			return nil
		}
		name := filepath.ToSlash(rel)
		switch {
		case e.IsDir() && rules.leftOut(name, true):
			p.LeftOut = append(p.LeftOut, name+"/")
			return fs.SkipDir
		case !e.IsDir() && (rules.leftOut(name, false) || !e.Type().IsRegular()):
			p.LeftOut = append(p.LeftOut, name)
			return nil
		}
		if strings.ContainsFunc(name, func(c rune) bool { return c < ' ' || c == 0x7f || c == '\\' }) {
			return fmt.Errorf("%q: a path has no control characters and no backslashes", name)
		}
		if len(name) > deployPathMost {
			return fmt.Errorf("%s: a path is at most %d bytes", name, deployPathMost)
		}
		if strings.Count(name, "/") >= deployDepthMost {
			return fmt.Errorf("%s: at most %d folders deep", name, deployDepthMost)
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if e.IsDir() {
			rules.enter(name)
			return archive.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: name + "/", Mode: 0o755, ModTime: info.ModTime()})
		}
		if os.SameFile(info, own) {
			return nil
		}
		p.Files++
		if p.Files > deployFilesMost {
			return fmt.Errorf("at most %d files can be sent: use a repository, or send less", deployFilesMost)
		}
		if info.Size() > deployFileMost {
			return fmt.Errorf("%s: a file is at most 100 MB", name)
		}
		if p.Bytes += info.Size(); p.Bytes > deployAllMost {
			return errors.New("at most 500 MB can be sent: use a repository, or send less")
		}
		p.Paths = append(p.Paths, name)
		mode := int64(0o644)
		if info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		if err := archive.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Size: info.Size(), Mode: mode, ModTime: info.ModTime()}); err != nil {
			return err
		}
		f, err := openRegular(path, info)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := io.CopyN(archive, f, info.Size()); err != nil {
			return fmt.Errorf("%s changed while it was being packed: %w", name, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if p.Files == 0 {
		return fmt.Errorf("%s has no files to send", dir)
	}
	if err := archive.Close(); err != nil {
		return err
	}
	return zipped.Close()
}

// The files at a folder's top that say how it builds and starts, as the API reads them by name; and those whose contents
// it reads. The rest it needs only to know are there.
var (
	deployDetectNames = []string{"package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "requirements.txt", "pyproject.toml",
		"uv.lock", "Pipfile", "manage.py", "main.py", "app.py", "Procfile", "go.mod", "Cargo.toml", "index.html", "Dockerfile"}
	deployDetectRead = map[string]bool{"package.json": true, "requirements.txt": true, "pyproject.toml": true, "Pipfile": true, "Procfile": true,
		"go.mod": true, "Cargo.toml": true}
)

// FolderDetect is what to ask DetectDeployment of a folder about to be sent: what goes of those files, and nothing a link
// leads to, which could be anywhere.
func FolderDetect(dir string, o FolderOptions) DeployDetect {
	files := map[string]*string{}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return DeployDetect{Files: files}
	}
	rules := newFolderRules(root, o)
	rules.enter("")
	for _, name := range deployDetectNames {
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || rules.leftOut(name, false) {
			continue
		}
		files[name] = nil
		if deployDetectRead[name] && info.Size() <= deployDetectMost {
			if text, err := readRegular(path, info, deployDetectMost); err == nil {
				files[name] = Ptr(text)
			}
		}
	}
	return DeployDetect{Files: files}
}

// FolderName is a name for a deployment of a folder: the folder's, in the shape a machine's name takes; "" for one that
// has none.
func FolderName(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	return deployNameShape(filepath.Base(abs))
}

// The name the API gives a deployment made of a repository or an address, when none is said: its last part, in the shape
// a machine's name takes ("app" for one with nothing to go by).
func SourceName(repo, address string) string {
	from := strings.Trim(strings.TrimSpace(repo), "/")
	if address != "" {
		from = strings.TrimRight(strings.TrimSpace(address), "/")
	}
	name := deployNameShape(strings.TrimSuffix(from[strings.LastIndexByte(from, '/')+1:], ".git"))
	if name == "" {
		return "app"
	}
	return name
}

func deployNameShape(s string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(s) {
		switch {
		case c >= 'a' && c <= 'z' || c >= '0' && c <= '9':
			b.WriteRune(c)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteRune('-')
		}
	}
	name := strings.Trim(b.String(), "-")
	if len(name) > 40 {
		name = strings.Trim(name[:40], "-")
	}
	return name
}
