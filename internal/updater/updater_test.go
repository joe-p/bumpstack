package updater

import (
	"testing"
	"time"

	"github.com/joe-p/bumpstack/internal/registry"
)

func tag(name string, pushed *int64) registry.TagMatch {
	var p *time.Time
	if pushed != nil {
		t := time.Unix(*pushed, 0).UTC()
		p = &t
	}
	return registry.TagMatch{Tag: name, Pushed: p}
}

func seconds(v int64) *int64 { return &v }

func TestPrefersMostRecentPush(t *testing.T) {
	best, ok := pickBest(
		[]registry.TagMatch{tag("v3.10", seconds(100)), tag("v3.9", seconds(200))},
		OrderPushed,
	)
	if !ok || best != "v3.9" {
		t.Errorf("best = %q, %v; want v3.9", best, ok)
	}
}

func TestVersionModeIgnoresPushTime(t *testing.T) {
	best, ok := pickBest(
		[]registry.TagMatch{tag("v3.10", seconds(100)), tag("v3.9", seconds(200))},
		OrderVersion,
	)
	if !ok || best != "v3.10" {
		t.Errorf("best = %q, %v; want v3.10", best, ok)
	}
}

func TestFallsBackToNaturalOrder(t *testing.T) {
	best, ok := pickBest(
		[]registry.TagMatch{tag("v3.9", nil), tag("v3.10", nil)},
		OrderPushed,
	)
	if !ok || best != "v3.10" {
		t.Errorf("best = %q, %v; want v3.10", best, ok)
	}
}

func TestDatedTagsWinOverUndated(t *testing.T) {
	best, ok := pickBest(
		[]registry.TagMatch{tag("v3.10", nil), tag("v3.2", seconds(1))},
		OrderPushed,
	)
	if !ok || best != "v3.2" {
		t.Errorf("best = %q, %v; want v3.2", best, ok)
	}
}
