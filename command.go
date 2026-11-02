package treadle

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	"github.com/heedle-ai/draft/registry"
	"github.com/heedle-ai/pirn"
	"github.com/heedle-ai/shuttle"
	"github.com/heedle-ai/shuttle/plan"
	"github.com/heedle-ai/warp"
	"github.com/heedle-ai/weft"
)

// ActivityName is the Temporal activity every skill's steps run.
const ActivityName = "Skill"

// Spec is the draft command for one version of a skill: named after it,
// any number of string arguments, run on Temporal with pirn's
// AgentOptions, and Bound to the skill's name and version, so every plan
// node pins the version it was compiled with.
func Spec(s Skill) registry.BuiltinSpec {
	return registry.BuiltinSpec{
		Name:      s.Name,
		AgentID:   ActivityName,
		ArgSchema: registry.ArgSchema{Variadic: true, VariadicType: registry.StringT},
		Backends:  []registry.Backend{registry.BackendTemporal},
		Options:   pirn.AgentOptions,
		Bound:     []string{s.Name, string(s.Version)},
	}
}

// AddTo registers the newest version of every skill in reg. A skill that
// isn't valid, or is named like a command reg already has, is left out and
// listed in skipped, so one bad skill never stops a worker; err is only
// for failing to read the store.
func (l *Library) AddTo(ctx context.Context, reg *registry.Registry) (skipped []error, err error) {
	skills, skipped, err := l.Skills(ctx)
	if err != nil {
		return nil, err
	}
	for _, s := range skills {
		if _, taken := reg.Lookup(s.Name); taken {
			skipped = append(skipped, fmt.Errorf("%w: the skill %q has the name of a command this worker already has", ErrInvalid, s.Name))
			continue
		}
		if err := reg.Register(Spec(s)); err != nil {
			return nil, err
		}
	}
	return skipped, nil
}

// Pins are the skill versions a plan uses, by name.
func Pins(p plan.Plan) (map[string]warp.ID, error) {
	pins := map[string]warp.ID{}
	for _, n := range p.Nodes {
		if n.Activity != ActivityName {
			continue
		}
		if len(n.Args) < 2 {
			return nil, fmt.Errorf("%w: skill node %s has no name and version", ErrInvalid, n.ID)
		}
		name, version := n.Args[0], warp.ID(n.Args[1])
		if v, ok := pins[name]; ok && v != version {
			return nil, fmt.Errorf("%w: the plan pins the skill %q to two versions, %s and %s", ErrInvalid, name, v, version)
		}
		pins[name] = version
	}
	return pins, nil
}

// Pinned returns base with the skills p uses registered at the versions p
// pins: the registry p's program compiles to p with, if it does. Each
// pinned version must be in the library, and be the skill it's named as.
// loom's check uses it, so a run is checked against the skills it was
// compiled with, not whatever is newest when it runs.
func (l *Library) Pinned(ctx context.Context, base *registry.Registry, p plan.Plan) (*registry.Registry, error) {
	pins, err := Pins(p)
	if err != nil {
		return nil, err
	}
	for name, version := range pins {
		files, err := l.version(ctx, name, version)
		if err != nil {
			return nil, fmt.Errorf("the plan pins %s to %s: %w", name, version, err)
		}
		_, desc, _ := Meta(files[SkillFile])
		s := Skill{Name: name, Description: desc, Version: version}
		if _, taken := base.Lookup(name); taken {
			return nil, fmt.Errorf("%w: the skill %q has the name of a command this worker already has", ErrInvalid, name)
		}
		if err := base.Register(Spec(s)); err != nil {
			return nil, err
		}
	}
	return base, nil
}

// Command is the Skill activity as an arrow: Args[0] is the skill's
// name, Args[1] the pinned version and the rest the program's arguments.
// It runs one of a's agent steps (pirn.Step) with the version's folder in
// /work/skill, every upstream output as an input, and an instruction
// naming the skill and its arguments. A version that isn't in the
// library, or isn't the named skill, fails with the non-retryable type
// "SkillSetup"; otherwise errors are classified as pirn's agent steps'.
func Command(a pirn.Agent, l *Library) weft.Arrow[plan.Input, string] {
	return func(ctx context.Context, in plan.Input) (string, error) {
		if len(in.Args) < 2 {
			return "", temporal.NewNonRetryableApplicationError("a skill step needs its name and version", "InvalidInput", nil)
		}
		name, version, args := in.Args[0], warp.ID(in.Args[1]), in.Args[2:]
		files, reviewed, err := l.reviewedVersion(ctx, name, version)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if errors.Is(err, ErrInvalid) || errors.Is(err, warp.ErrInvalid) || errors.Is(err, warp.ErrNotFound) {
				return "", temporal.NewNonRetryableApplicationError(fmt.Sprintf("skill %s at %s: %v", name, version, err), "SkillSetup", nil)
			}
			return "", err
		}
		grant, err := Connectors(files[SkillFile])
		if err != nil {
			return "", temporal.NewNonRetryableApplicationError(fmt.Sprintf("skill %s at %s: %v", name, version, err), "SkillSetup", nil)
		}
		if len(grant) > 0 && !reviewed {
			return "", temporal.NewNonRetryableApplicationError(fmt.Sprintf("skill %s at %s asks for connectors (%s) but nobody reviewed it: import it again, and approve it", name, version, grant), "SkillSetup", nil)
		}
		return pirn.Step(ctx, a, pirn.Question{Instruction: Instruction(name, args), Sources: pirn.Sources(in.Upstream), Skill: files, Grant: grant})
	}
}

// Instruction is what a skill step asks of Claude.
func Instruction(name string, args []string) string {
	if len(args) == 0 {
		return fmt.Sprintf("Use the skill %q on your inputs.", name)
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = fmt.Sprintf("%q", a)
	}
	return fmt.Sprintf("Use the skill %q with these arguments: %s.", name, strings.Join(quoted, ", "))
}

// Activities is the part of a Temporal worker Register needs.
type Activities interface {
	RegisterActivityWithOptions(a any, options activity.RegisterOptions)
}

// Register adds every skill in l to reg (AddTo) and the Skill activity,
// running steps on a, to a worker, returning the skills it left out.
func Register(ctx context.Context, reg *registry.Registry, w Activities, a pirn.Agent, l *Library) (skipped []error, err error) {
	if skipped, err = l.AddTo(ctx, reg); err != nil {
		return nil, err
	}
	w.RegisterActivityWithOptions(shuttle.ArrowAsActivity(Command(a, l)), activity.RegisterOptions{Name: ActivityName})
	return skipped, nil
}

// Uses names, for loom's Recorder.Uses, each skill version a plan pins as
// an entry of the record's uses/ directory: the skill's name, and the
// version's tree, which must be in the library.
func (l *Library) Uses(ctx context.Context, p plan.Plan) ([]warp.Entry, error) {
	pins, err := Pins(p)
	if err != nil {
		return nil, err
	}
	var out []warp.Entry
	for name, version := range pins {
		if _, err := l.version(ctx, name, version); err != nil {
			return nil, err
		}
		out = append(out, warp.Entry{Name: name, Kind: warp.KindTree, ID: version})
	}
	return out, nil
}
