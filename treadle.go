// Package treadle keeps skills and turns each into a command.
//
// A skill is a folder whose SKILL.md says how to do a kind of work, with
// any scripts or references it needs, in the format of Anthropic's Agent
// Skills. treadle keeps every version of every skill in a warp store (a
// bare Git repository): skill name N lives on the ref refs/treadle/skills/N,
// each version a commit whose tree is the folder. A version is that tree's
// ID, so it names the folder's exact bytes.
//
// Each skill is a draft command named after it, (weather "Boston"), whose
// plan nodes run the activity "Skill" with two Bound arguments first: the
// skill's name and the version the plan pins. A step runs as one of pirn's
// agent steps, with the skill's folder in /work/skill.
package treadle

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/heedle-ai/draft"
	"github.com/heedle-ai/pirn"
	"github.com/heedle-ai/warp"
)

// RefPrefix is where skills live in a store: RefPrefix + name.
const RefPrefix = "refs/treadle/skills/"

// SkillFile is the file every skill has.
const SkillFile = pirn.SkillFile

// ErrInvalid wraps every refusal of a skill or a version. It wraps
// warp.ErrInvalid in turn, so loom's check treats it as permanent rather
// than retrying.
var ErrInvalid = fmt.Errorf("treadle: %w", warp.ErrInvalid)

// Skill is one version of a skill.
type Skill struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Version     warp.ID `json:"version"` // the folder's tree ID
	Commit      warp.ID `json:"commit"`  // where this version was added
}

var skillName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Meta reads a SKILL.md's front matter: the YAML between its first two
// "---" lines. name must be lower-case letters, digits and single hyphens,
// at most 64 characters, and not contain "anthropic" or "claude";
// description must be non-empty, at most 1024 characters, without "<" or
// ">". These are the rules of Anthropic's Agent Skills. Both must be YAML
// strings, the front matter ends at a line that is exactly "---", and
// treadle also refuses names draft reserves (draft.Reserved) and control
// characters in descriptions.
func Meta(skillMD []byte) (name, description string, err error) {
	bad := func(format string, a ...any) (string, string, error) {
		return "", "", fmt.Errorf("%w: %s: "+format, append([]any{ErrInvalid, SkillFile}, a...)...)
	}
	text := strings.ReplaceAll(string(skillMD), "\r\n", "\n")
	if !utf8.ValidString(text) {
		return bad("not UTF-8")
	}
	lines := strings.Split(text, "\n")
	if lines[0] != "---" {
		return bad("must start with a --- line and YAML front matter")
	}
	end := -1
	for i := 1; i < len(lines) && end < 0; i++ {
		if strings.TrimRight(lines[i], " \t") == "---" {
			end = i
		}
	}
	if end < 0 {
		return bad("its front matter has no closing --- line")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &doc); err != nil {
		return bad("front matter: %v", err)
	}
	// name and description must be YAML strings: not numbers, booleans,
	// binary or anything else YAML would turn into one.
	var m struct{ Name, Description string }
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return bad("front matter must be a YAML mapping")
	}
	fields := doc.Content[0].Content
	for i := 0; i+1 < len(fields); i += 2 {
		k, v := fields[i], fields[i+1]
		var dst *string
		switch k.Value {
		case "name":
			dst = &m.Name
		case "description":
			dst = &m.Description
		default:
			continue
		}
		if v.Kind != yaml.ScalarNode || v.Tag != "!!str" {
			return bad("%s must be a string", k.Value)
		}
		*dst = v.Value
	}
	switch {
	case len(m.Name) > 64 || !skillName.MatchString(m.Name):
		return bad("name %q must be lower-case letters, digits and single hyphens, at most 64 characters", m.Name)
	case strings.Contains(m.Name, "anthropic") || strings.Contains(m.Name, "claude"):
		return bad("name %q may not contain \"anthropic\" or \"claude\"", m.Name)
	case draft.Reserved(m.Name):
		return bad("name %q is reserved by the language (a program could never call it)", m.Name)
	case strings.TrimSpace(m.Description) == "" || utf8.RuneCountInString(m.Description) > 1024 || strings.ContainsAny(m.Description, "<>") ||
		strings.ContainsFunc(m.Description, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }):
		return bad("description must be 1 to 1024 characters, without < or > or control characters")
	}
	return m.Name, strings.TrimSpace(m.Description), nil
}

// Check reports whether files are a skill pirn can run (pirn.CheckSkill),
// with a valid front matter, and returns its name and description.
func Check(files map[string][]byte) (name, description string, err error) {
	if err := pirn.CheckSkill(files); err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return Meta(files[SkillFile])
}

// Library is the skills a store keeps.
type Library struct {
	Store *warp.Store
}

// Add keeps files as the newest version of their skill, named by its
// SKILL.md, and returns it. Adding the version a skill already has
// changes nothing and returns it. at is the commit's time: pass a fixed
// time, not now, for the same commit from the same files.
func (l *Library) Add(ctx context.Context, files map[string][]byte, at time.Time) (Skill, error) {
	name, desc, err := Check(files)
	if err != nil {
		return Skill{}, err
	}
	tree, err := putDir(ctx, l.Store, files, "")
	if err != nil {
		return Skill{}, err
	}
	ref := RefPrefix + name
	// Another writer may move the ref between our read and our swap: read
	// it again and retry, so concurrent adds of a folder all agree.
	for try := 0; ; try++ {
		head, exists, err := l.Store.Resolve(ctx, ref)
		if err != nil {
			return Skill{}, err
		}
		var parents []warp.ID
		if exists {
			t, _, err := l.Store.Parents(ctx, head)
			if err != nil {
				return Skill{}, err
			}
			if t == tree {
				return Skill{Name: name, Description: desc, Version: tree, Commit: head}, nil
			}
			parents = []warp.ID{head}
		}
		commit, err := l.Store.Commit(ctx, warp.CommitSpec{Tree: tree, Parents: parents, Message: "skill " + name,
			Author: warp.Signature{Name: "treadle", Email: "treadle@heedle.ai", When: at}})
		if err != nil {
			return Skill{}, err
		}
		err = l.Store.Advance(ctx, ref, head, commit)
		if errors.Is(err, warp.ErrConflict) && try < 10 {
			continue
		}
		if err != nil {
			return Skill{}, err
		}
		return Skill{Name: name, Description: desc, Version: tree, Commit: commit}, nil
	}
}

// Skills lists the newest version of every skill, by name. A ref whose
// newest version isn't a valid skill of its name is left out and listed
// in bad, so one broken skill never hides the others.
func (l *Library) Skills(ctx context.Context) (skills []Skill, bad []error, err error) {
	refs, err := l.Store.Refs(ctx, RefPrefix)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range refs {
		tree, _, err := l.Store.Parents(ctx, r.Commit)
		if err != nil {
			return nil, nil, err
		}
		s, err := l.describe(ctx, tree)
		switch {
		case errors.Is(err, ErrInvalid):
			bad = append(bad, fmt.Errorf("%s: %w", r.Name, err))
			continue
		case err != nil:
			return nil, nil, err
		case RefPrefix+s.Name != r.Name:
			bad = append(bad, fmt.Errorf("%w: %s holds the skill %q", ErrInvalid, r.Name, s.Name))
			continue
		}
		s.Commit = r.Commit
		skills = append(skills, s)
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return skills, bad, nil
}

// version checks that version was added as a version of the skill name
// (it is the tree of a commit on that skill's ref) and returns its
// folder, checked. Any other tree in the store, even one holding a
// SKILL.md of that name, is refused.
func (l *Library) version(ctx context.Context, name string, version warp.ID) (map[string][]byte, error) {
	if !skillName.MatchString(name) {
		return nil, fmt.Errorf("%w: skill name %q", ErrInvalid, name)
	}
	head, ok, err := l.Store.Resolve(ctx, RefPrefix+name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: this worker has no skill %q", ErrInvalid, name)
	}
	log, err := l.Store.Log(ctx, head, 0)
	if err != nil {
		return nil, err
	}
	added := false
	for _, c := range log {
		added = added || c.Tree == version
	}
	if !added {
		return nil, fmt.Errorf("%w: %s was never added as a version of the skill %q", ErrInvalid, version, name)
	}
	files, err := l.Files(ctx, version)
	if err != nil {
		return nil, err
	}
	if got, _, _ := Meta(files[SkillFile]); got != name {
		return nil, fmt.Errorf("%w: version %s is the skill %q, not %q", ErrInvalid, version, got, name)
	}
	return files, nil
}

// Versions lists every version of a skill, newest first.
func (l *Library) Versions(ctx context.Context, name string) ([]Skill, error) {
	if !skillName.MatchString(name) {
		return nil, fmt.Errorf("%w: skill name %q", ErrInvalid, name)
	}
	head, ok, err := l.Store.Resolve(ctx, RefPrefix+name)
	if err != nil || !ok {
		return nil, err
	}
	log, err := l.Store.Log(ctx, head, 0)
	if err != nil {
		return nil, err
	}
	var out []Skill
	for _, c := range log {
		tree, _, err := l.Store.Parents(ctx, c.ID)
		if err != nil {
			return nil, err
		}
		s, err := l.describe(ctx, tree)
		if err != nil {
			return nil, err
		}
		s.Commit = c.ID
		out = append(out, s)
	}
	return out, nil
}

// Files returns a version's folder, checked as Add checks it.
func (l *Library) Files(ctx context.Context, version warp.ID) (map[string][]byte, error) {
	list, err := l.Store.Files(ctx, version)
	if err != nil {
		return nil, err
	}
	if len(list) > pirn.MaxSkillFiles {
		return nil, fmt.Errorf("%w: version %s has %d files", ErrInvalid, version, len(list))
	}
	ids := make([]warp.ID, len(list))
	var total int64
	for i, f := range list {
		ids[i], total = f.ID, total+f.Size
	}
	if total > pirn.MaxSkillBytes {
		return nil, fmt.Errorf("%w: version %s holds %d bytes", ErrInvalid, version, total)
	}
	data, err := l.Store.Blobs(ctx, ids)
	if err != nil {
		return nil, err
	}
	files := make(map[string][]byte, len(list))
	for i, f := range list {
		files[f.Path] = data[i]
	}
	if _, _, err := Check(files); err != nil {
		return nil, err
	}
	return files, nil
}

// describe reads a version's name and description from its SKILL.md.
func (l *Library) describe(ctx context.Context, tree warp.ID) (Skill, error) {
	es, err := l.Store.Tree(ctx, tree)
	if err != nil {
		return Skill{}, err
	}
	for _, e := range es {
		if e.Name == SkillFile && e.Kind == warp.KindBlob {
			b, err := l.Store.Blob(ctx, e.ID)
			if err != nil {
				return Skill{}, err
			}
			name, desc, err := Meta(b)
			if err != nil {
				return Skill{}, err
			}
			return Skill{Name: name, Description: desc, Version: tree}, nil
		}
	}
	return Skill{}, fmt.Errorf("%w: version %s has no %s", ErrInvalid, tree, SkillFile)
}

// putDir stores the files under prefix (relative paths, "/"-separated) as
// a tree of trees.
func putDir(ctx context.Context, s *warp.Store, files map[string][]byte, prefix string) (warp.ID, error) {
	subdirs := map[string]bool{}
	var es []warp.Entry
	for p, data := range files {
		rest, ok := strings.CutPrefix(p, prefix)
		if !ok {
			continue
		}
		if name, _, nested := strings.Cut(rest, "/"); nested {
			subdirs[name] = true
			continue
		}
		id, err := s.PutBlob(ctx, bytes.Clone(data))
		if err != nil {
			return "", err
		}
		es = append(es, warp.Entry{Name: rest, Kind: warp.KindBlob, ID: id})
	}
	for name := range subdirs {
		if _, clash := files[prefix+name]; clash {
			return "", fmt.Errorf("%w: %s is both a file and a folder", ErrInvalid, prefix+name)
		}
		id, err := putDir(ctx, s, files, prefix+name+"/")
		if err != nil {
			return "", err
		}
		es = append(es, warp.Entry{Name: name, Kind: warp.KindTree, ID: id})
	}
	return s.PutTree(ctx, es)
}
