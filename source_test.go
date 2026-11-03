package treadle_test

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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
	must(0, l.Trust(ctx, repo, time.Unix(1700000000, 0)))
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
	if list, _, _ := l.Pending(ctx); len(list) != 0 {
		t.Errorf("kept pending %+v", list)
	}
	// Review 18: a missing repository or ref was a failure to retry, not
	// a refusal.
	if _, err := l.Import(ctx, treadle.Source{Repo: repo, Ref: "nope", Path: "ok"}, time.Now()); !errors.Is(err, treadle.ErrInvalid) {
		t.Errorf("a ref that doesn't exist: %v", err)
	}
	if _, err := l.Import(ctx, treadle.Source{Repo: "file:///nonexistent/repo"}, time.Now()); !errors.Is(err, treadle.ErrInvalid) {
		t.Errorf("a repository that doesn't exist: %v", err)
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
	// SourceFor: a repository's link takes a folder and branch; a
	// folder's link carries its own; any other URL is checked as a source.
	if s, err := treadle.SourceFor("github.com/a/b", "skills/pdf", "v1"); err != nil || s != (treadle.Source{Repo: "https://github.com/a/b", Ref: "v1", Path: "skills/pdf"}) {
		t.Errorf("SourceFor(repo, path, branch) = %+v, %v", s, err)
	}
	for _, bad := range [][3]string{{"https://github.com/a/b/tree/main/x", "", "v1"}, {"https://github.com/a/b/tree/main/x", "y", ""}, {"ssh://x/y", "", ""}, {"file:///tmp/r", "../x", ""}} {
		if _, err := treadle.SourceFor(bad[0], bad[1], bad[2]); !errors.Is(err, treadle.ErrInvalid) {
			t.Errorf("SourceFor%q: %v", bad, err)
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

// TestInv_ImportsAreExactBytes: an imported skill's files are the
// commit's bytes, whatever the repository's .gitattributes or the
// machine's settings say (review 14: eol=crlf and ident changed them, so
// one source gave different versions on different machines); a symbolic
// link is refused.
func TestInv_ImportsAreExactBytes(t *testing.T) {
	body := md("exact", "Keeps its bytes.") + "$Id$\nline two\n"
	repo := skillRepo(t, map[string]string{".gitattributes": "* text eol=crlf ident\n", "exact/SKILL.md": body, "exact/run.sh": "echo hi\n"}, nil)
	files, _, err := treadle.Fetch(ctx, treadle.Source{Repo: repo, Path: "exact"})
	if err != nil {
		t.Fatal(err)
	}
	if string(files["SKILL.md"]) != body || string(files["run.sh"]) != "echo hi\n" {
		t.Errorf("bytes changed: %q", files["SKILL.md"])
	}
	linked := skillRepo(t, map[string]string{"l/SKILL.md": md("l", "Linked.")}, map[string]string{"l/x": "SKILL.md"})
	if _, _, err := treadle.Fetch(ctx, treadle.Source{Repo: linked, Path: "l"}); !errors.Is(err, treadle.ErrInvalid) {
		t.Errorf("a symbolic link: %v", err)
	}
}

// TestInv_ImportSizesAreCheckedFirst: a folder over the limits is refused
// before its files are read, from disk or from Git. (Review 14: a 300 MB
// file was read whole before being refused.)
func TestInv_ImportSizesAreCheckedFirst(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md("big", "Big.")), 0o644)
	f, _ := os.Create(filepath.Join(dir, "huge.bin"))
	f.Truncate(1 << 40) // a sparse terabyte: reading it would never end
	f.Close()
	if _, err := treadle.ReadFolder(dir); !errors.Is(err, treadle.ErrInvalid) {
		t.Errorf("a terabyte file: %v", err)
	}
	repo := skillRepo(t, map[string]string{"big/SKILL.md": md("big", "Big."), "big/huge.bin": strings.Repeat("x", 17<<20)}, nil)
	if _, _, err := treadle.Fetch(ctx, treadle.Source{Repo: repo, Path: "big"}); !errors.Is(err, treadle.ErrInvalid) {
		t.Errorf("a 17 MiB file: %v", err)
	}
}

// TestInv_StalledFetchesEnd: a fetch from a host that never answers ends
// when its context does, leaving no git process behind. (Review 14: it
// hung forever, and git-remote-https outlived it.)
func TestInv_StalledFetchesEnd(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			defer c.Close() // accept, and never answer
		}
	}()
	tctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	start := time.Now()
	_, _, err = treadle.Fetch(tctx, treadle.Source{Repo: "https://" + l.Addr().String() + "/x/y"})
	if err == nil || time.Since(start) > 10*time.Second {
		t.Errorf("after %v: %v", time.Since(start), err)
	}
	time.Sleep(500 * time.Millisecond)
	if out, _ := exec.Command("pgrep", "-f", "remote-https https://"+l.Addr().String()).Output(); len(out) != 0 {
		t.Errorf("git processes left: %s", out)
	}
}

// TestInv_SourcePathsAreSafe: a source path or a catalog entry is only
// segments of letters, digits, ".", "_" and "-", so nothing reaches git's
// sparse-checkout patterns or a terminal as anything else. (Review 14.)
func TestInv_SourcePathsAreSafe(t *testing.T) {
	repo := skillRepo(t, map[string]string{"ok/SKILL.md": md("ok", "Fine."), "x\x1b[31mred/ok/SKILL.md": md("ok", "Coloured."),
		"weird/SKILL.md/big.bin": strings.Repeat("x", 1000)}, nil)
	for _, p := range []string{"*", "o*", "a\nb", "a[b]", "!x", `a\b`, "x\x1b[31m"} {
		if _, err := (&treadle.Library{}).Import(ctx, treadle.Source{Repo: repo, Path: p}, time.Now()); !errors.Is(err, treadle.ErrInvalid) {
			t.Errorf("path %q: %v", p, err)
		}
	}
	cat, err := treadle.Catalog(ctx, repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cat) != 1 || cat[0].Source.Path != "ok" {
		t.Errorf("catalog %+v", cat)
	}
	if _, err := treadle.Catalog(ctx, "https://user:SECRET@github.com/a/b", ""); err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("a URL's credentials: %v", err)
	}
}

// TestInv_CatalogProblemsAreBounded: a skill treadle refuses is listed with
// its problem cut to MaxProblem bytes, on a character boundary, however
// much of its SKILL.md the refusal would quote (review 24: a 1 MB name
// made a listing too big for Temporal).
func TestInv_CatalogProblemsAreBounded(t *testing.T) {
	huge := "---\nname: " + strings.Repeat("é", 400_000) + "\ndescription: x\n---\n"
	repo := skillRepo(t, map[string]string{"skills/big/SKILL.md": huge}, nil)
	cat, err := treadle.Catalog(ctx, repo, "")
	if err != nil || len(cat) != 1 {
		t.Fatalf("%d entries, %v", len(cat), err)
	}
	if p := cat[0].Problem; p == "" || len(p) > treadle.MaxProblem+len("…") || !utf8.ValidString(p) {
		t.Errorf("problem of %d bytes, valid UTF-8 %v", len(p), utf8.ValidString(p))
	}
}
