package treadle_test

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/heedle-ai/pirn"
	"github.com/heedle-ai/treadle"
)

// TestInv_CreatedSkillsWaitForReview: a skill created from a description
// is what the agent left in /work/out, checked as any skill is; the agent
// is told the description and to write and pass a test; a folder that
// isn't a valid skill, or lost files, is refused; and the created skill
// waits for review like an untrusted import, carrying its description,
// which its version keeps once approved.
func TestInv_CreatedSkillsWaitForReview(t *testing.T) {
	var asked pirn.Question
	agent := func(files ...pirn.File) func(context.Context, pirn.Question) (pirn.AgentResult, error) {
		return func(_ context.Context, q pirn.Question) (pirn.AgentResult, error) {
			asked = q
			return pirn.AgentResult{Files: files}, nil
		}
	}
	md := pirn.File{Path: "SKILL.md", Text: "---\nname: csv-sum\ndescription: Sums a CSV column.\n---\nRun scripts/sum.sh.\n"}
	script := pirn.File{Path: "scripts/sum.sh", Text: "awk -F, '{s+=$2} END {print s}' \"$1\"\n"}
	test := pirn.File{Path: "tests/test.sh", Base64: base64.StdEncoding.EncodeToString([]byte("test \"$(sh scripts/sum.sh x)\" = 3\n"))}
	files, _, err := treadle.Create(ctx, agent(md, script, test), "Sum the second column of a CSV file.")
	if err != nil || len(files) != 3 || string(files["tests/test.sh"]) != "test \"$(sh scripts/sum.sh x)\" = 3\n" {
		t.Fatalf("%q %v", files, err)
	}
	if !strings.Contains(asked.Instruction, "Sum the second column of a CSV file.") || !strings.Contains(asked.Instruction, "/work/out/tests/test.sh") || asked.Skill != nil || len(asked.Grant) != 0 {
		t.Errorf("the agent was asked %+v", asked)
	}
	for _, bad := range []struct {
		files []pirn.File
		desc  string
	}{
		{[]pirn.File{script}, "x"},
		{[]pirn.File{{Path: "SKILL.md", Text: "no front matter"}}, "x"},
		{[]pirn.File{md, {Path: "big.bin", OCI: "ghcr.io/a/b", SHA256: strings.Repeat("0", 64)}}, "x"},
		{[]pirn.File{md}, ""},
		{[]pirn.File{md}, strings.Repeat("x", treadle.MaxCreateDescription+1)},
		// Review 20: refused before the agent runs, not after.
		{[]pirn.File{md}, "sum\u0007it"},
		{[]pirn.File{md}, "sum \u202egnp.exe"},
		{[]pirn.File{md}, "a\xc3"},
	} {
		if _, _, err := treadle.Create(ctx, agent(bad.files...), bad.desc); !errors.Is(err, treadle.ErrInvalid) {
			t.Errorf("created from %+v: %v", bad.files, err)
		}
	}
	lost := func(context.Context, pirn.Question) (pirn.AgentResult, error) {
		return pirn.AgentResult{Files: []pirn.File{md}, Skipped: []string{"scripts/huge.sh: too big"}}, nil
	}
	ran := false
	never := func(context.Context, pirn.Question) (pirn.AgentResult, error) { ran = true; return pirn.AgentResult{}, nil }
	if _, _, err := treadle.Create(ctx, never, "sum\u0007it"); !errors.Is(err, treadle.ErrInvalid) || ran {
		t.Errorf("a bad description reached the agent: %v", err)
	}
	if _, _, err := treadle.Create(ctx, lost, "x"); !errors.Is(err, treadle.ErrInvalid) {
		t.Errorf("a folder with lost files: %v", err)
	}

	l := newLibrary(t)
	at := time.Unix(1700000000, 0)
	p := must(l.Propose(ctx, files, "Sum the second\ncolumn of a CSV file.", at))
	if !p.Pending || p.Created != "Sum the second column of a CSV file." {
		t.Errorf("proposed %+v", p)
	}
	if list, _, _ := l.Skills(ctx); len(list) != 0 {
		t.Errorf("a created skill is usable before review: %+v", list)
	}
	// Review 20: proposing the same files again reports what's stored.
	if again := must(l.Propose(ctx, files, "something else", at)); again.Created != p.Created {
		t.Errorf("a second proposal reported %q, stored %q", again.Created, p.Created)
	}
	for _, bad := range []string{"sum\u0007it", "x \u202e", strings.Repeat("é", treadle.MaxCreateDescription)} {
		if _, err := l.Propose(ctx, files, bad, at); !errors.Is(err, treadle.ErrInvalid) {
			t.Errorf("proposed with %q: %v", bad[:min(len(bad), 12)], err)
		}
	}
	r := must(l.Review(ctx, "csv-sum"))
	if r.Pending.Created != p.Created || !strings.Contains(string(r.Diff), "+awk") {
		t.Errorf("review %+v", r.Pending)
	}
	a := must(l.Approve(ctx, "csv-sum", p.Version, "", at))
	if vs := must(l.Versions(ctx, "csv-sum")); len(vs) != 1 || vs[0].Created != p.Created || a.Version != p.Version {
		t.Errorf("approved %+v", vs)
	}
}
