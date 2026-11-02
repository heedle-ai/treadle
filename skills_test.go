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

	"go.temporal.io/sdk/temporal"

	"github.com/heedle-ai/bobbin"
	"github.com/heedle-ai/loom/examples/hello/shipment"
	"github.com/heedle-ai/pirn"
	"github.com/heedle-ai/shuttle/plan"
	"github.com/heedle-ai/treadle"
)

// TestInv_FirstSkillsAreValid: the skills shipped in skills/ are skills
// treadle keeps (Check), with the names their folders have.
func TestInv_FirstSkillsAreValid(t *testing.T) {
	for _, name := range []string{"find-evidence", "assess-claim"} {
		files, err := treadle.ReadFolder(filepath.Join("skills", name))
		if err != nil {
			t.Fatal(err)
		}
		if got, _, err := treadle.Check(files); err != nil || got != name {
			t.Errorf("%s: %q %v", name, got, err)
		}
	}
}

// TestLaw_AssessSkillAgreesWithAssess: assess-claim's verdict script gives
// exactly what the hello world's assess command gives, on every subset of
// the hello world's sources and on reports that all say on-time; and every
// source report passes find-evidence's check, while malformed ones don't.
// So the skill version of the hello world reaches the same verdict.
func TestLaw_AssessSkillAgreesWithAssess(t *testing.T) {
	ctx := context.Background()
	reports := map[string]string{}
	for _, n := range []string{"weather", "carrier", "factory"} {
		out, err := shipment.Source(shipment.SourcesDir())(ctx, plan.Input{Args: []string{n}})
		if err != nil {
			t.Fatal(err)
		}
		reports[n] = out
	}
	reports["port"] = "port: on-time: Berths are clear.\n"
	names := []string{"weather", "carrier", "factory", "port"}
	claim := "the shipment will be late"
	for mask := 1; mask < 1<<len(names); mask++ {
		dir := t.TempDir()
		upstream := map[string]string{}
		var paths []string
		for i, n := range names {
			if mask&(1<<i) == 0 {
				continue
			}
			p := filepath.Join(dir, "n"+string(rune('0'+i))+".txt")
			os.WriteFile(p, []byte(reports[n]), 0o644)
			paths = append(paths, p)
			upstream["n"+string(rune('0'+i))] = reports[n]
		}
		want, err := shipment.Assess()(ctx, plan.Input{Args: []string{claim}, Upstream: upstream})
		if err != nil {
			t.Fatal(err)
		}
		got, err := exec.Command("sh", append([]string{"skills/assess-claim/scripts/assess.sh", claim}, paths...)...).Output()
		if err != nil || string(got) != want {
			t.Errorf("subset %b: script %q (%v), assess %q", mask, got, err, want)
		}
		check := exec.Command("sh", append([]string{"skills/find-evidence/scripts/check.sh"}, paths...)...)
		if out, err := check.CombinedOutput(); err != nil {
			t.Errorf("check refused a source report: %s", out)
		}
	}
	dir := t.TempDir()
	for _, bad := range []string{"weather delay", "Weather: delay: x", "weather: late: x", ": delay"} {
		p := filepath.Join(dir, "bad.txt")
		os.WriteFile(p, []byte(bad+"\n"), 0o644)
		if exec.Command("sh", "skills/find-evidence/scripts/check.sh", p).Run() == nil {
			t.Errorf("check accepted %q", bad)
		}
		if out, err := exec.Command("sh", "skills/assess-claim/scripts/assess.sh", claim, p).Output(); err == nil && !strings.Contains(bad, ": ") {
			t.Errorf("assess accepted %q: %s", bad, out)
		}
	}
}

// TestInv_SkillsDeclareTheirConnectors: a skill declares the connectors
// its steps may use as metadata.connectors in its front matter, read as a
// pirn grant; a malformed one refuses the skill; every way a skill is
// listed carries it; and a skill step is granted exactly that, so on a
// worker without those connectors it fails as setup before any container
// starts.
func TestInv_SkillsDeclareTheirConnectors(t *testing.T) {
	md := "---\nname: triage\ndescription: Triages bugs.\nmetadata:\n  connectors: \"linear/search_issues mail\"\n---\nGo.\n"
	g, err := treadle.Connectors([]byte(md))
	if err != nil || g.String() != "linear/search_issues mail" {
		t.Errorf("connectors %q %v", g, err)
	}
	if g, err := treadle.Connectors([]byte("---\nname: x\ndescription: y\n---\n")); err != nil || len(g) != 0 {
		t.Errorf("none: %q %v", g, err)
	}
	for _, bad := range []string{
		"metadata:\n  connectors: \"Linear\"\n",
		"metadata:\n  connectors: [linear]\n",
		"metadata:\n  connectors: 3\n",
		"metadata: linear\n",
		// Review 20: the first key won here, the last in most readers.
		"metadata:\n  connectors: \"mail\"\n  connectors: \"\"\n",
		"name: z\n",
	} {
		if _, _, err := treadle.Meta([]byte("---\nname: x\ndescription: y\n" + bad + "---\n")); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	l := newLibrary(t)
	sk := must(l.Add(ctx, map[string][]byte{"SKILL.md": []byte(md)}, time.Unix(1700000000, 0)))
	list, _, _ := l.Skills(ctx)
	vs := must(l.Versions(ctx, "triage"))
	folder, _, _, _ := l.Folder(ctx, "triage", sk.Version)
	if len(list) != 1 || list[0].Connectors != "linear/search_issues mail" || vs[0].Connectors != list[0].Connectors || folder.Connectors != list[0].Connectors {
		t.Errorf("listed %+v %+v %+v", list, vs, folder)
	}
	podman, log := fakePodman(t)
	cmd := treadle.Command(pirn.Agent{Claude: pirn.Claude{APIKey: "k"}, Runner: bobbin.Runner{Podman: podman}, Image: strings.Repeat("0", 64)}, l)
	_, err = cmd(ctx, plan.Input{Args: []string{"triage", string(sk.Version), "today"}})
	var app *temporal.ApplicationError
	if !errors.As(err, &app) || app.Type() != "AgentSetup" || !strings.Contains(err.Error(), "linear/search_issues") {
		t.Errorf("a skill's connectors on a worker without them: %v", err)
	}
	if b, _ := os.ReadFile(log); len(b) != 0 {
		t.Errorf("a container started: %s", b)
	}
}
