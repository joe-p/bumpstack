// Package updater decides and applies the update for each image.
package updater

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/joe-p/bumpstack/internal/compose"
	"github.com/joe-p/bumpstack/internal/ordering"
	"github.com/joe-p/bumpstack/internal/registry"
)

// OrderMode says how to choose among tags matching an image's version pattern.
type OrderMode int

const (
	// OrderPushed picks the newest pushed tag (falling back to newest version).
	OrderPushed OrderMode = iota
	// OrderVersion picks the newest version, regardless of when it was pushed.
	OrderVersion
)

func (o OrderMode) String() string {
	if o == OrderVersion {
		return "version"
	}
	return "pushed"
}

// ParseOrderMode converts a CLI value into an OrderMode.
func ParseOrderMode(value string) (OrderMode, error) {
	switch value {
	case "pushed":
		return OrderPushed, nil
	case "version":
		return OrderVersion, nil
	}
	return OrderPushed, fmt.Errorf("invalid value %q for --order (want `pushed` or `version`)", value)
}

// Options are the knobs controlling how images are processed.
type Options struct {
	Services         []string
	Concurrency      int
	Order            OrderMode
	RequireMultiarch bool
}

// Status is the result of examining one image.
type Status interface{ isStatus() }

// Updated means a newer tag and/or digest is available.
type Updated struct{ Value string }

// Current means the image is already at the newest matching tag with the right
// digest.
type Current struct{}

// Skipped means the image was deliberately not touched.
type Skipped struct{ Reason string }

// Failed means something went wrong; the line is left alone.
type Failed struct{ Message string }

func (Updated) isStatus() {}
func (Current) isStatus() {}
func (Skipped) isStatus() {}
func (Failed) isStatus()  {}

// Outcome is one image and what should happen to it.
type Outcome struct {
	Line    int
	Service string
	Current string
	Status  Status
	// Warning is a non-fatal problem worth surfacing (does not block the update).
	Warning string
}

// Changed reports whether the image would be rewritten.
func (o Outcome) Changed() bool {
	_, ok := o.Status.(Updated)
	return ok
}

// Plan examines every eligible image and returns what should change.
func Plan(client *registry.Client, file *compose.File, opts Options) []Outcome {
	var selected []*compose.Image
	for _, image := range file.Images {
		if len(opts.Services) == 0 || slices.Contains(opts.Services, image.Service) {
			selected = append(selected, image)
		}
	}

	results := runPool(client, selected, opts)

	outcomes := make([]Outcome, 0, len(selected))
	for i, image := range selected {
		outcomes = append(outcomes, Outcome{
			Line:    image.Line,
			Service: image.Service,
			Current: image.Original,
			Status:  results[i].status,
			Warning: results[i].warning,
		})
	}
	return outcomes
}

type result struct {
	status  Status
	warning string
}

// runPool runs compute over the images using a small pool of worker goroutines.
func runPool(client *registry.Client, images []*compose.Image, opts Options) []result {
	results := make([]result, len(images))
	if len(images) == 0 {
		return results
	}

	workers := opts.Concurrency
	if workers < 1 {
		workers = 1
	}
	if workers > len(images) {
		workers = len(images)
	}

	var next atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				index := int(next.Add(1)) - 1
				if index >= len(images) {
					return
				}
				results[index] = compute(client, images[index], opts)
			}
		}()
	}
	wg.Wait()
	return results
}

func compute(client *registry.Client, image *compose.Image, opts Options) result {
	status, warning, err := computeInner(client, image, opts)
	if err != nil {
		return result{status: Failed{Message: ErrString(err)}}
	}
	return result{status: status, warning: warning}
}

func computeInner(client *registry.Client, image *compose.Image, opts Options) (Status, string, error) {
	needAll := opts.Order == OrderVersion
	matches, err := client.FindMatchingTags(image.Reference, image.Pattern, needAll)
	if err != nil {
		return nil, "", err
	}
	if len(matches) == 0 {
		return Skipped{Reason: fmt.Sprintf(
			"no tag matches `%s` (current tag %s)", image.VersionPattern, image.Reference.TagOrLatest(),
		)}, "", nil
	}

	tag, ok := pickBest(matches, opts.Order)
	if !ok {
		return Skipped{Reason: "no usable tag"}, "", nil
	}

	resolved, err := client.ResolveDigest(image.Reference, tag)
	if err != nil {
		return nil, "", err
	}

	var warning string
	if !resolved.IsMultiarch() {
		var coverage string
		switch {
		case !resolved.IsIndex:
			mediaType := resolved.MediaType
			if mediaType == "" {
				mediaType = "unknown type"
			}
			coverage = fmt.Sprintf("a single manifest (%s)", mediaType)
		case len(resolved.Platforms) == 0:
			coverage = "an index with no platform metadata"
		default:
			coverage = fmt.Sprintf("an index covering only %s", strings.Join(resolved.Platforms, ", "))
		}
		warning = fmt.Sprintf(
			"%s:%s resolves to %s, so the pinned digest will not run on other architectures",
			image.Reference.Repository, tag, coverage,
		)
		if opts.RequireMultiarch {
			return Failed{Message: warning}, "", nil
		}
	}

	value := image.Reference.Render(tag, resolved.Digest)
	if value == image.Original {
		return Current{}, warning, nil
	}
	return Updated{Value: value}, warning, nil
}

// pickBest chooses the tag to pin according to the requested ordering.
func pickBest(matches []registry.TagMatch, order OrderMode) (string, bool) {
	if len(matches) == 0 {
		return "", false
	}
	if order == OrderVersion {
		return slices.MaxFunc(matches, func(a, b registry.TagMatch) int {
			return ordering.NaturalCmp(a.Tag, b.Tag)
		}).Tag, true
	}

	dated := slices.DeleteFunc(slices.Clone(matches), func(m registry.TagMatch) bool {
		return m.Pushed == nil
	})
	if len(dated) > 0 {
		return slices.MaxFunc(dated, func(a, b registry.TagMatch) int {
			return a.Pushed.Compare(*b.Pushed)
		}).Tag, true
	}
	return slices.MaxFunc(matches, func(a, b registry.TagMatch) int {
		return ordering.NaturalCmp(a.Tag, b.Tag)
	}).Tag, true
}

// ErrString flattens an error chain the way anyhow's `{:#}` does.
//
// Parts that are already spelled out inside the outermost message are dropped:
// net/http wraps transport failures several layers deep with near-identical
// text, and repeating it adds nothing.
func ErrString(err error) string {
	var parts []string
	for err != nil {
		parts = append(parts, err.Error())
		err = errors.Unwrap(err)
	}
	if len(parts) == 0 {
		return ""
	}
	kept := parts[:1]
	for _, part := range parts[1:] {
		if strings.Contains(parts[0], part) {
			continue
		}
		kept = append(kept, part)
	}
	return strings.Join(kept, ": ")
}
