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
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/heedle-ai/pirn"
	"github.com/heedle-ai/warp"
)

// RefPrefix is where skills live in a store: RefPrefix + name.
const RefPrefix = "refs/treadle/skills/"

// SkillFile is the file every skill has.
const SkillFile = pirn.SkillFile

// ErrInvalid wraps every refusal of a skill or a version.
var ErrInvalid = errors.New("treadle: invalid")

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
// ">". These are the rules of Anthropic's Agent Skills.
func Meta(skillMD []byte) (name, description string, err error) {
	bad := func(format string, a ...any) (string, string, error) {
		return "", "", fmt.Errorf("%w: %s: "+format, append([]any{ErrInvalid, SkillFile}, a...)...)
	}
	text := strings.ReplaceAll(string(skillMD), "\r\n", "\n")
	if !utf8.ValidString(text) {
		return bad("not UTF-8")
	}
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return bad("must start with a --- line and YAML front matter")
	}
	front, _, ok := strings.Cut(rest, "\n---")
	if !ok {
		return bad("its front matter has no closing --- line")
	}
	var m struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(front), &m); err != nil {
		return bad("front matter: %v", err)
	}
	switch {
	case len(m.Name) > 64 || !skillName.MatchString(m.Name):
		return bad("name %q must be lower-case letters, digits and single hyphens, at most 64 characters", m.Name)
	case strings.Contains(m.Name, "anthropic") || strings.Contains(m.Name, "claude"):
		return bad("name %q may not contain \"anthropic\" or \"claude\"", m.Name)
	case strings.TrimSpace(m.Description) == "" || utf8.RuneCountInString(m.Description) > 1024 || strings.ContainsAny(m.Description, "<>"):
		return bad("description must be 1 to 1024 characters, without < or >")
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
	if err := l.Store.Advance(ctx, ref, head, commit); err != nil {
		return Skill{}, err
	}
	return Skill{Name: name, Description: desc, Version: tree, Commit: commit}, nil
}

// Skills lists the newest version of every skill, by name.
func (l *Library) Skills(ctx context.Context) ([]Skill, error) {
	refs, err := l.Store.Refs(ctx, RefPrefix)
	if err != nil {
		return nil, err
	}
	var out []Skill
	for _, r := range refs {
		tree, _, err := l.Store.Parents(ctx, r.Commit)
		if err != nil {
			return nil, err
		}
		s, err := l.describe(ctx, tree)
		if err != nil {
			return nil, err
		}
		if RefPrefix+s.Name != r.Name {
			return nil, fmt.Errorf("%w: %s holds the skill %q", ErrInvalid, r.Name, s.Name)
		}
		s.Commit = r.Commit
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
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
