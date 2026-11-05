package treadle

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/heedle-ai/warp"
)

// Publishing (roadmap 10.6): a skill's newest approved version, written
// into a local clone of a repository of yours, at skills/<name>/, with a
// row in skills/README.md, and committed with the git CLI. treadle never
// pushes: you do, with the command Publish returns.

// TrailerVersion names, on the commit that publishes a skill, the
// version it came from.
const TrailerVersion = "Treadle-Version"

// PublishDir is the folder of a repository skills are published under.
const PublishDir = "skills"

// readme is the published skills' table, in PublishDir.
const readme = "README.md"

// The README's table lies between these lines; everything else in the
// file is yours and kept as it is.
const (
	tableStart = "<!-- treadle:skills -->"
	tableEnd   = "<!-- /treadle:skills -->"
)

// ErrNotApproved is a skill with no approved version to publish: only
// one waiting for review.
var ErrNotApproved = fmt.Errorf("%w: only an approved version can be published", ErrInvalid)

// ErrSecret is a skill whose files look like they hold a secret.
var ErrSecret = fmt.Errorf("%w: it looks like it holds a secret", ErrInvalid)

// Published is what Publish did.
type Published struct {
	Skill  Skill  `json:"skill"`
	Commit string `json:"commit"` // the repository's commit, or "" if it already had this version
	Push   string `json:"push"`   // the command that pushes it; treadle never does
}

// secrets are what a key check matches: Anthropic, GitHub, AWS, Slack,
// Stripe and Google keys, and private-key blocks.
var secrets = []struct {
	what string
	re   *regexp.Regexp
}{
	{"an Anthropic API key", regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{8,}`)},
	{"a GitHub token", regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})`)},
	{"an AWS access key", regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"an AWS secret key", regexp.MustCompile(`(?i)aws_?secret_?access_?key\s*[:=]\s*["']?[A-Za-z0-9/+=]{40}`)},
	{"a private key", regexp.MustCompile(`-----BEGIN ([A-Z0-9]+ )*PRIVATE KEY( BLOCK)?-----`)},
	{"a Slack token", regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`)},
	{"a Stripe secret key", regexp.MustCompile(`\b[rs]k_live_[A-Za-z0-9]{16,}`)},
	{"a Google API key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35,}`)},
	{"an OpenAI API key", regexp.MustCompile(`\bsk-(proj-)?[A-Za-z0-9_-]{32,}`)},
}

// SecretIn reports where files look like they hold a secret: the first
// file (by path) and line a key check matches, and what it looks like.
func SecretIn(files map[string][]byte) (file string, line int, what string, found bool) {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		for i, l := range bytes.Split(files[p], []byte("\n")) {
			for _, s := range secrets {
				if s.re.Match(l) {
					return p, i + 1, s.what, true
				}
			}
		}
	}
	return "", 0, "", false
}

// Publish writes the newest approved version of the skill name into the
// Git work tree dir (a clone of your repository, made by you), at
// skills/<name>/, exactly: the same files, bytes for bytes, as regular
// files (treadle keeps no modes). Publishing again replaces that folder;
// a skill removed from your library is never removed from the repository
// for you. It sets the skill's row in skills/README.md (name,
// description, connectors asked for, version), leaving every other row
// and line as they are, and commits those two paths, and nothing else
// you may have changed, with the git CLI and your git identity, with a
// TrailerVersion trailer. It never pushes; Published.Push is the
// command to.
//
// It refuses, before writing anything: a skill with no approved version
// (ErrNotApproved), files that look like they hold a secret (ErrSecret,
// naming the file and line), a file path that would leave the skill's
// folder, and a dir, skills/ or skills/<name>/ (or anything in it) that
// is a symbolic link. git runs without hooks, signing or prompts.
func (l *Library) Publish(ctx context.Context, name, dir string) (Published, error) {
	if !validName(name) {
		return Published{}, fmt.Errorf("%w: skill name %q", ErrInvalid, name)
	}
	var sk *Skill
	skills, _, err := l.Skills(ctx)
	if err != nil {
		return Published{}, err
	}
	for i := range skills {
		if skills[i].Name == name {
			sk = &skills[i]
		}
	}
	if sk == nil {
		if _, waiting, err := l.pending(ctx, name); err != nil {
			return Published{}, err
		} else if waiting {
			return Published{}, fmt.Errorf("%w: %s waits for review; approve it first", ErrNotApproved, name)
		}
		return Published{}, fmt.Errorf("%w: no skill %q", warp.ErrNotFound, name)
	}
	files, err := l.Files(ctx, sk.Version)
	if err != nil {
		return Published{}, err
	}
	if f, n, what, ok := SecretIn(files); ok {
		return Published{}, fmt.Errorf("%w: %s line %d looks like %s; take it out of the skill, then add it again", ErrSecret, f, n, what)
	}
	for p := range files {
		if !skillPath(p) || hidden(p) {
			return Published{}, fmt.Errorf("%w: %s would leave the skill's folder", ErrInvalid, p)
		}
	}

	// dir must be the top of a Git work tree, and not a link.
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Published{}, err
	}
	if fi, err := os.Lstat(abs); err != nil || !fi.IsDir() {
		return Published{}, fmt.Errorf("%w: %s isn't a folder (clone your repository there first)", ErrInvalid, dir)
	}
	top, err := git(ctx, abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return Published{}, fmt.Errorf("%w: %s isn't a Git repository (clone yours there first): %v", ErrInvalid, dir, err)
	}
	if real, _ := filepath.EvalSymlinks(abs); filepath.Clean(strings.TrimSpace(top)) != real {
		return Published{}, fmt.Errorf("%w: %s isn't the top of its repository (%s is)", ErrInvalid, dir, strings.TrimSpace(top))
	}
	skillsDir := filepath.Join(abs, PublishDir)
	folder := filepath.Join(skillsDir, name)
	if err := noLinks(abs, PublishDir, filepath.Join(PublishDir, name), filepath.Join(PublishDir, readme)); err != nil {
		return Published{}, err
	}
	if err := filepath.WalkDir(folder, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) && p == folder {
			return nil
		}
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s is a symbolic link", ErrInvalid, p)
		}
		return nil
	}); err != nil {
		return Published{}, err
	}

	// The README's new text, before anything is written.
	old, err := os.ReadFile(filepath.Join(skillsDir, readme))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Published{}, err
	}
	table, err := setRow(old, *sk)
	if err != nil {
		return Published{}, err
	}

	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return Published{}, err
	}
	if err := os.RemoveAll(folder); err != nil {
		return Published{}, err
	}
	for p, b := range files {
		dst := filepath.Join(folder, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return Published{}, err
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			return Published{}, err
		}
	}
	if err := os.WriteFile(filepath.Join(skillsDir, readme), table, 0o644); err != nil {
		return Published{}, err
	}

	paths := []string{"--", path.Join(PublishDir, name), path.Join(PublishDir, readme)}
	if _, err := publishGit(ctx, abs, append([]string{"add", "--all"}, paths...)...); err != nil {
		return Published{}, err
	}
	out := Published{Skill: *sk, Push: "git -C " + shellQuote(abs) + " push"}
	if _, err := publishGit(ctx, abs, append([]string{"diff", "--cached", "--quiet"}, paths...)...); err == nil {
		return out, nil // this version is already there
	}
	msg := fmt.Sprintf("Publish the skill %s\n\n%s\n\n%s: %s\n", name, sk.Description, TrailerVersion, sk.Version)
	if _, err := publishGitIn(ctx, abs, []byte(msg), append([]string{"commit", "--quiet", "--no-verify", "-F", "-"}, paths...)...); err != nil {
		return Published{}, err
	}
	head, err := publishGit(ctx, abs, "rev-parse", "HEAD")
	if err != nil {
		return Published{}, err
	}
	out.Commit = strings.TrimSpace(head)
	return out, nil
}

// noLinks refuses any of rels (under dir) that is a symbolic link.
func noLinks(dir string, rels ...string) error {
	for _, r := range rels {
		fi, err := os.Lstat(filepath.Join(dir, r))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s is a symbolic link", ErrInvalid, r)
		}
	}
	return nil
}

// publishGit is git in your repository: no hooks, no signing, no
// prompts, no pager.
func publishGit(ctx context.Context, dir string, args ...string) (string, error) {
	return publishGitIn(ctx, dir, nil, args...)
}

func publishGitIn(ctx context.Context, dir string, stdin []byte, args ...string) (string, error) {
	return gitIn(ctx, dir, stdin, append([]string{"-c", "commit.gpgsign=false", "-c", "core.pager=cat"}, args...)...)
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	if regexp.MustCompile(`^[A-Za-z0-9._/@:+=-]+$`).MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// rowName reads a table row's skill name: its first cell is [name](name/).
var rowName = regexp.MustCompile(`^\| \[([a-z0-9]+(?:-[a-z0-9]+)*)\]\(([a-z0-9]+(?:-[a-z0-9]+)*)/\) \|`)

// cell is s as one Markdown table cell that renders as the text it is:
// on one line, with every character Markdown or HTML reads specially
// escaped, so a description can't end the cell, start a link or an image,
// or inject HTML on GitHub.
func cell(s string) string {
	var b strings.Builder
	for _, r := range strings.Join(strings.Fields(s), " ") {
		switch r {
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '&':
			b.WriteString("&amp;")
		case '|', '\\', '`', '*', '_', '[', ']', '(', ')', '!', '#', '~', '{', '}', '+', '-', '.', ':', '"', '\'', '=':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// row is a skill's line in the README's table.
func row(s Skill) string {
	conn := "none"
	if s.Connectors != "" {
		conn = cell(s.Connectors)
	}
	return fmt.Sprintf("| [%s](%s/) | %s | %s | `%s` |", s.Name, s.Name, cell(s.Description), conn, s.Version)
}

// setRow is the README with s's row set: replaced where it was, or added
// last; every other line kept. A README with no table gets one at its
// end; no README is a new one.
func setRow(old []byte, s Skill) ([]byte, error) {
	header := "| Skill | Description | Connectors it asks for | Version |\n|---|---|---|---|"
	if len(old) == 0 {
		return []byte("# Skills\n\nSkills published with heedle (treadle). Import one with its folder's link.\n\n" + tableStart + "\n" + header + "\n" + row(s) + "\n" + tableEnd + "\n"), nil
	}
	text := string(old)
	i, j := strings.Index(text, tableStart), strings.Index(text, tableEnd)
	if i < 0 && j < 0 {
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		return []byte(text + "\n" + tableStart + "\n" + header + "\n" + row(s) + "\n" + tableEnd + "\n"), nil
	}
	if i < 0 || j < i || strings.Count(text, tableStart) != 1 || strings.Count(text, tableEnd) != 1 {
		return nil, fmt.Errorf("%w: %s/%s's table markers aren't one pair; fix them by hand", ErrInvalid, PublishDir, readme)
	}
	lines := strings.Split(strings.Trim(text[i+len(tableStart):j], "\n"), "\n")
	set := false
	for k, l := range lines {
		if m := rowName.FindStringSubmatch(l); m != nil && m[1] == s.Name && m[2] == s.Name {
			lines[k], set = row(s), true
		}
	}
	if !set {
		lines = append(lines, row(s))
	}
	return []byte(text[:i] + tableStart + "\n" + strings.Join(lines, "\n") + "\n" + text[j:]), nil
}
