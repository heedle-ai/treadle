package treadle

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/heedle-ai/pirn"
	"github.com/heedle-ai/warp"
)

// TrailerCreatedFrom, on a version's commit, is the description it was
// created from (Library.Create).
const TrailerCreatedFrom = "Skill-Created-From"

// MaxCreateDescription bounds a description to create a skill from.
const MaxCreateDescription = 2 << 10

// CreateInstruction is what the agent creating a skill is told: the
// skill's description, then how to write it and its test.
func CreateInstruction(description string) string {
	return `Create an Agent Skill for this description, as a folder in /work/out:

` + description + `

- /work/out/SKILL.md starts with YAML front matter between --- lines: name (lower-case letters, digits and single hyphens, at most 64 characters) and description (what the skill does and when to use it, at most 1024 characters, without < or >; quote it if it has a colon). After the front matter come the instructions a later agent will follow, in Markdown.
- The skill's files will be in /work/skill when it runs, and the step's inputs in /work/in. Put any scripts in /work/out/scripts/; they'll be run with sh, so don't rely on file modes. Use only what a plain Linux container has.
- Write a test, /work/out/tests/test.sh, that runs the scripts on a small example in a temporary directory and exits non-zero if they're wrong. Run it with sh until it passes.
- Keep it small: at most 20 files, nothing hidden, no network.
Reply with the skill's name and one line on what the test checks.`
}

// CheckDescription returns a description to create a skill from, on one
// line, or ErrInvalid: it is 1 to MaxCreateDescription bytes of valid
// UTF-8 with no control or invisible format characters (Meta's rule for
// SKILL.md), since it is kept as a trailer and shown to reviewers
// (review 20).
func CheckDescription(description string) (string, error) {
	d := strings.Join(strings.Fields(description), " ")
	switch {
	case d == "" || len(d) > MaxCreateDescription:
		return "", fmt.Errorf("%w: a description is 1 to %d bytes", ErrInvalid, MaxCreateDescription)
	case !utf8.ValidString(d) || strings.ContainsFunc(d, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) && r != '\u200d' }):
		return "", fmt.Errorf("%w: a description must be plain text, without control or invisible characters", ErrInvalid)
	}
	return d, nil
}

// Create has an agent step (run: a pirn.Agent's Run) write a skill, and
// its test, from a description, and returns the folder, checked as Add
// checks it. The agent runs in a fresh container like any agent step; the
// files it leaves in /work/out are the skill.
func Create(ctx context.Context, run func(context.Context, pirn.Question) (pirn.AgentResult, error), description string) (map[string][]byte, pirn.AgentResult, error) {
	description, err := CheckDescription(description)
	if err != nil {
		return nil, pirn.AgentResult{}, err
	}
	res, err := run(ctx, pirn.Question{Instruction: CreateInstruction(description)})
	if err != nil {
		return nil, res, err
	}
	if len(res.Skipped) > 0 || res.SkippedMore > 0 {
		return nil, res, fmt.Errorf("%w: the skill's files weren't all kept: %s", ErrInvalid, strings.Join(res.Skipped, "; "))
	}
	files := map[string][]byte{}
	for _, f := range res.Files {
		switch {
		case f.OCI != "":
			return nil, res, fmt.Errorf("%w: %s is too big for a skill", ErrInvalid, f.Path)
		case f.Base64 != "":
			b, err := base64.StdEncoding.DecodeString(f.Base64)
			if err != nil {
				return nil, res, fmt.Errorf("%w: %s: %v", ErrInvalid, f.Path, err)
			}
			files[f.Path] = b
		default:
			files[f.Path] = []byte(f.Text)
		}
	}
	if _, _, err := Check(files); err != nil {
		return nil, res, err
	}
	return files, res, nil
}

// Propose keeps files, a skill folder created from description, waiting
// for review, as an untrusted import does: it becomes a version only when
// Approve names it. Its commit, and the version's once approved, carry
// the description (TrailerCreatedFrom).
func (l *Library) Propose(ctx context.Context, files map[string][]byte, description string, at time.Time) (Skill, error) {
	d, err := CheckDescription(description)
	if err != nil {
		return Skill{}, err
	}
	return l.propose(ctx, files, at, nil, warp.Trailer{Key: TrailerCreatedFrom, Value: d})
}

// createdOf is a commit's TrailerCreatedFrom, if any.
func createdOf(ts []warp.Trailer) string {
	for _, t := range ts {
		if t.Key == TrailerCreatedFrom {
			return t.Value
		}
	}
	return ""
}
