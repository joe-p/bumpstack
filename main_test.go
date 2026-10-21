package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joe-p/bumpstack/internal/updater"
)

func TestCheckExitCodes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/bad/") {
			http.Error(w, "registry failure", http.StatusInternalServerError)
			return
		}
		if strings.Contains(r.URL.Path, "/tags/list") {
			w.Write([]byte(`{"tags":["2"]}`))
			return
		}
		w.Header().Set("Docker-Content-Digest", "sha256:"+strings.Repeat("a", 64))
		w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
		w.Write([]byte(`{"manifests":[{"platform":{"os":"linux","architecture":"amd64"}},{"platform":{"os":"linux","architecture":"arm64"}}]}`))
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	for _, tc := range []struct {
		name   string
		images []string
		want   int
	}{
		{"failure", []string{"bad:1"}, 2},
		{"failure and update", []string{"bad:1", "good:1"}, 2},
		{"update", []string{"good:1"}, 1},
		{"current", []string{"good:2@sha256:" + strings.Repeat("a", 64)}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := "services:\n"
			for i, image := range tc.images {
				text += "  service" + strings.Repeat("x", i) + ":\n    # version: ^2$\n    image: " + host + "/" + image + "\n"
			}
			path := filepath.Join(t.TempDir(), "compose.yml")
			if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
			code, err := execute(&options{file: path, check: true, insecure: true, order: updater.OrderVersion})
			if err != nil || code != tc.want {
				t.Fatalf("execute = %d, %v; want %d, nil", code, err, tc.want)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != text {
				t.Fatalf("--check modified the file: %v", err)
			}
		})
	}
}

func TestWriteIsOptIn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/tags/list") {
			w.Write([]byte(`{"tags":["2"]}`))
			return
		}
		w.Header().Set("Docker-Content-Digest", "sha256:"+strings.Repeat("a", 64))
		w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
		w.Write([]byte(`{"manifests":[{"platform":{"os":"linux","architecture":"amd64"}},{"platform":{"os":"linux","architecture":"arm64"}}]}`))
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	t.Setenv("DOCKER_CONFIG", t.TempDir())

	text := "services:\n  app:\n    # version: ^2$\n    image: " + host + "/good:1\n"
	written := "services:\n  app:\n    # version: ^2$\n    image: " + host + "/good:2@sha256:" + strings.Repeat("a", 64) + "\n"

	for _, tc := range []struct {
		name  string
		write bool
		want  string
	}{
		{"default leaves the file alone", false, text},
		{"--write applies the change", true, written},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "compose.yml")
			if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
			code, err := execute(&options{file: path, write: tc.write, insecure: true, order: updater.OrderVersion})
			if err != nil || code != 0 {
				t.Fatalf("execute = %d, %v; want 0, nil", code, err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != tc.want {
				t.Fatalf("file = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestParseWriteFlag(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		write bool
	}{
		{[]string{"compose.yml"}, false},
		{[]string{"-w"}, true},
		{[]string{"--write", "compose.yml"}, true},
	} {
		opts, err := parseArgs(tc.args)
		if err != nil || opts == nil {
			t.Fatalf("parseArgs(%q) = %v, %v; want options, nil", tc.args, opts, err)
		}
		if opts.write != tc.write {
			t.Errorf("parseArgs(%q).write = %v; want %v", tc.args, opts.write, tc.write)
		}
	}
}
