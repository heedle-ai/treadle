package treadle

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/heedle-ai/pirn"
	"github.com/heedle-ai/warp"
)

// Imports wait for review unless their repository is trusted. An import
// waiting for review lives on PendingPrefix + name: it isn't a version of
// the skill, so no command runs it and no plan can pin it, until Approve
// adds exactly the version that was reviewed.
const (
	PendingPrefix = "refs/treadle/pending/"
	// ConfigRef holds the library's settings: a tree whose file
	// TrustedFile lists trusted prefixes, one per line.
	ConfigRef   = "refs/treadle/config"
	TrustedFile = "trusted"
	// TrailerApprovedFrom, on a version's commit, names the pending
	// commit it was approved from.
	TrailerApprovedFrom = "Skill-Approved-From"
	// TrailerApproved and TrailerRejected, on a pending commit, close the
	// review of the version they name.
	TrailerApproved = "Skill-Approved"
	TrailerRejected = "Skill-Rejected"
)

// checkPrefix reports whether p can be trusted: a repository URL as
// Source accepts one, with at least one path segment after the host, so
// no prefix trusts a whole host.
func checkPrefix(p string) error {
	if !repoURL.MatchString(p) || !plainSegments(p) {
		return fmt.Errorf("%w: trusted prefix %q (want an https:// or file:// URL with a path, such as https://github.com/your-org, without a trailing /)", ErrInvalid, redact(p))
	}
	return nil
}

// plainSegments reports whether the path of url (after its host) is
// segments that are neither empty nor "." or "..": a URL that names the
// same place in one way only, so no prefix can match more than it says
// (review 16: file:///. trusted every folder, and https://github.com/.
// would have trusted the whole host).
func plainSegments(url string) bool {
	_, rest, ok := strings.Cut(url, "://")
	if !ok {
		return false
	}
	segs := strings.Split(rest, "/")
	if len(segs) < 2 {
		return false
	}
	for _, seg := range segs[1:] {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// trustedBy reports whether repo is under one of prefixes: equal to it, or
// inside it at a "/", so https://github.com/acme trusts
// https://github.com/acme/skills but not https://github.com/acme-evil/x.
func trustedBy(prefixes []string, repo string) bool {
	for _, p := range prefixes {
		if checkPrefix(p) == nil && (repo == p || strings.HasPrefix(repo, p+"/")) {
			return true
		}
	}
	return false
}

// Trusted lists the trusted prefixes, sorted.
func (l *Library) Trusted(ctx context.Context) ([]string, error) {
	_, list, err := l.trusted(ctx)
	return list, err
}

func (l *Library) trusted(ctx context.Context) (head warp.ID, list []string, err error) {
	head, ok, err := l.Store.Resolve(ctx, ConfigRef)
	if err != nil || !ok {
		return "", nil, err
	}
	tree, _, err := l.Store.Parents(ctx, head)
	if err != nil {
		return "", nil, err
	}
	es, err := l.Store.Tree(ctx, tree)
	if err != nil {
		return "", nil, err
	}
	for _, e := range es {
		if e.Name == TrustedFile && e.Kind == warp.KindBlob {
			b, err := l.Store.Blob(ctx, e.ID)
			if err != nil {
				return "", nil, err
			}
			for _, line := range strings.Split(string(b), "\n") {
				if line = strings.TrimSpace(line); line != "" {
					list = append(list, line)
				}
			}
		}
	}
	return head, list, nil
}

// Trust adds prefix to the trusted prefixes: imports from a repository
// under it become versions without review.
func (l *Library) Trust(ctx context.Context, prefix string, at time.Time) error {
	if err := checkPrefix(prefix); err != nil {
		return err
	}
	return l.setTrusted(ctx, at, func(list []string) []string {
		if slices.Contains(list, prefix) {
			return list
		}
		return append(list, prefix)
	})
}

// Untrust removes prefix, exactly as Trusted lists it, from the trusted
// prefixes. Versions already imported stay; later imports wait for
// review. A prefix that isn't trusted is an error, never a silent no-op,
// so a misspelling can't leave one trusted.
func (l *Library) Untrust(ctx context.Context, prefix string, at time.Time) error {
	list, err := l.Trusted(ctx)
	if err != nil {
		return err
	}
	if !slices.Contains(list, prefix) {
		return fmt.Errorf("%w: %q isn't a trusted prefix (the trusted ones are %q)", warp.ErrNotFound, redact(prefix), list)
	}
	return l.setTrusted(ctx, at, func(list []string) []string {
		return slices.DeleteFunc(list, func(p string) bool { return p == prefix })
	})
}

func (l *Library) setTrusted(ctx context.Context, at time.Time, change func([]string) []string) error {
	for try := 0; ; try++ {
		head, old, err := l.trusted(ctx)
		if err != nil {
			return err
		}
		list := change(slices.Clone(old))
		slices.Sort(list)
		list = slices.Compact(list)
		if slices.Equal(list, old) {
			return nil
		}
		text := strings.Join(list, "\n")
		if text != "" {
			text += "\n"
		}
		blob, err := l.Store.PutBlob(ctx, []byte(text))
		if err != nil {
			return err
		}
		tree, err := l.Store.PutTree(ctx, []warp.Entry{{Name: TrustedFile, Kind: warp.KindBlob, ID: blob}})
		if err != nil {
			return err
		}
		var parents []warp.ID
		if head != "" {
			parents = []warp.ID{head}
		}
		commit, err := l.Store.Commit(ctx, warp.CommitSpec{Tree: tree, Parents: parents, Message: "trusted prefixes",
			Author: warp.Signature{Name: "treadle", Email: "treadle@heedle.ai", When: at}})
		if err != nil {
			return err
		}
		err = l.Store.Advance(ctx, ConfigRef, head, commit)
		if errors.Is(err, warp.ErrConflict) && try < 10 {
			continue
		}
		return err
	}
}

// newest is the tree of the skill's newest version, or "" if it has
// none.
func (l *Library) newest(ctx context.Context, name string) (warp.ID, error) {
	head, ok, err := l.Store.Resolve(ctx, RefPrefix+name)
	if err != nil || !ok {
		return "", err
	}
	tree, _, err := l.Store.Parents(ctx, head)
	return tree, err
}

// propose keeps files on their skill's pending ref, waiting for review.
// Proposing the skill's newest version changes nothing and returns it;
// any other version, even an older one, waits, since adding it would
// change what the skill runs.
func (l *Library) propose(ctx context.Context, files map[string][]byte, at time.Time, src *Source, extra ...warp.Trailer) (Skill, error) {
	name, desc, err := Check(files)
	if err != nil {
		return Skill{}, err
	}
	tree, err := putDir(ctx, l.Store, files, "")
	if err != nil {
		return Skill{}, err
	}
	if head, ok, err := l.Store.Resolve(ctx, RefPrefix+name); err != nil {
		return Skill{}, err
	} else if ok {
		log, err := l.Store.Log(ctx, head, 1)
		if err != nil {
			return Skill{}, err
		}
		if log[0].Tree == tree {
			return Skill{Name: name, Description: desc, Version: tree, Commit: head, Source: sourceOf(log[0].Trailers)}, nil
		}
	}
	ref := PendingPrefix + name
	for try := 0; ; try++ {
		head, exists, err := l.Store.Resolve(ctx, ref)
		if err != nil {
			return Skill{}, err
		}
		var parents []warp.ID
		if exists {
			log, err := l.Store.Log(ctx, head, 1)
			if err != nil {
				return Skill{}, err
			}
			if log[0].Tree == tree && !decided(log[0].Trailers) {
				return Skill{Name: name, Description: desc, Version: tree, Commit: head, Source: sourceOf(log[0].Trailers), Pending: true, Created: createdOf(log[0].Trailers)}, nil
			}
			parents = []warp.ID{head}
		}
		commit, err := l.Store.Commit(ctx, warp.CommitSpec{Tree: tree, Parents: parents, Message: "pending skill " + name,
			Trailers: append(sourceTrailers(src), extra...), Author: warp.Signature{Name: "treadle", Email: "treadle@heedle.ai", When: at}})
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
		return Skill{Name: name, Description: desc, Version: tree, Commit: commit, Source: src, Pending: true, Created: createdOf(extra)}, nil
	}
}

// decided reports whether a pending commit closes its review: an
// approval or a rejection.
func decided(ts []warp.Trailer) bool {
	return slices.ContainsFunc(ts, func(t warp.Trailer) bool { return t.Key == TrailerRejected || t.Key == TrailerApproved })
}

// pending returns the import of name waiting for review, if one is: the
// head of its pending ref, unless that head records a decision (approved
// or rejected) or is already the skill's newest version.
func (l *Library) pending(ctx context.Context, name string) (Skill, bool, error) {
	if !validName(name) {
		return Skill{}, false, fmt.Errorf("%w: skill name %q", ErrInvalid, name)
	}
	head, ok, err := l.Store.Resolve(ctx, PendingPrefix+name)
	if err != nil || !ok {
		return Skill{}, false, err
	}
	log, err := l.Store.Log(ctx, head, 1)
	if err != nil {
		return Skill{}, false, err
	}
	c := log[0]
	if decided(c.Trailers) {
		return Skill{}, false, nil
	}
	if cur, err := l.newest(ctx, name); err != nil || cur == c.Tree {
		return Skill{}, false, err
	}
	s, err := l.describe(ctx, c.Tree)
	if err != nil {
		return Skill{}, false, err
	}
	if s.Name != name {
		return Skill{}, false, fmt.Errorf("%w: %s holds the skill %q", ErrInvalid, PendingPrefix+name, s.Name)
	}
	s.Commit, s.Source, s.Pending, s.Created = c.ID, sourceOf(c.Trailers), true, createdOf(c.Trailers)
	return s, true, nil
}

// Pending lists the imports waiting for review, by name. One that isn't a
// valid skill of its name is listed in bad instead.
func (l *Library) Pending(ctx context.Context) (skills []Skill, bad []error, err error) {
	refs, err := l.Store.Refs(ctx, PendingPrefix)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range refs {
		s, ok, err := l.pending(ctx, strings.TrimPrefix(r.Name, PendingPrefix))
		switch {
		case errors.Is(err, ErrInvalid):
			bad = append(bad, fmt.Errorf("%s: %w", r.Name, err))
		case err != nil:
			return nil, nil, err
		case ok:
			skills = append(skills, s)
		}
	}
	return skills, bad, nil
}

// Review is what a reviewer reads before approving an import.
type Review struct {
	Pending Skill
	Files   []warp.File // every file, with its size
	// SkillMD is the import's SKILL.md, in full: read it, whatever the
	// diff shows.
	SkillMD []byte
	// Replaces is the tree of the skill's newest version, which the diff
	// is from and Approve must be given, or "" for a new skill. Current
	// is that version, if it's a valid one.
	Replaces warp.ID
	Current  *Skill
	// Diff is the unified diff (git diff) from Replaces to Pending, or
	// from nothing for a new skill, so every text file's change is in it.
	Diff []byte
	// Binary lists the files whose change the diff doesn't show, because
	// git takes them for binary (a NUL byte is enough): read them some
	// other way before approving.
	Binary []string
}

var binaryLine = regexp.MustCompile(`(?m)^Binary files (?:/dev/null|a/.+) and b/(.+) differ$`)

// Review returns the import of name waiting for review, with its files,
// its SKILL.md and its diff from the skill's newest version.
func (l *Library) Review(ctx context.Context, name string) (Review, error) {
	p, ok, err := l.pending(ctx, name)
	if err != nil {
		return Review{}, err
	}
	if !ok {
		return Review{}, fmt.Errorf("%w: no import of %q is waiting for review", warp.ErrNotFound, name)
	}
	r := Review{Pending: p}
	if r.Files, err = l.Store.Files(ctx, p.Version); err != nil {
		return Review{}, err
	}
	var total int64
	for _, f := range r.Files {
		total += f.Size
	}
	if len(r.Files) > pirn.MaxSkillFiles || total > pirn.MaxSkillBytes {
		return Review{}, fmt.Errorf("%w: the import of %q waiting for review isn't within pirn's limits", ErrInvalid, name)
	}
	for _, f := range r.Files {
		if f.Path == SkillFile && f.Size <= MaxSkillMD {
			if r.SkillMD, err = l.Store.Blob(ctx, f.ID); err != nil {
				return Review{}, err
			}
		}
	}
	if r.Replaces, err = l.newest(ctx, name); err != nil {
		return Review{}, err
	}
	if cur, err := l.Versions(ctx, name); err != nil {
		return Review{}, err
	} else if len(cur) > 0 && cur[0].Version == r.Replaces {
		r.Current = &cur[0]
	}
	if r.Diff, err = l.Store.DiffTrees(ctx, r.Replaces, p.Version); err != nil {
		return Review{}, err
	}
	for _, m := range binaryLine.FindAllSubmatch(r.Diff, -1) {
		r.Binary = append(r.Binary, string(m[1]))
	}
	return r, nil
}

// waiting returns the import of name waiting for review, and refuses
// unless it is exactly version: a decision binds the version reviewed,
// never a later import.
func (l *Library) waiting(ctx context.Context, name string, version warp.ID) (Skill, error) {
	p, ok, err := l.pending(ctx, name)
	if err != nil {
		return Skill{}, err
	}
	if !ok {
		return Skill{}, fmt.Errorf("%w: no import of %q is waiting for review", warp.ErrNotFound, name)
	}
	if p.Version != version {
		return Skill{}, fmt.Errorf("%w: the import of %q waiting for review is %s, not %s: review it again", ErrInvalid, name, p.Version, version)
	}
	return p, nil
}

// decide closes the review of p with a commit carrying trailer, moving
// the pending ref only if it still points at p: of an approval and a
// rejection made at once, exactly one wins.
func (l *Library) decide(ctx context.Context, p Skill, trailer string, at time.Time) (warp.ID, error) {
	commit, err := l.Store.Commit(ctx, warp.CommitSpec{Tree: p.Version, Parents: []warp.ID{p.Commit}, Message: strings.ToLower(trailer) + " " + p.Name,
		Trailers: []warp.Trailer{{Key: trailer, Value: string(p.Version)}}, Author: warp.Signature{Name: "treadle", Email: "treadle@heedle.ai", When: at}})
	if err != nil {
		return "", err
	}
	err = l.Store.Advance(ctx, PendingPrefix+p.Name, p.Commit, commit)
	if errors.Is(err, warp.ErrConflict) {
		return "", fmt.Errorf("%w: the review of %q changed while deciding it: review it again", ErrInvalid, p.Name)
	}
	return commit, err
}

// Approve adds the import of name waiting for review as the skill's
// newest version, if it is exactly version and the skill's newest version
// is still replaces, the one Review diffed it against ("" for a new
// skill). The version's commit has the import's source and
// TrailerApprovedFrom, and its source is the import waiting's: the
// version binds the bytes reviewed, and the source recorded is that of
// the import that brought them most recently. The review is closed
// first, so an approval and a rejection never both succeed; if the skill
// changes before the version is added, the review is reopened and
// Approve fails.
func (l *Library) Approve(ctx context.Context, name string, version, replaces warp.ID, at time.Time) (Skill, error) {
	p, err := l.waiting(ctx, name, version)
	if err != nil {
		return Skill{}, err
	}
	if cur, err := l.newest(ctx, name); err != nil {
		return Skill{}, err
	} else if cur != replaces {
		return Skill{}, fmt.Errorf("%w: %q's newest version is %s, not %s, the one the review showed: review it again", ErrInvalid, name, orNone(cur), orNone(replaces))
	}
	files, err := l.Files(ctx, version)
	if err != nil {
		return Skill{}, err
	}
	closed, err := l.decide(ctx, p, TrailerApproved, at)
	if err != nil {
		return Skill{}, err
	}
	extra := []warp.Trailer{{Key: TrailerApprovedFrom, Value: string(p.Commit)}}
	if p.Created != "" {
		extra = append(extra, warp.Trailer{Key: TrailerCreatedFrom, Value: p.Created})
	}
	sk, err := l.add(ctx, files, at, p.Source, extra, &replaces)
	if err != nil {
		// The skill changed between the check and the add (review 17:
		// the review stayed closed, saying approved). Reopen it, so it
		// waits again, to be reviewed against the new newest version.
		reopen, cerr := l.Store.Commit(ctx, warp.CommitSpec{Tree: p.Version, Parents: []warp.ID{closed}, Message: "reopened skill " + name,
			Trailers: sourceTrailers(p.Source), Author: warp.Signature{Name: "treadle", Email: "treadle@heedle.ai", When: at}})
		if cerr == nil {
			cerr = l.Store.Advance(ctx, PendingPrefix+name, closed, reopen)
		}
		return Skill{}, errors.Join(err, cerr)
	}
	return sk, nil
}

func orNone(v warp.ID) string {
	if v == "" {
		return "none"
	}
	return string(v)
}

// Reject closes the review of the import of name waiting for review, if
// it is exactly version. Importing it again opens a new review.
func (l *Library) Reject(ctx context.Context, name string, version warp.ID, at time.Time) error {
	p, err := l.waiting(ctx, name, version)
	if err != nil {
		return err
	}
	_, err = l.decide(ctx, p, TrailerRejected, at)
	return err
}
