package treadle

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestInv_UndoRestoresEvenWhenCancelled: a publish that can't finish,
// most often because it was cancelled, puts the skill's folder and the
// README back as HEAD has them, staged nothing, with a cancelled
// context too. (Review 49 round 2: a cancelled context ran only the
// removals.)
func TestInv_UndoRestoresEvenWhenCancelled(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return string(out)
	}
	run("init", "-q", "-b", "main")
	os.MkdirAll(filepath.Join(dir, "skills", "w"), 0o755)
	os.WriteFile(filepath.Join(dir, "skills", "w", "SKILL.md"), []byte("v1"), 0o644)
	os.WriteFile(filepath.Join(dir, "skills", "README.md"), []byte("table v1"), 0o644)
	run("add", "-A")
	run("-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "-m", "v1")
	// Half a publish: new files written and staged.
	os.WriteFile(filepath.Join(dir, "skills", "w", "SKILL.md"), []byte("v2"), 0o644)
	os.WriteFile(filepath.Join(dir, "skills", "w", "new.txt"), []byte("new"), 0o644)
	os.WriteFile(filepath.Join(dir, "skills", "README.md"), []byte("table v2"), 0o644)
	run("add", "-A")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	undo(ctx, dir, "w")
	if st := run("status", "--porcelain", "--ignored"); st != "" {
		t.Errorf("left %q", st)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "skills", "w", "SKILL.md")); string(b) != "v1" {
		t.Errorf("SKILL.md is %q", b)
	}
}
