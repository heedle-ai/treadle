package treadle

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/heedle-ai/pirn"
	"github.com/heedle-ai/warp"
)

// Source is where an imported skill came from: a folder in a Git
// repository.
type Source struct {
	Repo string `json:"repo"`           // an https:// or file:// URL
	Ref  string `json:"ref,omitempty"`  // a branch or tag; empty means the default branch
	Path string `json:"path,omitempty"` // the skill's folder in the repository; empty means its root
	// Commit is the exact commit the skill was read from, set by Fetch.
	Commit string `json:"commit,omitempty"`
}

// Trailers name a version's source on its commit.
const (
	TrailerSource       = "Skill-Source"
	TrailerSourceRef    = "Skill-Source-Ref"
	TrailerSourcePath   = "Skill-Source-Path"
	TrailerSourceCommit = "Skill-Source-Commit"
)

var (
	repoURL = regexp.MustCompile(`^(https://[A-Za-z0-9.-]+(:[0-9]+)?|file://)/[A-Za-z0-9._~/@+-]+$`)
	refName = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)
	// fullCommit is a full SHA-1 or SHA-256 commit ID.
	fullCommit = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
	gitHub     = regexp.MustCompile(`^https://github\.com/([A-Za-z0-9-]+)/([A-Za-z0-9._-]+?)(\.git)?(/tree/([^/]+)(/(.+?))?)?/?$`)
)

// check reports whether s can be fetched: an https:// or file:// URL, a
// plain branch or tag name, and a relative path of safe segments.
func (s Source) check() error {
	switch {
	case !repoURL.MatchString(s.Repo) || !plainSegments(s.Repo):
		return fmt.Errorf("%w: repository %q (want an https:// or file:// URL, without credentials)", ErrInvalid, redact(s.Repo))
	case s.Ref != "" && (!refName.MatchString(s.Ref) || strings.Contains(s.Ref, "..") || strings.HasPrefix(s.Ref, "-")):
		return fmt.Errorf("%w: ref %q", ErrInvalid, s.Ref)
	case s.Path != "" && !skillPath(s.Path):
		return fmt.Errorf("%w: path %q", ErrInvalid, s.Path)
	case s.Commit != "" && !fullCommit.MatchString(s.Commit):
		return fmt.Errorf("%w: commit %q (want a full commit ID)", ErrInvalid, s.Commit)
	}
	return nil
}

// safePath is a relative path of segments of letters, digits, ".", "_"
// and "-", none starting with "." or "-": nothing a sparse-checkout
// pattern, a shell or a terminal reads specially.
var safePath = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*(/[A-Za-z0-9_][A-Za-z0-9._-]*)*$`)

func skillPath(p string) bool { return len(p) <= 255 && safePath.MatchString(p) }

// SourceFor is the source a person names: a GitHub link (to a repository
// or one of its folders, with or without https://), or any repository URL with the skill's folder
// and branch given apart. A GitHub link to a folder carries its own
// branch and folder, so path and branch must then be empty.
func SourceFor(link, path, branch string) (Source, error) {
	if strings.HasPrefix(link, "github.com/") {
		link = "https://" + link // as people copy it from the address bar
	}
	if !strings.HasPrefix(link, "https://github.com/") {
		s := Source{Repo: link, Ref: branch, Path: path}
		return s, s.check()
	}
	s, err := ParseGitHub(link)
	if err != nil {
		return s, err
	}
	if path != "" || branch != "" {
		if s.Ref != "" || s.Path != "" {
			return s, fmt.Errorf("%w: a GitHub link to a folder carries its own branch and folder: give the folder and branch with the repository's link instead", ErrInvalid)
		}
		s.Ref, s.Path = branch, path
	}
	return s, s.check()
}

// ParseGitHub reads a GitHub link to a skill's folder,
// https://github.com/OWNER/REPO/tree/REF/PATH, or to a repository whose
// root is a skill. REF is one path segment: a branch or tag with a "/" in
// its name needs a Source written out instead.
func ParseGitHub(link string) (Source, error) {
	m := gitHub.FindStringSubmatch(link)
	if m == nil {
		return Source{}, fmt.Errorf("%w: %q isn't a GitHub link to a repository or one of its folders", ErrInvalid, link)
	}
	s := Source{Repo: "https://github.com/" + m[1] + "/" + m[2], Ref: m[5], Path: m[7]}
	return s, s.check()
}

// FetchTimeout bounds a fetch or a catalog whose context has no deadline
// of its own, so a host that never answers can't hang it.
const FetchTimeout = 5 * time.Minute

// Fetch reads a skill's folder from its source with the git CLI: a
// shallow, sparse clone of just that folder into a temporary directory,
// removed afterwards. The files are then read from Git's own objects, not
// the checked-out folder, so their bytes are exactly the commit's: no
// line-ending or attribute conversion, and no file-system case folding.
// Their number and sizes are checked before any is read. Hidden files and
// folders are left out; a symbolic link or submodule refuses the folder.
// It returns the folder and the source with Commit set. A source that
// already names its Commit (one a Catalog listed) is fetched only if its
// branch is still at that commit: what was seen is what's imported, or
// nothing (roadmap 10.6).
func Fetch(ctx context.Context, s Source) (map[string][]byte, Source, error) {
	seen := s.Commit
	if err := s.check(); err != nil {
		return nil, s, err
	}
	ctx, cancel := withDeadline(ctx)
	defer cancel()
	dir, err := os.MkdirTemp("", "treadle-fetch-")
	if err != nil {
		return nil, s, err
	}
	defer os.RemoveAll(dir)
	pattern := "/*"
	if s.Path != "" {
		pattern = "/" + s.Path + "/"
	}
	if err := sparseClone(ctx, dir, s, pattern); err != nil {
		return nil, s, err
	}
	src := dir + "/src"
	commit, err := git(ctx, src, "rev-parse", "HEAD")
	if err != nil {
		return nil, s, err
	}
	s.Commit = strings.TrimSpace(commit)
	if seen != "" && seen != s.Commit {
		return nil, s, fmt.Errorf("%w: %s has changed since it was listed: it is at %s now, not %s; list it again", ErrInvalid, s.Repo, s.Commit, seen)
	}
	treeish := "HEAD^{tree}" // HEAD alone is a commit, never a tree
	if s.Path != "" {
		treeish = "HEAD:" + s.Path
	}
	if kind, err := git(ctx, src, "cat-file", "-t", treeish); err != nil || strings.TrimSpace(kind) != "tree" {
		return nil, s, fmt.Errorf("%w: %s has no folder %q at %s", ErrInvalid, s.Repo, s.Path, s.Commit)
	}
	list, err := git(ctx, src, "ls-tree", "-r", "-l", "-z", treeish)
	if err != nil {
		return nil, s, err
	}
	type entry struct{ path, id string }
	var want []entry
	var total int64
	for _, rec := range strings.Split(strings.TrimSuffix(list, "\x00"), "\x00") {
		if rec == "" {
			continue
		}
		meta, p, ok := strings.Cut(rec, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 4 {
			return nil, s, fmt.Errorf("treadle: unexpected ls-tree output %q", rec)
		}
		if hidden(p) {
			continue
		}
		switch f[0] {
		case "100644", "100755":
		case "120000":
			return nil, s, fmt.Errorf("%w: %s is a symbolic link", ErrInvalid, p)
		default:
			return nil, s, fmt.Errorf("%w: %s isn't a regular file (mode %s)", ErrInvalid, p, f[0])
		}
		size, err := strconv.ParseInt(f[3], 10, 64)
		if err != nil {
			return nil, s, fmt.Errorf("treadle: unexpected ls-tree size %q", f[3])
		}
		total += size
		want = append(want, entry{p, f[2]})
		if len(want) > pirn.MaxSkillFiles || total > pirn.MaxSkillBytes {
			return nil, s, fmt.Errorf("%w: a skill has at most %d files and %d bytes", ErrInvalid, pirn.MaxSkillFiles, pirn.MaxSkillBytes)
		}
	}
	files := make(map[string][]byte, len(want))
	for _, e := range want {
		b, err := git(ctx, src, "cat-file", "blob", e.id)
		if err != nil {
			return nil, s, err
		}
		files[e.path] = []byte(b)
	}
	return files, s, nil
}

// hidden reports whether a path has a segment starting with ".".
func hidden(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}

// withDeadline is ctx, with FetchTimeout if it has no deadline.
func withDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, FetchTimeout)
}

// CatalogEntry is one skill a repository holds.
type CatalogEntry struct {
	Source      Source `json:"source"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Problem     string `json:"problem,omitempty"` // why it can't be imported as it is
}

// MaxProblem bounds a catalog entry's Problem: a refusal can quote the
// SKILL.md, which may be up to MaxSkillMD (review 24).
const MaxProblem = 300

// clip is s cut to at most n bytes, on a character boundary, marked "…".
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// MaxSkillMD is the largest SKILL.md a skill may have (Meta refuses a
// bigger one), so a review, a catalog and the Skills page always show it
// whole.
const MaxSkillMD = 1 << 20

// Catalog lists the skills a repository holds: every folder, up to three
// levels down, whose SKILL.md treadle can read, fetching only those
// files. A SKILL.md treadle refuses is listed with its Problem; a folder
// whose path treadle couldn't import is left out.
func Catalog(ctx context.Context, repo, ref string) ([]CatalogEntry, error) {
	s := Source{Repo: repo, Ref: ref}
	if err := s.check(); err != nil {
		return nil, err
	}
	ctx, cancel := withDeadline(ctx)
	defer cancel()
	dir, err := os.MkdirTemp("", "treadle-catalog-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	// Only files named SKILL.md: a trailing / makes the ! patterns leave
	// out folders of that name, and everything in them.
	if err := sparseClone(ctx, dir, s, "/SKILL.md", "/*/SKILL.md", "/*/*/SKILL.md", "/*/*/*/SKILL.md",
		"!/SKILL.md/", "!/*/SKILL.md/", "!/*/*/SKILL.md/", "!/*/*/*/SKILL.md/"); err != nil {
		return nil, err
	}
	commit, err := git(ctx, dir+"/src", "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	s.Commit = strings.TrimSpace(commit)
	var out []CatalogEntry
	root := filepath.Join(dir, "src")
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.IsDir() || d.Name() != SkillFile || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, filepath.Dir(p))
		if err != nil {
			return err
		}
		if rel != "." && !skillPath(filepath.ToSlash(rel)) {
			return nil // a folder name treadle can't import, or print safely
		}
		e := CatalogEntry{Source: s}
		if rel != "." {
			e.Source.Path = filepath.ToSlash(rel)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > MaxSkillMD {
			e.Problem = fmt.Sprintf("its SKILL.md is over %d bytes", MaxSkillMD)
			out = append(out, e)
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if e.Name, e.Description, err = Meta(b); err != nil {
			e.Problem = clip(err.Error(), MaxProblem)
		} else if e.Source.Path != "" && path.Base(e.Source.Path) != e.Name {
			e.Problem = fmt.Sprintf("its folder is %q but its name is %q", path.Base(e.Source.Path), e.Name)
		}
		out = append(out, e)
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Source.Path < out[j].Source.Path })
	return out, err
}

// Import fetches a skill from its source, with the source on its commit
// (TrailerSource and the rest), so its history says where every version
// came from. A source under a trusted prefix (Trusted) is kept as the
// skill's newest version at once, unless it asks for connectors. Any other source waits for review: it
// is kept on the skill's pending ref and returned with Pending set, and
// becomes a version only once Approve names it. Importing the version a
// skill already has changes nothing.
func (l *Library) Import(ctx context.Context, s Source, at time.Time) (Skill, error) {
	files, s, err := Fetch(ctx, s)
	if err != nil {
		return Skill{}, err
	}
	trusted, err := l.Trusted(ctx)
	if err != nil {
		return Skill{}, err
	}
	// A skill that asks for connectors waits for review even from a
	// trusted prefix: a person approves what it may reach (review 20).
	if g, _ := Connectors(files[SkillFile]); trustedBy(trusted, s.Repo) && len(g) == 0 {
		return l.add(ctx, files, at, &s, nil, nil)
	}
	return l.propose(ctx, files, at, &s)
}

// sparseClone clones s's ref into dir/src, shallow, fetching only the
// files the sparse-checkout patterns select.
// missing is what git says when a repository or ref doesn't exist, or
// (GitHub's answer for both a missing and a private repository) asks for
// credentials it may not prompt for.
var missing = regexp.MustCompile(`(?i)repository .*not found|does not exist|not appear to be a git repository|could not find remote branch|remote branch .* not found|could not read username`)

func sparseClone(ctx context.Context, dir string, s Source, patterns ...string) error {
	args := []string{"clone", "--quiet", "--depth", "1", "--filter=blob:none", "--sparse", "--no-checkout"}
	if s.Ref != "" {
		args = append(args, "--branch", s.Ref)
	}
	if _, err := git(ctx, dir, append(args, "--", s.Repo, "src")...); err != nil {
		// A repository or ref that isn't there won't appear on a retry:
		// say so as a refusal, not a failure to retry (review 18).
		if missing.MatchString(err.Error()) {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		return err
	}
	if _, err := git(ctx, dir+"/src", append([]string{"sparse-checkout", "set", "--no-cone", "--"}, patterns...)...); err != nil {
		return err
	}
	_, err := git(ctx, dir+"/src", "checkout", "--quiet")
	return err
}

// git runs the git CLI in dir, never asking for credentials (the user's
// own git setup, such as gh auth setup-git, still applies), and never
// following a repository's own hooks or filters into this machine.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	return gitIn(ctx, dir, nil, args...)
}

// gitIn is git with stdin.
func gitIn(ctx context.Context, dir string, stdin []byte, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "protocol.file.allow=user"}, args...)...)
	cmd.Dir = dir
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	cmd.Env = append(gitEnv(), "GIT_TERMINAL_PROMPT=0", "GIT_LFS_SKIP_SMUDGE=1")
	// git starts helpers (git-remote-https) that outlive it if only it is
	// killed: give it its own process group, and kill the group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("treadle: git %s: %v: %s", args[0], err, redact(strings.TrimSpace(stderr.String())))
	}
	return out.String(), nil
}

// gitEnv is this process's environment without the variables that
// would point git at another repository, index or object store, or add
// configuration (hooks, filters) from outside (review 49).
func gitEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case k == "GIT_DIR", k == "GIT_WORK_TREE", k == "GIT_INDEX_FILE", k == "GIT_OBJECT_DIRECTORY", k == "GIT_ALTERNATE_OBJECT_DIRECTORIES",
			k == "GIT_CEILING_DIRECTORIES", k == "GIT_COMMON_DIR", k == "GIT_NAMESPACE", k == "GIT_DISCOVERY_ACROSS_FILESYSTEM",
			k == "GIT_CONFIG", k == "GIT_CONFIG_PARAMETERS", k == "GIT_CONFIG_COUNT",
			strings.HasPrefix(k, "GIT_CONFIG_KEY_"), strings.HasPrefix(k, "GIT_CONFIG_VALUE_"):
			// GIT_CONFIG_GLOBAL, _SYSTEM and _NOSYSTEM choose your own
			// configuration files, and are kept (review 49).
			continue
		}
		out = append(out, kv)
	}
	return out
}

var userinfo = regexp.MustCompile(`://[^/@\s]*@`)

// redact drops credentials (user:token@) from URLs in text.
func redact(text string) string { return userinfo.ReplaceAllString(text, "://") }

// ReadFolder reads every regular file under dir, by "/"-separated path,
// as a skill's folder. Hidden files and folders (.git, .DS_Store) aren't
// part of a skill and are left out; symbolic links and other special
// files are refused, since a skill is its files' bytes.
func ReadFolder(dir string) (map[string][]byte, error) {
	files := map[string][]byte{}
	var total int64
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != dir && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%w: %s isn't a regular file", ErrInvalid, p)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		// Counted before reading, so one huge file is refused unread.
		if total += info.Size(); len(files) >= pirn.MaxSkillFiles || total > pirn.MaxSkillBytes {
			return fmt.Errorf("%w: a skill has at most %d files and %d bytes", ErrInvalid, pirn.MaxSkillFiles, pirn.MaxSkillBytes)
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		files[filepath.ToSlash(rel)] = b
		return err
	})
	return files, err
}

// sourceTrailers are a source's commit trailers.
func sourceTrailers(s *Source) []warp.Trailer {
	if s == nil {
		return nil
	}
	t := []warp.Trailer{{Key: TrailerSource, Value: s.Repo}}
	if s.Ref != "" {
		t = append(t, warp.Trailer{Key: TrailerSourceRef, Value: s.Ref})
	}
	if s.Path != "" {
		t = append(t, warp.Trailer{Key: TrailerSourcePath, Value: s.Path})
	}
	if s.Commit != "" {
		t = append(t, warp.Trailer{Key: TrailerSourceCommit, Value: s.Commit})
	}
	return t
}

// sourceOf reads a version's source back from its commit's trailers, or
// nil if it was added from a folder.
func sourceOf(ts []warp.Trailer) *Source {
	var s Source
	for _, t := range ts {
		switch t.Key {
		case TrailerSource:
			s.Repo = t.Value
		case TrailerSourceRef:
			s.Ref = t.Value
		case TrailerSourcePath:
			s.Path = t.Value
		case TrailerSourceCommit:
			s.Commit = t.Value
		}
	}
	if s.Repo == "" {
		return nil
	}
	return &s
}
