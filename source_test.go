package treadle_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/heedle-ai/treadle"
)

// skillRepo makes a Git repository holding skills, and returns its file://
// URL. Each file's path is relative to the repository's root.
func skillRepo(t *testing.T, files map[string]string, links map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for p, c := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644)
	}
	for p, target := range links {
		os.Symlink(target, filepath.Join(dir, p))
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"},
		{"-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "-m", "skills"}, {"tag", "v1"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return "file://" + dir
}

func md(name, desc string) string {
	return "---\nname: " + name + "\ndescription: " + desc + "\n---\nDo it.\n"
}

// TestInv_ImportsKeepTheirSource: a skill imported from a folder of a Git
// repository is that folder's files exactly (hidden files left out), and
// its version's commit names the repository, ref, folder and source
// commit, which Skills and Versions read back; importing again gives the
// same version.
func TestInv_ImportsKeepTheirSource(t *testing.T) {
	repo := skillRepo(t, map[string]string{
		"skills/pdf/SKILL.md": md("pdf", "Reads PDFs."), "skills/pdf/scripts/x.py": "print(1)", "skills/pdf/.hidden": "x",
		"skills/docx/SKILL.md": md("docx", "Reads Word files."), "README.md": "hi",
	}, nil)
	l := newLibrary(t)
	s, err := l.Import(ctx, treadle.Source{Repo: repo, Ref: "v1", Path: "skills/pdf"}, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	files := must(l.Files(ctx, s.Version))
	if len(files) != 2 || string(files["scripts/x.py"]) != "print(1)" {
		t.Errorf("files %q", files)
	}
	head, _ := exec.Command("git", "-C", strings.TrimPrefix(repo, "file://"), "rev-parse", "HEAD").Output()
	want := treadle.Source{Repo: repo, Ref: "v1", Path: "skills/pdf", Commit: strings.TrimSpace(string(head))}
	if s.Source == nil || *s.Source != want {
		t.Errorf("source %+v, want %+v", s.Source, want)
	}
	vs := must(l.Versions(ctx, "pdf"))
	list, _, _ := l.Skills(ctx)
	if len(vs) != 1 || vs[0].Source == nil || *vs[0].Source != want || len(list) != 1 || list[0].Source == nil || *list[0].Source != want {
		t.Errorf("read back: versions %+v, skills %+v", vs, list)
	}
	if again := must(l.Import(ctx, treadle.Source{Repo: repo, Path: "skills/pdf"}, time.Unix(1700000099, 0))); again.Version != s.Version || again.Commit != s.Commit {
		t.Errorf("importing again: %+v", again)
	}
}

// TestInv_ImportsAreChecked: a source must be an https:// or file:// URL,
// a plain branch or tag, and a safe relative path, refused before git
// runs; a folder that's missing, holds a symbolic link, or isn't a valid
// skill is refused; nothing is kept.
func TestInv_ImportsAreChecked(t *testing.T) {
	repo := skillRepo(t, map[string]string{
		"ok/SKILL.md": md("ok", "Fine."), "bad/SKILL.md": "no front matter", "linked/SKILL.md": md("linked", "Has a link."),
	}, map[string]string{"linked/passwd": "/etc/passwd"})
	l := newLibrary(t)
	for _, s := range []treadle.Source{
		{Repo: "--upload-pack=touch /tmp/x"}, {Repo: "ssh://github.com/a/b"}, {Repo: "http://github.com/a/b"}, {Repo: "github.com/a/b"},
		{Repo: "https://github.com/a/../b"}, {Repo: repo, Ref: "--orphan"}, {Repo: repo, Ref: "a..b"}, {Repo: repo, Path: "../ok"},
		{Repo: repo, Path: "/ok"}, {Repo: repo, Path: ".git"}, {Repo: repo, Path: "-x"},
	} {
		if _, err := l.Import(ctx, s, time.Now()); !errors.Is(err, treadle.ErrInvalid) {
			t.Errorf("source %+v: %v", s, err)
		}
	}
	for _, path := range []string{"missing", "bad", "linked"} {
		if _, err := l.Import(ctx, treadle.Source{Repo: repo, Path: path}, time.Now()); err == nil {
			t.Errorf("imported %s", path)
		}
	}
	if list, _, _ := l.Skills(ctx); len(list) != 0 {
		t.Errorf("kept %+v", list)
	}
	if _, err := l.Import(ctx, treadle.Source{Repo: repo, Ref: "nope", Path: "ok"}, time.Now()); err == nil {
		t.Error("imported from a ref that doesn't exist")
	}
}

// TestInv_GitHubLinks: a GitHub link to a repository, or to one of its
// folders at a branch or tag, is read as a source; anything else isn't.
func TestInv_GitHubLinks(t *testing.T) {
	for link, want := range map[string]treadle.Source{
		"https://github.com/anthropics/skills/tree/main/skills/pdf": {Repo: "https://github.com/anthropics/skills", Ref: "main", Path: "skills/pdf"},
		"https://github.com/anthropics/skills/tree/v1/skills/pdf/":  {Repo: "https://github.com/anthropics/skills", Ref: "v1", Path: "skills/pdf"},
		"https://github.com/acme/weather":                           {Repo: "https://github.com/acme/weather"},
		"https://github.com/acme/weather.git":                       {Repo: "https://github.com/acme/weather"},
		"https://github.com/acme/weather/tree/main":                 {Repo: "https://github.com/acme/weather", Ref: "main"},
	} {
		if got, err := treadle.ParseGitHub(link); err != nil || got != want {
			t.Errorf("%s: %+v %v", link, got, err)
		}
	}
	for _, bad := range []string{"https://gitlab.com/a/b", "http://github.com/a/b", "https://github.com/a", "https://github.com/a/b/blob/main/x",
		"https://github.com/a/b/tree/main/../x", "https://github.com/a/b/tree/main/.git", "https://github.com/a/b?x=1"} {
		if _, err := treadle.ParseGitHub(bad); !errors.Is(err, treadle.ErrInvalid) {
			t.Errorf("accepted %s", bad)
		}
	}
}

// TestInv_CatalogListsARepositorysSkills: a catalog lists every folder
// with a SKILL.md, up to three levels down, with its name and description,
// and says why one can't be imported as it is.
func TestInv_CatalogListsARepositorysSkills(t *testing.T) {
	repo := skillRepo(t, map[string]string{
		"skills/pdf/SKILL.md": md("pdf", "Reads PDFs."), "skills/pdf/big.bin": strings.Repeat("x", 1000),
		"skills/odd/SKILL.md": md("other", "Named differently."), "broken/SKILL.md": "nope",
		"a/b/c/d/SKILL.md": md("d", "Too deep."), "README.md": "hi",
	}, nil)
	cat, err := treadle.Catalog(ctx, repo, "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]treadle.CatalogEntry{}
	for _, e := range cat {
		got[e.Source.Path] = e
	}
	if len(cat) != 3 || got["skills/pdf"].Name != "pdf" || got["skills/pdf"].Problem != "" || got["skills/pdf"].Source.Commit == "" ||
		got["skills/odd"].Problem == "" || got["broken"].Problem == "" {
		t.Errorf("catalog %+v", cat)
	}
	if _, err := treadle.Catalog(context.Background(), "--upload-pack=x", ""); !errors.Is(err, treadle.ErrInvalid) {
		t.Errorf("catalog of a bad URL: %v", err)
	}
}
