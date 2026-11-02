package treadle_test

import (
	"os"
	"testing"
	"time"

	"github.com/heedle-ai/treadle"
)

// TestLive_AnthropicSkills catalogs Anthropic's public skills repository
// and imports every skill it lists without a problem. It needs the
// network, so it runs only with TREADLE_LIVE=1 and isn't an invariant
// guard.
func TestLive_AnthropicSkills(t *testing.T) {
	if os.Getenv("TREADLE_LIVE") != "1" {
		t.Skip("set TREADLE_LIVE=1 to reach github.com")
	}
	cat, err := treadle.Catalog(ctx, "https://github.com/anthropics/skills", "")
	if err != nil {
		t.Fatal(err)
	}
	l := newLibrary(t)
	for _, e := range cat {
		if e.Problem != "" {
			t.Logf("listed, not importable: %s: %s", e.Source.Path, e.Problem)
			continue
		}
		s, err := l.Import(ctx, e.Source, time.Now())
		if err != nil {
			t.Errorf("%s: %v", e.Source.Path, err)
			continue
		}
		t.Logf("imported %-22s %.12s", s.Name, s.Version)
	}
}
