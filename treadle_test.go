package treadle_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"

	"github.com/heedle-ai/bobbin"
	"github.com/heedle-ai/draft"
	"github.com/heedle-ai/draft/registry"
	"github.com/heedle-ai/loom"
	"github.com/heedle-ai/pirn"
	"github.com/heedle-ai/shuttle/plan"
	"github.com/heedle-ai/treadle"
	"github.com/heedle-ai/warp"
)

var ctx = context.Background()

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func newLibrary(t *testing.T) *treadle.Library {
	t.Helper()
	return &treadle.Library{Store: must(warp.Init(ctx, filepath.Join(t.TempDir(), "store.git")))}
}

func skill(name, body string, extra ...string) map[string][]byte {
	files := map[string][]byte{"SKILL.md": []byte("---\nname: " + name + "\ndescription: Says the weather for a city.\n---\n" + body)}
	for i := 0; i+1 < len(extra); i += 2 {
		files[extra[i]] = []byte(extra[i+1])
	}
	return files
}

// TestInv_SkillsAreChecked: a skill's front matter follows Anthropic's
// Agent Skills rules, and its folder pirn's (pirn.CheckSkill).
func TestInv_SkillsAreChecked(t *testing.T) {
	if name, desc, err := treadle.Meta([]byte("---\nname: weather-now\ndescription: |\n  Says the weather.\n---\nbody")); err != nil || name != "weather-now" || desc != "Says the weather." {
		t.Errorf("valid: %q %q %v", name, desc, err)
	}
	if _, _, err := treadle.Meta([]byte("---\r\nname: w\r\ndescription: d\r\n---\r\n")); err != nil {
		t.Errorf("CRLF: %v", err)
	}
	for _, bad := range []string{
		"name: w\ndescription: d\n", "---\nname: w\ndescription: d\n", "---\nname: [w\n---\n",
		"---\nname: Weather\ndescription: d\n---\n", "---\nname: wea--ther\ndescription: d\n---\n", "---\nname: -w\ndescription: d\n---\n",
		"---\nname: " + strings.Repeat("w", 65) + "\ndescription: d\n---\n", "---\nname: claude-helper\ndescription: d\n---\n",
		"---\nname: my-anthropic\ndescription: d\n---\n", "---\nname: w\n---\n", "---\nname: w\ndescription: \"  \"\n---\n",
		"---\nname: w\ndescription: " + strings.Repeat("d", 1025) + "\n---\n", "---\nname: w\ndescription: use <b>\n---\n",
		"---\nname: w\ndescription: d\n---\n\xff",
		// Review 13: a line starting --- closed the front matter; numbers
		// and binary passed as names; control characters reached terminals;
		// names the language reserves could be added but never called.
		"---\nname: w\n---extra\ndescription: d\n---\n", "---\nname: 123\ndescription: d\n---\n",
		"---\nname: !!binary d2VhdGhlcg==\ndescription: d\n---\n", "---\nname: w\ndescription: true\n---\n",
		"---\nname: w\ndescription: \"bad\\u001b[31m\"\n---\n", "---\nname: par\ndescription: d\n---\n",
		"---\nname: when\ndescription: d\n---\n", "---\n- name\n---\n",
	} {
		if _, _, err := treadle.Meta([]byte(bad)); !errors.Is(err, treadle.ErrInvalid) {
			t.Errorf("accepted %q", bad)
		}
	}
	for _, bad := range []map[string][]byte{
		{"README.md": []byte("x")}, skill("w", "", "../x", "y"), skill("w", "", ".env", "y"), skill("w", "", "a b", "y"),
	} {
		if _, _, err := treadle.Check(bad); !errors.Is(err, treadle.ErrInvalid) {
			t.Errorf("accepted the folder %v", bad)
		}
	}
}

// TestInv_LibraryKeepsEveryVersion: each version of a skill is a commit
// on refs/treadle/skills/<name> whose tree is the folder, byte for byte;
// adding the same folder again changes nothing; the newest is listed, and
// every version stays readable.
func TestInv_LibraryKeepsEveryVersion(t *testing.T) {
	l := newLibrary(t)
	at := time.Unix(1700000000, 0)
	v1 := must(l.Add(ctx, skill("weather", "v1", "scripts/get.sh", "echo rain"), at))
	if again := must(l.Add(ctx, skill("weather", "v1", "scripts/get.sh", "echo rain"), at.Add(time.Hour))); again != v1 {
		t.Errorf("adding the same folder again: %+v, want %+v", again, v1)
	}
	v2 := must(l.Add(ctx, skill("weather", "v2", "scripts/get.sh", "echo sun"), at.Add(time.Hour)))
	must(l.Add(ctx, skill("brief", "b"), at))
	if v2.Version == v1.Version {
		t.Fatal("two folders, one version")
	}
	if _, parents := must2(l.Store.Parents(ctx, v2.Commit)); len(parents) != 1 || parents[0] != v1.Commit {
		t.Errorf("v2's parents %v, want v1", parents)
	}
	list, bad, err := l.Skills(ctx)
	if err != nil || len(bad) != 0 {
		t.Fatal(err, bad)
	}
	if len(list) != 2 || list[0].Name != "brief" || list[1].Version != v2.Version || list[1].Description != "Says the weather for a city." {
		t.Errorf("skills %+v", list)
	}
	vs := must(l.Versions(ctx, "weather"))
	if len(vs) != 2 || vs[0].Version != v2.Version || vs[1].Version != v1.Version {
		t.Errorf("versions %+v", vs)
	}
	files := must(l.Files(ctx, v1.Version))
	if string(files["scripts/get.sh"]) != "echo rain" || !strings.HasSuffix(string(files["SKILL.md"]), "v1") || len(files) != 2 {
		t.Errorf("v1's files %q", files)
	}
	notSkill := must(l.Store.PutTree(ctx, []warp.Entry{{Name: "x", Kind: warp.KindBlob, ID: must(l.Store.PutBlob(ctx, []byte("x")))}}))
	if _, err := l.Files(ctx, notSkill); !errors.Is(err, treadle.ErrInvalid) {
		t.Errorf("a tree that isn't a skill: %v", err)
	}
	if _, err := l.Add(ctx, skill("w", "", "a", "1", "a/b", "2"), at); err == nil {
		t.Error("a path that's a file and a folder was kept")
	}
	if _, err := l.Versions(ctx, "../x"); !errors.Is(err, treadle.ErrInvalid) {
		t.Errorf("versions of a bad name: %v", err)
	}
}

func must2[A, B any](a A, b B, err error) (A, B) {
	if err != nil {
		panic(err)
	}
	return a, b
}

func node(id, name string, v warp.ID) plan.Node {
	return plan.Node{ID: plan.NodeID(id), Activity: treadle.ActivityName, Args: []string{name, string(v)}}
}

// TestInv_PinsAreChecked: a skill named like a command is left out, not
// fatal; a plan's pins must be versions added to the skill they're named
// as (never just any tree with that SKILL.md), one version per skill.
func TestInv_PinsAreChecked(t *testing.T) {
	l := newLibrary(t)
	at := time.Unix(1700000000, 0)
	echo := must(l.Add(ctx, skill("echo", "x"), at))
	w := must(l.Add(ctx, skill("weather", "x"), at))
	// Review 13: a skill named like a command stopped every worker; now
	// it's left out, and the others are still registered.
	reg := draft.DefaultRegistry()
	if skipped, err := l.AddTo(ctx, reg); err != nil || len(skipped) != 1 || !strings.Contains(skipped[0].Error(), `"echo"`) {
		t.Errorf("a skill named echo: %v %v", skipped, err)
	}
	if _, ok := reg.Lookup("weather"); !ok {
		t.Error("weather wasn't registered beside the bad skill")
	}
	// Review 13: a tree that was never added, even one whose SKILL.md is
	// named weather (an agent can write one into its /work/out, which loom
	// records in the same store), can't be pinned, recorded or run.
	forged := must(l.Store.PutTree(ctx, []warp.Entry{{Name: "SKILL.md", Kind: warp.KindBlob,
		ID: must(l.Store.PutBlob(ctx, skill("weather", "forged")["SKILL.md"]))}}))
	fp := plan.Plan{Nodes: []plan.Node{node("n0", "weather", forged)}}
	if _, err := l.Pinned(ctx, draft.DefaultRegistry(), fp); !errors.Is(err, treadle.ErrInvalid) {
		t.Errorf("a forged version was pinned: %v", err)
	}
	if _, err := l.Uses(ctx, fp); !errors.Is(err, treadle.ErrInvalid) {
		t.Errorf("a forged version was recorded: %v", err)
	}
	for _, p := range []plan.Plan{
		{Nodes: []plan.Node{node("n0", "weather", warp.ID(strings.Repeat("ab", 20)))}},
		{Nodes: []plan.Node{node("n0", "weather", echo.Version)}},
		{Nodes: []plan.Node{node("n0", "weather", w.Version), node("n1", "weather", echo.Version)}},
		{Nodes: []plan.Node{{ID: "n0", Activity: treadle.ActivityName, Args: []string{"weather"}}}},
	} {
		if _, err := l.Pinned(ctx, draft.DefaultRegistry(), p); !errors.Is(err, treadle.ErrInvalid) {
			t.Errorf("plan %+v: %v", p.Nodes, err)
		}
	}
}

// script is a stand-in Messages API that answers each request with the
// next turn, and keeps what it was asked.
type script struct {
	mu    sync.Mutex
	turns []string
	asked []string
	srv   *httptest.Server
}

func newScript(t *testing.T, turns ...string) *script {
	s := &script{turns: turns}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.asked = append(s.asked, string(b))
		next := `{"id":"m","type":"message","role":"assistant","model":"claude-sonnet-5-5","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
		if len(s.turns) > 0 {
			next, s.turns = s.turns[0], s.turns[1:]
		}
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, next)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// fakePodman logs every call and what it's given on standard input.
func fakePodman(t *testing.T) (string, string) {
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	sh := `printf '%s\n' "$*" >> ` + log + `
case "$1" in
run) echo id;;
diff) echo '{}';;
exec) cat >> ` + log + `;;
esac`
	p := filepath.Join(dir, "podman")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+sh+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p, log
}

// TestLaw_SkillRunsArePinned: a program using a skill compiles to a plan
// pinning the skill's newest version; once the skill changes, that plan
// still checks, runs that version's files, and is recorded with the
// version's tree under uses/; a fresh compile pins the new version.
func TestLaw_SkillRunsArePinned(t *testing.T) {
	l := newLibrary(t)
	at := time.Unix(1700000000, 0)
	v1 := must(l.Add(ctx, skill("weather", "Say RAIN-V1."), at))
	reg := draft.DefaultRegistry()
	if _, err := l.AddTo(ctx, reg); err != nil {
		t.Fatal(err)
	}
	src := `(pipe (echo "Boston") (weather "today"))`
	p := must(loom.Compile(ctx, reg, src))
	if got := p.Nodes[1].Args; len(got) != 3 || got[0] != "weather" || got[1] != string(v1.Version) || got[2] != "today" {
		t.Fatalf("node args %q", got)
	}
	v2 := must(l.Add(ctx, skill("weather", "Say SUN-V2."), at.Add(time.Hour)))

	podman, log := fakePodman(t)
	api := newScript(t)
	agent := pirn.Agent{Claude: pirn.Claude{APIKey: "k", BaseURL: api.srv.URL}, Runner: bobbin.Runner{Podman: podman}, Image: strings.Repeat("0", 64)}
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	worker := draft.DefaultRegistry()
	if _, err := treadle.Register(ctx, worker, env, agent, l); err != nil {
		t.Fatal(err)
	}
	loom.RegisterWith(env, &loom.Checker{Registry: worker, RegistryFor: func(ctx context.Context, p plan.Plan) (*registry.Registry, error) {
		return l.Pinned(ctx, draft.DefaultRegistry(), p)
	}}, &loom.Recorder{Store: l.Store, Uses: l.Uses})
	env.ExecuteWorkflow(loom.RunWorkflowName, loom.RunInput{Program: src, Plan: p, Ref: loom.DefaultRef})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("the run pinned to v1: %v", err)
	}
	var res loom.RunResult
	must(0, env.GetWorkflowResult(&res))
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "Say RAIN-V1.") || strings.Contains(string(calls), "SUN-V2") {
		t.Errorf("the session didn't get v1's files:\n%s", calls)
	}
	if !strings.Contains(strings.Join(api.asked, ""), `Use the skill \"weather\" with these arguments: \"today\".`) {
		t.Errorf("Claude was asked: %s", api.asked)
	}
	tree, _ := must2(l.Store.Parents(ctx, res.Commit))
	var uses warp.ID
	for _, e := range must(l.Store.Tree(ctx, tree)) {
		if e.Name == loom.UsesDir {
			uses = e.ID
		}
	}
	if es := must(l.Store.Tree(ctx, uses)); len(es) != 1 || es[0].Name != "weather" || es[0].ID != v1.Version {
		t.Errorf("uses/ = %+v, want weather at v1", es)
	}
	if fresh := must(loom.Compile(ctx, worker, src)); fresh.Nodes[1].Args[1] != string(v2.Version) {
		t.Errorf("a fresh compile pins %s, want v2", fresh.Nodes[1].Args[1])
	}
}

// TestInv_UnknownVersionsFailAsSetup: a skill step whose version isn't in
// the library, or is another skill, fails with SkillSetup, non-retryable,
// before any container starts.
func TestInv_UnknownVersionsFailAsSetup(t *testing.T) {
	l := newLibrary(t)
	brief := must(l.Add(ctx, skill("brief", "x"), time.Unix(1700000000, 0)))
	podman, log := fakePodman(t)
	cmd := treadle.Command(pirn.Agent{Claude: pirn.Claude{APIKey: "k"}, Runner: bobbin.Runner{Podman: podman}, Image: strings.Repeat("0", 64)}, l)
	forged := must(l.Store.PutTree(ctx, []warp.Entry{{Name: "SKILL.md", Kind: warp.KindBlob, ID: must(l.Store.PutBlob(ctx, skill("brief", "forged")["SKILL.md"]))}}))
	for _, args := range [][]string{{"weather", strings.Repeat("ab", 20)}, {"weather", string(brief.Version)}, {"weather", "HEAD"}, {"brief", string(forged)}} {
		_, err := cmd(ctx, plan.Input{Args: args})
		var app *temporal.ApplicationError
		if !errors.As(err, &app) || app.Type() != "SkillSetup" || !app.NonRetryable() {
			t.Errorf("args %q: %v", args, err)
		}
	}
	if _, err := cmd(ctx, plan.Input{Args: []string{"weather"}}); err == nil {
		t.Error("a step with no version ran")
	}
	if b, _ := os.ReadFile(log); len(b) != 0 {
		t.Errorf("podman ran: %s", b)
	}
	if got := treadle.Instruction("weather", nil); got != `Use the skill "weather" on your inputs.` {
		t.Error(got)
	}
}

// TestInv_BrokenSkillsAreSkipped: a skill ref whose newest version isn't a
// valid skill of its name is listed as bad, and the others are still
// listed. (Review 13: one such ref made every listing, and so every
// worker and drawdown, fail.)
func TestInv_BrokenSkillsAreSkipped(t *testing.T) {
	l := newLibrary(t)
	at := time.Unix(1700000000, 0)
	w := must(l.Add(ctx, skill("weather", "x"), at))
	must(l.Add(ctx, skill("brief", "x"), at))
	if err := l.Store.Advance(ctx, treadle.RefPrefix+"zzz", "", w.Commit); err != nil {
		t.Fatal(err)
	}
	skills, bad, err := l.Skills(ctx)
	if err != nil || len(skills) != 2 || len(bad) != 1 || !strings.Contains(bad[0].Error(), "zzz") {
		t.Errorf("skills %+v, bad %v, err %v", skills, bad, err)
	}
}

// TestInv_ConcurrentAddsAgree: adding the same folder from many writers
// at once gives every one the same version, and one commit. (Review 13:
// seven of eight failed with a conflict.)
func TestInv_ConcurrentAddsAgree(t *testing.T) {
	l := newLibrary(t)
	var wg sync.WaitGroup
	got := make([]treadle.Skill, 8)
	errs := make([]error, 8)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i], errs[i] = l.Add(ctx, skill("weather", "same"), time.Unix(1700000000, 0))
		}(i)
	}
	wg.Wait()
	for i := range got {
		if errs[i] != nil || got[i].Version != got[0].Version {
			t.Errorf("writer %d: %+v %v", i, got[i], errs[i])
		}
	}
	if vs := must(l.Versions(ctx, "weather")); len(vs) != 1 {
		t.Errorf("%d versions", len(vs))
	}
}
