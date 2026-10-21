package ordering

import "testing"

func TestNumericRunsCompareNumerically(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v3.9", "v3.10", -1},
		{"v3.10", "v3.9", 1},
		{"1.2.3", "1.10.0", -1},
		{"v1.01", "v1.1", 0},
	}
	for _, c := range cases {
		if got := NaturalCmp(c.a, c.b); got != c.want {
			t.Errorf("NaturalCmp(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestPrefixAndSuffixOrdering(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"7.2", "7.2-alpine", -1},
		{"latest", "v1", -1},
		{"same", "same", 0},
	}
	for _, c := range cases {
		if got := NaturalCmp(c.a, c.b); got != c.want {
			t.Errorf("NaturalCmp(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestComparesBuildCountersNumerically(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"12.2ubu2604-ls55", "12.2ubu2604-ls100", -1},
		{"12.2ubu2604-ls9", "12.2ubu2604-ls55", -1},
		{"12.2ubu2604-ls55", "12.2ubu2604-ls54", 1},
	}
	for _, c := range cases {
		if got := NaturalCmp(c.a, c.b); got != c.want {
			t.Errorf("NaturalCmp(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestNewerUbuntuBaseWinsWithinAVersion(t *testing.T) {
	if got := NaturalCmp("12.2ubu2404-ls99", "12.2ubu2604-ls55"); got != -1 {
		t.Errorf("got %d, want -1", got)
	}
}

func TestMinorVersionBeatsBuildCounter(t *testing.T) {
	if got := NaturalCmp("12.9ubu2604-ls300", "12.10ubu2604-ls1"); got != -1 {
		t.Errorf("got %d, want -1", got)
	}
}

func TestOldLineRebuildCannotOutrankNewerVersion(t *testing.T) {
	tags := []string{
		"12.2ubu2604-ls55",
		"12.2ubu2604-ls54",
		"12.1ubu2604-ls51",
		"12.0ubu2604-ls48",
		"10.10.7-ls300",
	}
	best := tags[0]
	for _, tag := range tags[1:] {
		if NaturalCmp(tag, best) > 0 {
			best = tag
		}
	}
	if best != "12.2ubu2604-ls55" {
		t.Errorf("best = %q", best)
	}
}
