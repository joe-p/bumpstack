// Command bumpstack bumps container images in a docker-compose file to the
// newest matching version and pins them to their multi-arch manifest digest.
//
// It only reports the changes it would make unless --write is given.
//
// An image is managed when the line directly above it is a comment holding a
// version pattern, for example:
//
//	services:
//	  gluetun:
//	    # version: ^v3.\d+$
//	    image: qmcgaw/gluetun:v3.41@sha256:...
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/joe-p/bumpstack/internal/compose"
	"github.com/joe-p/bumpstack/internal/dockerconfig"
	"github.com/joe-p/bumpstack/internal/registry"
	"github.com/joe-p/bumpstack/internal/updater"
)

const version = "0.1.0"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	opts, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}
	if opts == nil {
		return 0 // --help or --version was already handled
	}

	code, err := execute(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", updater.ErrString(err))
		return 2
	}
	return code
}

type options struct {
	file             string
	write            bool
	check            bool
	services         stringList
	insecure         bool
	concurrency      int
	order            updater.OrderMode
	requireMultiarch bool
	verbose          bool
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func parseArgs(args []string) (*options, error) {
	opts := &options{}
	fs := flag.NewFlagSet("bumpstack", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usageText) }

	fs.BoolVar(&opts.write, "w", false, "")
	fs.BoolVar(&opts.write, "write", false, "")
	fs.BoolVar(&opts.check, "check", false, "")
	fs.Var(&opts.services, "s", "")
	fs.Var(&opts.services, "service", "")
	fs.BoolVar(&opts.insecure, "insecure", false, "")
	fs.IntVar(&opts.concurrency, "j", 4, "")
	fs.IntVar(&opts.concurrency, "concurrency", 4, "")
	orderRaw := fs.String("order", "pushed", "")
	fs.BoolVar(&opts.requireMultiarch, "require-multiarch", false, "")
	fs.BoolVar(&opts.verbose, "v", false, "")
	fs.BoolVar(&opts.verbose, "verbose", false, "")
	showVersion := fs.Bool("version", false, "")

	// Go's flag package stops at the first positional argument, so move the
	// flags in front of it to accept `bumpstack file.yml -n` like clap does.
	if err := fs.Parse(permuteArgs(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, nil
		}
		return nil, err
	}
	if *showVersion {
		fmt.Printf("bumpstack %s\n", version)
		return nil, nil
	}

	opts.file = "docker-compose.yml"
	if fs.NArg() > 1 {
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(1))
	}
	if fs.NArg() == 1 {
		opts.file = fs.Arg(0)
	}

	order, err := updater.ParseOrderMode(*orderRaw)
	if err != nil {
		return nil, err
	}
	opts.order = order

	return opts, nil
}

// valueFlags names the flags that consume a following argument, used when
// reordering args around positionals.
var valueFlags = map[string]bool{
	"s": true, "service": true, "j": true, "concurrency": true, "order": true,
}

func permuteArgs(args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(arg) > 1 && arg[0] == '-' {
			flags = append(flags, arg)
			name := strings.TrimLeft(arg, "-")
			if strings.ContainsRune(name, '=') {
				continue // value is attached to the flag
			}
			if valueFlags[name] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positional = append(positional, arg)
	}
	return append(flags, positional...)
}

func execute(opts *options) (int, error) {
	text, err := os.ReadFile(opts.file)
	if err != nil {
		return 2, fmt.Errorf("reading %s: %w", opts.file, err)
	}
	file, err := compose.Parse(string(text))
	if err != nil {
		return 2, fmt.Errorf("parsing %s: %w", opts.file, err)
	}

	if opts.verbose {
		fmt.Printf("%s: %d image(s) with a version pattern, %d without\n",
			opts.file, len(file.Images), len(file.Skipped))
		for _, plain := range file.Skipped {
			fmt.Fprintf(os.Stderr,
				"note: %s: %s (line %d) skipped, no `# version:` pattern\n",
				serviceOrDash(plain.Service), plain.Value, plain.Line+1)
		}
	}

	registry.Version = version
	client := registry.New(dockerconfig.Load(opts.verbose), opts.insecure)
	planOpts := updater.Options{
		Services:         opts.services,
		Concurrency:      opts.concurrency,
		Order:            opts.order,
		RequireMultiarch: opts.requireMultiarch,
	}

	outcomes := updater.Plan(client, file, planOpts)

	updated := 0
	for _, outcome := range outcomes {
		if u, ok := outcome.Status.(updater.Updated); ok {
			updated++
			file.Rewrite(outcome.Line, u.Value)
		}
	}

	report(outcomes, opts.verbose)

	failures := 0
	for _, outcome := range outcomes {
		if _, ok := outcome.Status.(updater.Failed); ok {
			failures++
		}
	}

	if opts.check {
		if failures > 0 {
			return 2, nil
		}
		if updated > 0 {
			fmt.Printf("%d image(s) need updating\n", updated)
			return 1, nil
		}
		fmt.Println("all images are up to date")
		return 0, nil
	}

	if opts.write {
		if updated > 0 {
			if err := os.WriteFile(opts.file, []byte(file.Render()), 0o644); err != nil {
				return 2, fmt.Errorf("writing %s: %w", opts.file, err)
			}
			fmt.Printf("updated %s (%d image(s))\n", opts.file, updated)
		} else {
			fmt.Println("nothing to update")
		}
	} else if updated > 0 {
		fmt.Printf("dry run: %d image(s) would be updated\n", updated)
	} else {
		fmt.Println("dry run: nothing to update")
	}

	if failures > 0 {
		return 2, nil
	}
	return 0, nil
}

func report(outcomes []updater.Outcome, verbose bool) {
	width := 4
	for _, outcome := range outcomes {
		if n := utf8.RuneCountInString(serviceOrDash(outcome.Service)); n > width {
			width = n
		}
	}

	for _, outcome := range outcomes {
		service := serviceOrDash(outcome.Service)
		switch status := outcome.Status.(type) {
		case updater.Updated:
			fmt.Printf("  %-*s  %s -> %s\n", width, service, outcome.Current, status.Value)
		case updater.Current:
			if verbose {
				fmt.Printf("  %-*s  %s (up to date)\n", width, service, outcome.Current)
			}
		case updater.Skipped:
			fmt.Printf("  %-*s  %s (skipped: %s)\n", width, service, outcome.Current, status.Reason)
		case updater.Failed:
			fmt.Printf("  %-*s  %s (error: %s)\n", width, service, outcome.Current, status.Message)
		}
		if outcome.Warning != "" {
			fmt.Fprintf(os.Stderr, "warning: %s: %s\n", service, outcome.Warning)
		}
	}
}

func serviceOrDash(service string) string {
	if service == "" {
		return "-"
	}
	return service
}

const usageText = `Bump and digest-pin container images in a docker-compose file.

An image is bumped when the line above it is a comment holding a version
pattern, for example:

    services:
      gluetun:
        # version: ^v3.\d+$
        image: qmcgaw/gluetun:v3.41@sha256:...

Usage:
  bumpstack [OPTIONS] [FILE]      FILE defaults to docker-compose.yml

Options:
  -w, --write              Apply the changes to FILE (default is a dry run).
      --check              Write nothing; exit 1 if any image needs updating.
  -s, --service NAME       Only process this service; repeatable.
      --order pushed|version
                           Pick the newest pushed tag (default) or the newest
                           version.
      --require-multiarch  Fail instead of pinning a single-architecture tag.
  -j, --concurrency N      Images to query in parallel (default 4).
      --insecure           Use plain HTTP - for local test registries.
  -v, --verbose            Extra diagnostics, including skipped images.
  -h, --help               Print this help.
      --version            Print the version.

Exit codes: 0 success, 1 updates available under --check, 2 failure.
`
