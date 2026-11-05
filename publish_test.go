package treadle_test

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/heedle-ai/treadle"
	"github.com/heedle-ai/warp"
)

// myRepo is a fresh clone-like repository of yours, with an identity
// and a bare remote, origin; it returns the work tree and the remote.
func myRepo(t *testing.T) (dir, origin string) {
	t.Helper()
	dir, origin = t.TempDir(), filepath.Join(t.TempDir(), "origin.git")
	for _, c := range [][]string{{"init", "-q", "--bare", origin}, {"-C", dir, "init", "-q", "-b", "main"}, {"-C", dir, "config", "user.name", "Ada"},
		{"-C", dir, "config", "user.email", "ada@example.org"}, {"-C", dir, "remote", "add", "origin", origin}} {
		if out, err := exec.Command("git", c...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", c, err, out)
		}
	}
	return dir, origin
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return string(out)
}

// randomSkill is a random skill folder: a SKILL.md with a random
// description (some connectors), and up to five more files, some in
// folders, some binary.
func randomSkill(r *rand.Rand, i int) map[string][]byte {
	name := fmt.Sprintf("skill-%d-%c", i, 'a'+rune(r.Intn(26)))
	words := []string{"Plans", "trips", "with", "tiles,", "food", "|", "pipes", "and", "[links](x)", "*stars*", "`code`", "#tag", "a_b", "50%", "&"}
	var d []string
	for n := 1 + r.Intn(10); n > 0; n-- {
		d = append(d, words[r.Intn(len(words))])
	}
	md := "---\nname: " + name + "\ndescription: \"" + strings.Join(d, " ") + "\"\n"
	if r.Intn(2) == 0 {
		md += "metadata:\n  connectors: \"search maps/directions\"\n"
	}
	md += "---\nDo it well.\n"
	files := map[string][]byte{"SKILL.md": []byte(md)}
	for n := r.Intn(6); n > 0; n-- {
		p := []string{"notes.md", "scripts/run.py", "data/a.csv", "data/deep/b.bin", "README.txt"}[r.Intn(5)]
		b := make([]byte, r.Intn(300))
		r.Read(b)
		if r.Intn(2) == 0 {
			b = []byte(fmt.Sprintf("line %d\nline two\n", r.Int()))
		}
		files[p] = b
	}
	return files
}

// TestLaw_PublishThenImportIsIdentity: for any approved skill,
// publishing it to a repository and importing it from there into another
// library gives the same files, byte for byte, the same modes (regular
// files, as treadle keeps them) and the same description, from the
// commit published, pinned. Checked on 40 random skill folders.
// (Roadmap 10.6.)
func TestLaw_PublishThenImportIsIdentity(t *testing.T) {
	r := rand.New(rand.NewSource(106))
	dir, _ := myRepo(t)
	at := time.Unix(1762300000, 0)
	for i := 0; i < 40; i++ {
		files := randomSkill(r, i)
		from := newLibrary(t)
		sk, err := from.Add(ctx, files, at)
		if err != nil {
			t.Fatalf("%d: add: %v", i, err)
		}
		pub, err := from.Publish(ctx, sk.Name, dir)
		if err != nil {
			t.Fatalf("%s: publish: %v", sk.Name, err)
		}
		to := newLibrary(t)
		got, err := to.Import(ctx, treadle.Source{Repo: "file://" + dir, Path: "skills/" + sk.Name}, at)
		if err != nil {
			t.Fatalf("%s: import: %v", sk.Name, err)
		}
		back, err := to.Files(ctx, got.Version)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(back, files) || got.Description != sk.Description || got.Name != sk.Name || got.Source.Commit != pub.Commit ||
			got.Version != sk.Version || got.Connectors != sk.Connectors {
			t.Fatalf("%s: published then imported isn't the same skill: %+v vs %+v", sk.Name, got, sk)
		}
		for _, l := range strings.Split(strings.TrimSpace(gitOut(t, dir, "ls-tree", "-r", "HEAD", "--", "skills/"+sk.Name)), "\n") {
			if !strings.HasPrefix(l, "100644 blob ") {
				t.Fatalf("%s: published %q", sk.Name, l)
			}
		}
	}
}

// TestInv_PublishingAgainChangesOnlyItsFolderAndRow: publishing a new
// version of a skill replaces its folder (a file it no longer has is
// gone) and its README row, and changes no other line, folder or file
// you changed; publishing the same version again commits nothing; the
// commit names the version (Treadle-Version). (Roadmap 10.6.)
func TestInv_PublishingAgainChangesOnlyItsFolderAndRow(t *testing.T) {
	dir, _ := myRepo(t)
	l := newLibrary(t)
	at := time.Unix(1762300000, 0)
	must(l.Add(ctx, skill("weather", "v1", "old.txt", "gone soon"), at))
	must(l.Add(ctx, skill("tides", "v1"), at))
	must(l.Publish(ctx, "weather", dir))
	must(l.Publish(ctx, "tides", dir))
	// Your own words around the table, and work of yours not committed.
	readme := filepath.Join(dir, "skills", "README.md")
	b, _ := os.ReadFile(readme)
	os.WriteFile(readme, append([]byte("# My skills\n\nHand-written intro.\n\n"), b...), 0o644)
	gitOut(t, dir, "commit", "-q", "-am", "intro")
	os.WriteFile(filepath.Join(dir, "mine.txt"), []byte("not yet"), 0o644)
	before := must(os.ReadFile(readme))

	v2 := must(l.Add(ctx, skill("weather", "v2", "new.txt", "hi"), at.Add(time.Hour)))
	pub := must(l.Publish(ctx, "weather", dir))
	changed := strings.Fields(gitOut(t, dir, "diff", "--name-only", "HEAD~1", "HEAD"))
	if !reflect.DeepEqual(changed, []string{"skills/README.md", "skills/weather/SKILL.md", "skills/weather/new.txt", "skills/weather/old.txt"}) {
		t.Errorf("changed %v", changed)
	}
	after := must(os.ReadFile(readme))
	var diff []string
	bl, al := strings.Split(string(before), "\n"), strings.Split(string(after), "\n")
	if len(bl) != len(al) {
		t.Fatalf("README lines %d -> %d:\n%s", len(bl), len(al), after)
	}
	for i := range bl {
		if bl[i] != al[i] {
			diff = append(diff, al[i])
		}
	}
	if len(diff) != 1 || !strings.HasPrefix(diff[0], "| [weather](weather/) |") || !strings.Contains(diff[0], string(v2.Version)) {
		t.Errorf("README changed %q", diff)
	}
	if !strings.Contains(gitOut(t, dir, "log", "-1", "--format=%B"), "Treadle-Version: "+string(v2.Version)) || pub.Commit == "" {
		t.Errorf("commit %+v", pub)
	}
	if st := gitOut(t, dir, "status", "--porcelain"); st != "?? mine.txt\n" {
		t.Errorf("your work: %q", st)
	}
	if again := must(l.Publish(ctx, "weather", dir)); again.Commit != "" || strings.TrimSpace(gitOut(t, dir, "rev-parse", "HEAD")) != pub.Commit {
		t.Errorf("the same version again: %+v", again)
	}
	// A skill gone from your library stays in the repository.
	if _, err := os.Stat(filepath.Join(dir, "skills", "tides", "SKILL.md")); err != nil {
		t.Error(err)
	}
}

// TestInv_PublishRefusesWhatItShould: before writing anything, Publish
// refuses a version waiting for review (never published), a skill
// whose files hold a planted key (naming the file and line), symbolic
// links at skills/, skills/<name>/ or inside it, a folder that isn't the
// top of a Git repository, and a skill it doesn't have. (Roadmap 10.6.)
func TestInv_PublishRefusesWhatItShould(t *testing.T) {
	at := time.Unix(1762300000, 0)
	clean := func(t *testing.T, dir string) {
		t.Helper()
		if st := gitOut(t, dir, "status", "--porcelain", "--ignored"); st != "" {
			t.Errorf("wrote %q", st)
		}
		if _, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output(); err == nil {
			t.Error("committed")
		}
	}
	// Waiting for review.
	l := newLibrary(t)
	repo := skillRepo(t, map[string]string{"trip/SKILL.md": md("trip", "Plans trips.")}, nil)
	if sk := must(l.Import(ctx, treadle.Source{Repo: repo, Path: "trip"}, at)); !sk.Pending {
		t.Fatal("not pending")
	}
	dir, _ := myRepo(t)
	if _, err := l.Publish(ctx, "trip", dir); !errors.Is(err, treadle.ErrNotApproved) {
		t.Errorf("pending: %v", err)
	}
	clean(t, dir)

	for _, key := range []string{
		"sk-ant-api03-abcdefghijklmnopqrstuvwxyz", "ghp_abcdefghijklmnopqrstuvwxyz0123456789", "github_pat_11ABCDEFG0123456789_abcdefghij",
		"AKIAIOSFODNN7EXAMPLE", "aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEYAB", "-----BEGIN OPENSSH PRIVATE KEY-----",
		"-----BEGIN RSA PRIVATE KEY-----", "xoxb-1234567890-abcdefghij", "sk_live_abcdefghijklmnop1234", "AIzaSyA-abcdefghijklmnopqrstuvwxyz0123456",
		"sk-proj-abcdefghijklmnopqrstuvwxyz0123456789",
	} {
		l := newLibrary(t)
		must(l.Add(ctx, skill("leaky", "body", "scripts/cfg.py", "x = 1\n# setup\nKEY = '"+key+"'\n"), at))
		dir, _ := myRepo(t)
		_, err := l.Publish(ctx, "leaky", dir)
		if !errors.Is(err, treadle.ErrSecret) || !strings.Contains(err.Error(), "scripts/cfg.py line 3") {
			t.Errorf("%.20s: %v", key, err)
		}
		clean(t, dir)
	}

	l = newLibrary(t)
	must(l.Add(ctx, skill("weather", "body"), at))
	elsewhere := t.TempDir()
	for _, link := range []func(dir string){
		func(dir string) { os.Symlink(elsewhere, filepath.Join(dir, "skills")) },
		func(dir string) {
			os.Mkdir(filepath.Join(dir, "skills"), 0o755)
			os.Symlink(elsewhere, filepath.Join(dir, "skills", "weather"))
		},
		func(dir string) {
			os.MkdirAll(filepath.Join(dir, "skills", "weather"), 0o755)
			os.Symlink(filepath.Join(elsewhere, "x"), filepath.Join(dir, "skills", "weather", "SKILL.md"))
		},
		func(dir string) {
			os.Mkdir(filepath.Join(dir, "skills"), 0o755)
			os.Symlink(filepath.Join(elsewhere, "r"), filepath.Join(dir, "skills", "README.md"))
		},
	} {
		dir, _ := myRepo(t)
		link(dir)
		if _, err := l.Publish(ctx, "weather", dir); !errors.Is(err, treadle.ErrInvalid) || !strings.Contains(err.Error(), "symbolic link") {
			t.Errorf("link: %v", err)
		}
		if es, _ := os.ReadDir(elsewhere); len(es) != 0 {
			t.Errorf("wrote through a link: %v", es)
		}
	}
	dir, _ = myRepo(t)
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	if _, err := l.Publish(ctx, "weather", filepath.Join(dir, "sub")); !errors.Is(err, treadle.ErrInvalid) {
		t.Errorf("not the top: %v", err)
	}
	if _, err := l.Publish(ctx, "weather", t.TempDir()); !errors.Is(err, treadle.ErrInvalid) {
		t.Errorf("not a repository: %v", err)
	}
	if _, err := l.Publish(ctx, "nope", dir); !errors.Is(err, warp.ErrNotFound) {
		t.Errorf("no such skill: %v", err)
	}
	if _, err := l.Publish(ctx, "../weather", dir); !errors.Is(err, treadle.ErrInvalid) {
		t.Errorf("a name that isn't one: %v", err)
	}
}

// TestInv_PublishNeverPushes: publishing commits in your clone and
// nothing else: your remote gets nothing, and the push is a command for
// you to run. (Roadmap 10.6.)
func TestInv_PublishNeverPushes(t *testing.T) {
	dir, origin := myRepo(t)
	l := newLibrary(t)
	must(l.Add(ctx, skill("weather", "body"), time.Unix(1762300000, 0)))
	pub := must(l.Publish(ctx, "weather", dir))
	if refs := gitOut(t, origin, "for-each-ref"); refs != "" {
		t.Errorf("the remote got %q", refs)
	}
	if pub.Push != "git -C "+dir+" push" {
		t.Errorf("push command %q", pub.Push)
	}
}

// TestInv_PublishedReadmeCantBeBroken: a description with |, newlines,
// links, images, HTML entities, backticks and emphasis is one cell of
// one row that reads as its text: the row has exactly its four columns,
// and nothing in it is a link, an image or HTML on GitHub. (Roadmap
// 10.6.)
func TestInv_PublishedReadmeCantBeBroken(t *testing.T) {
	dir, _ := myRepo(t)
	l := newLibrary(t)
	desc := `Evil | col ![img](https://x.example/t.png) [click](javascript:alert(1)) &lt;script&gt; ` + "`code`" + ` **bold** _it_ #h https://a.example`
	must(l.Add(ctx, map[string][]byte{"SKILL.md": []byte("---\nname: evil\ndescription: \"" + strings.ReplaceAll(desc, `"`, `\"`) + "\"\nmetadata:\n  connectors: \"search\"\n---\nx\n")}, time.Unix(1762300000, 0)))
	must(l.Publish(ctx, "evil", dir))
	b := must(os.ReadFile(filepath.Join(dir, "skills", "README.md")))
	var row string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "| [evil]") {
			row = l
		}
	}
	unescaped := 0
	for i := 0; i < len(row); i++ {
		if row[i] == '\\' {
			i++
			continue
		}
		if row[i] == '|' {
			unescaped++
		}
	}
	if unescaped != 5 {
		t.Errorf("%d cell walls in %q", unescaped, row)
	}
	cells := row[strings.Index(row[1:], "|")+2:]
	for _, bad := range []string{"](", "![", "<", "&lt;script", "**", "https://"} {
		if strings.Contains(cells, bad) {
			t.Errorf("%q in %q", bad, cells)
		}
	}
}

// TestInv_ImportPinsTheCommitSeen: a skill picked from a repository's
// catalog is imported from the commit the catalog was read at, or not at
// all if the repository has moved on since; an import by link takes the
// newest. (Roadmap 10.6: each imports pinned to the commit seen.)
func TestInv_ImportPinsTheCommitSeen(t *testing.T) {
	repo := skillRepo(t, map[string]string{"skills/trip/SKILL.md": md("trip", "Plans trips.")}, nil)
	cat := must(treadle.Catalog(ctx, repo, ""))
	if len(cat) != 1 || cat[0].Source.Commit == "" {
		t.Fatalf("%+v", cat)
	}
	l := newLibrary(t)
	if sk := must(l.Import(ctx, cat[0].Source, time.Unix(1762300000, 0))); sk.Source.Commit != cat[0].Source.Commit {
		t.Errorf("imported from %s, listed at %s", sk.Source.Commit, cat[0].Source.Commit)
	}
	change(t, repo, map[string]string{"skills/trip/SKILL.md": md("trip", "Plans trips, now with tiles.")})
	if _, err := newLibrary(t).Import(ctx, cat[0].Source, time.Unix(1762300000, 0)); !errors.Is(err, treadle.ErrInvalid) || !strings.Contains(err.Error(), "changed since it was listed") {
		t.Errorf("moved on: %v", err)
	}
	if _, err := newLibrary(t).Import(ctx, treadle.Source{Repo: repo, Path: "skills/trip", Commit: "abc"}, time.Unix(1762300000, 0)); !errors.Is(err, treadle.ErrInvalid) {
		t.Errorf("a commit that isn't one: %v", err)
	}
	if sk := must(newLibrary(t).Import(ctx, treadle.Source{Repo: repo, Path: "skills/trip"}, time.Unix(1762300000, 0))); sk.Description != "Plans trips, now with tiles." {
		t.Errorf("by link: %+v", sk)
	}
}
