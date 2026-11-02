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
	"strings"
	"time"

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
	gitHub  = regexp.MustCompile(`^https://github\.com/([A-Za-z0-9-]+)/([A-Za-z0-9._-]+?)(\.git)?(/tree/([^/]+)(/(.+?))?)?/?$`)
)

// check reports whether s can be fetched: an https:// or file:// URL, a
// plain branch or tag name, and a relative path of safe segments.
func (s Source) check() error {
	switch {
	case !repoURL.MatchString(s.Repo) || strings.Contains(s.Repo, ".."):
		return fmt.Errorf("%w: repository %q (want an https:// or file:// URL)", ErrInvalid, s.Repo)
	case s.Ref != "" && (!refName.MatchString(s.Ref) || strings.Contains(s.Ref, "..") || strings.HasPrefix(s.Ref, "-")):
		return fmt.Errorf("%w: ref %q", ErrInvalid, s.Ref)
	case s.Path != "" && !skillPath(s.Path):
		return fmt.Errorf("%w: path %q", ErrInvalid, s.Path)
	}
	return nil
}

func skillPath(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") || strings.HasPrefix(seg, "-") {
			return false
		}
	}
	return true
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

// Fetch reads a skill's folder from its source with the git CLI: a
// shallow, sparse clone of just that folder into a temporary directory,
// removed afterwards. It returns the folder (ReadFolder's rules) and the
// source with Commit set.
func Fetch(ctx context.Context, s Source) (map[string][]byte, Source, error) {
	if err := s.check(); err != nil {
		return nil, s, err
	}
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
	commit, err := git(ctx, dir+"/src", "rev-parse", "HEAD")
	if err != nil {
		return nil, s, err
	}
	s.Commit = strings.TrimSpace(commit)
	folder := filepath.Join(dir, "src", filepath.FromSlash(s.Path))
	if info, err := os.Lstat(folder); err != nil || !info.IsDir() {
		return nil, s, fmt.Errorf("%w: %s has no folder %q at %s", ErrInvalid, s.Repo, s.Path, s.Commit)
	}
	files, err := ReadFolder(folder)
	return files, s, err
}

// CatalogEntry is one skill a repository holds.
type CatalogEntry struct {
	Source      Source `json:"source"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Problem     string `json:"problem,omitempty"` // why it can't be imported as it is
}

// Catalog lists the skills a repository holds: every folder, up to three
// levels down, whose SKILL.md treadle can read, fetching only those
// files. A SKILL.md treadle refuses is listed with its Problem.
func Catalog(ctx context.Context, repo, ref string) ([]CatalogEntry, error) {
	s := Source{Repo: repo, Ref: ref}
	if err := s.check(); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "treadle-catalog-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if err := sparseClone(ctx, dir, s, "/SKILL.md", "/*/SKILL.md", "/*/*/SKILL.md", "/*/*/*/SKILL.md"); err != nil {
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
		e := CatalogEntry{Source: s}
		if rel != "." {
			e.Source.Path = filepath.ToSlash(rel)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if e.Name, e.Description, err = Meta(b); err != nil {
			e.Problem = err.Error()
		} else if e.Source.Path != "" && path.Base(e.Source.Path) != e.Name {
			e.Problem = fmt.Sprintf("its folder is %q but its name is %q", path.Base(e.Source.Path), e.Name)
		}
		out = append(out, e)
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Source.Path < out[j].Source.Path })
	return out, err
}

// Import fetches a skill from its source and keeps it as its newest
// version, with the source on the version's commit (TrailerSource and the
// rest), so its history says where every version came from.
func (l *Library) Import(ctx context.Context, s Source, at time.Time) (Skill, error) {
	files, s, err := Fetch(ctx, s)
	if err != nil {
		return Skill{}, err
	}
	return l.add(ctx, files, at, &s)
}

// sparseClone clones s's ref into dir/src, shallow, fetching only the
// files the sparse-checkout patterns select.
func sparseClone(ctx context.Context, dir string, s Source, patterns ...string) error {
	args := []string{"clone", "--quiet", "--depth", "1", "--filter=blob:none", "--sparse", "--no-checkout"}
	if s.Ref != "" {
		args = append(args, "--branch", s.Ref)
	}
	if _, err := git(ctx, dir, append(args, "--", s.Repo, "src")...); err != nil {
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
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "protocol.file.allow=user"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_LFS_SKIP_SMUDGE=1")
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("treadle: git %s: %v: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out.String(), nil
}

// ReadFolder reads every regular file under dir, by "/"-separated path,
// as a skill's folder. Hidden files and folders (.git, .DS_Store) aren't
// part of a skill and are left out; symbolic links and other special
// files are refused, since a skill is its files' bytes.
func ReadFolder(dir string) (map[string][]byte, error) {
	files := map[string][]byte{}
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
