package compose

import (
	"strings"
	"testing"
)

const sample = "services:\n" +
	"  gluetun:\n" +
	"    # version: ^v3.\\d+$\n" +
	"    image: qmcgaw/gluetun:v3.41@sha256:abc\n" +
	"  redis:\n" +
	"    image: redis:7.2\n" +
	"    # version: ^7\\.\\d+$\n" +
	"    restart: unless-stopped\n"

func TestFindsImageWithPattern(t *testing.T) {
	file, err := Parse(sample)
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Images) != 1 {
		t.Fatalf("images = %d, want 1", len(file.Images))
	}
	img := file.Images[0]
	if img.Line != 3 {
		t.Errorf("line = %d, want 3", img.Line)
	}
	if img.Service != "gluetun" {
		t.Errorf("service = %q, want gluetun", img.Service)
	}
	if img.Reference.Repository != "qmcgaw/gluetun" {
		t.Errorf("repository = %q", img.Reference.Repository)
	}
	if img.VersionPattern != `^v3.\d+$` {
		t.Errorf("pattern = %q", img.VersionPattern)
	}
	if !img.Pattern.MatchString("v3.41") {
		t.Error("v3.41 should match")
	}
	if img.Pattern.MatchString("v4.0") {
		t.Error("v4.0 should not match")
	}
	// redis has no adjacent pattern
	if len(file.Skipped) != 1 {
		t.Fatalf("skipped = %d, want 1", len(file.Skipped))
	}
	if file.Skipped[0].Service != "redis" {
		t.Errorf("skipped service = %q, want redis", file.Skipped[0].Service)
	}
}

func TestRewritePreservesFormatting(t *testing.T) {
	file, err := Parse(sample)
	if err != nil {
		t.Fatal(err)
	}
	file.Rewrite(3, "qmcgaw/gluetun:v3.42@sha256:def")
	out := file.Render()
	if !strings.Contains(out, "    image: qmcgaw/gluetun:v3.42@sha256:def\n") {
		t.Errorf("rewritten line missing:\n%s", out)
	}
	if !strings.Contains(out, `# version: ^v3.\d+$`+"\n") {
		t.Errorf("pattern comment lost:\n%s", out)
	}
	want := strings.Replace(sample, "v3.41@sha256:abc", "v3.42@sha256:def", 1)
	if out != want {
		t.Errorf("render mismatch:\n got %q\nwant %q", out, want)
	}
}

func TestPreservesQuotesAndTrailingComment(t *testing.T) {
	src := "services:\n  a:\n    # version: ^1$\n    image: \"repo/app:1\" # pinned\n"
	file, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if file.Images[0].Original != "repo/app:1" {
		t.Errorf("original = %q", file.Images[0].Original)
	}
	rendered := file.Images[0].RenderLine("repo/app:2@sha256:aa")
	want := `    image: "repo/app:2@sha256:aa" # pinned`
	if rendered != want {
		t.Errorf("rendered = %q, want %q", rendered, want)
	}
}

func TestRoundTripsUnchangedContent(t *testing.T) {
	file, err := Parse(sample)
	if err != nil {
		t.Fatal(err)
	}
	if file.Render() != sample {
		t.Errorf("round trip mismatch:\n got %q\nwant %q", file.Render(), sample)
	}
}

func TestIgnoresBlockScalarContents(t *testing.T) {
	for _, header := range []string{"command: |", "command: >-", "command: |2+ # script", "command: &script |", "\"command\": |", "'command': >", "command:\n      - |", "command:\n      - >-", "configs:\n    - content: |2"} {
		t.Run(header, func(t *testing.T) {
			src := "services:\n  api:\n    " + header + "\n" +
				"        # version: ^1$\n        image: script-text:1\n\n" +
				"        # version: ^2$\n        image: more-script-text:2\n" +
				"    # version: ^3$\n    image: real/app:3\n" +
				"  worker:\n    # version: ^4$\n    image: real/worker:4\n"
			file, err := Parse(src)
			if err != nil {
				t.Fatal(err)
			}
			if len(file.Images) != 2 || len(file.Skipped) != 0 {
				t.Fatalf("managed images = %d, skipped = %d; want 2, 0", len(file.Images), len(file.Skipped))
			}
			if file.Images[0].Service != "api" || file.Images[1].Service != "worker" {
				t.Fatal("service tracking lost after block scalar")
			}
			file.Rewrite(file.Images[0].Line, "real/app:5@sha256:abc")
			want := strings.Replace(src, "image: real/app:3", "image: real/app:5@sha256:abc", 1)
			if file.Render() != want {
				t.Errorf("block content or formatting changed:\n%s", file.Render())
			}
		})
	}
}
