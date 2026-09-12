package tokensaver

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrNilLogger is returned by Init when no logger is provided.
var ErrNilLogger = errors.New("tokensaver: logger is required")

const (
	defaultMinBytes = 500
	defaultMaxBytes = 10 * 1024 * 1024 // 10 MiB

	// detectPeekBytes is how much of each blob is inspected to pick a filter.
	detectPeekBytes = 1024

	// gitDiffMaxHunkLines caps changed/context lines kept per hunk.
	gitDiffMaxHunkLines = 100
	// dedupLogMaxLines caps log output after dedup.
	dedupLogMaxLines = 2000
	// smartTruncateThreshold is the line count above which smart-truncate kicks in.
	smartTruncateThreshold = 250
	smartTruncateHead      = 120
	smartTruncateTail      = 60
)

// Filter names (config rtk_filters.enabled).
const (
	FilterGitDiff       = "git-diff"
	FilterDedupLog      = "dedup-log"
	FilterSmartTruncate = "smart-truncate"
)

// filter is a pure, total text transformation: it never panics and never
// returns an empty string for non-empty input (callers guard anyway).
type filter func(text string) string

// builtinFilters is the ported filter set; order defines detect precedence.
var builtinFilters = []struct {
	name   string
	filter filter
}{
	{FilterGitDiff, filterGitDiff},
	{FilterDedupLog, filterDedupLog},
	{FilterSmartTruncate, filterSmartTruncate},
}

// filterSet bounds and applies the enabled filters.
type filterSet struct {
	minBytes int64
	maxBytes int64
	enabled  map[string]filter
}

// newFilterSet builds a filterSet; nil settings select defaults (all filters).
func newFilterSet(settings *RTKFilterSettings) filterSet {
	fs := filterSet{
		minBytes: defaultMinBytes,
		maxBytes: defaultMaxBytes,
		enabled:  map[string]filter{},
	}
	if settings != nil {
		if settings.MinBytes > 0 {
			fs.minBytes = settings.MinBytes
		}
		if settings.MaxBytes > 0 {
			fs.maxBytes = settings.MaxBytes
		}
	}
	for _, f := range builtinFilters {
		if settings == nil || len(settings.Enabled) == 0 {
			fs.enabled[f.name] = f.filter
			continue
		}
		for _, want := range settings.Enabled {
			if want == f.name {
				fs.enabled[f.name] = f.filter
				break
			}
		}
	}
	return fs
}

// detect picks a filter for a blob by peeking at its head. Specialized
// filters win over the generic fallback; no match means smart-truncate when
// enabled, otherwise no filter at all.
func (fs filterSet) detect(text string) (string, bool) {
	peek := text
	if len(peek) > detectPeekBytes {
		peek = peek[:detectPeekBytes]
	}
	if _, ok := fs.enabled[FilterGitDiff]; ok && strings.Contains(peek, "diff --git a/") {
		return FilterGitDiff, true
	}
	if _, ok := fs.enabled[FilterDedupLog]; ok && looksLikeRepetitiveLog(peek) {
		return FilterDedupLog, true
	}
	if _, ok := fs.enabled[FilterSmartTruncate]; ok {
		return FilterSmartTruncate, true
	}
	return "", false
}

// apply runs the named filter guarded against panics and empty output.
// ok is false when the result must be discarded (keep the original).
func (fs filterSet) apply(name, text string) (string, bool) {
	fn, ok := fs.enabled[name]
	if !ok || text == "" {
		return text, false
	}
	out := text
	func() {
		defer func() {
			if r := recover(); r != nil {
				out = text
			}
		}()
		out = safeApply(fn, text)
	}()
	if out == "" || len(out) >= len(text) {
		return text, false
	}
	return out, true
}

// safeApply runs fn, falling back to the input on panic.
func safeApply(fn filter, text string) (out string) {
	out = fn(text)
	return out
}

// looksLikeRepetitiveLog reports whether the head of a blob looks like log
// output with many duplicate lines (≥40% repeats in the first 60 lines).
func looksLikeRepetitiveLog(peek string) bool {
	lines := strings.Split(peek, "\n")
	if len(lines) < 10 {
		return false
	}
	if len(lines) > 60 {
		lines = lines[:60]
	}
	seen := make(map[string]struct{}, len(lines))
	dupes := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			dupes++
			continue
		}
		seen[trimmed] = struct{}{}
	}
	total := len(seen) + dupes
	return total > 0 && dupes*100/total >= 40
}

// filterGitDiff keeps file headers and hunks, capping each hunk's line count.
func filterGitDiff(text string) string {
	lines := strings.Split(text, "\n")
	var out []string
	hunkLines := 0
	inHunk := false
	truncatedHunks := 0
	for _, line := range lines {
		isHunkHeader := strings.HasPrefix(line, "@@")
		isFileHeader := strings.HasPrefix(line, "diff --git ") ||
			strings.HasPrefix(line, "index ") ||
			strings.HasPrefix(line, "--- ") ||
			strings.HasPrefix(line, "+++ ")
		switch {
		case isFileHeader:
			inHunk = false
			hunkLines = 0
			out = append(out, line)
		case isHunkHeader:
			inHunk = true
			hunkLines = 0
			out = append(out, line)
		case inHunk:
			hunkLines++
			if hunkLines > gitDiffMaxHunkLines {
				// Mark the hunk once and skip the rest of it.
				if hunkLines == gitDiffMaxHunkLines+1 {
					out = append(out, fmt.Sprintf("... [token-saver: hunk truncated, %d+ lines omitted]", gitDiffMaxHunkLines))
					truncatedHunks++
				}
				continue
			}
			out = append(out, line)
		default:
			out = append(out, line)
		}
	}
	if truncatedHunks == 0 && len(strings.Join(out, "\n")) >= len(text) {
		return text
	}
	return strings.Join(out, "\n")
}

// filterDedupLog collapses consecutive duplicate lines and caps the result.
func filterDedupLog(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	prev := ""
	collapsed := 0
	for _, line := range lines {
		if line == prev && line != "" {
			collapsed++
			continue
		}
		prev = line
		out = append(out, line)
	}
	if collapsed == 0 {
		return text
	}
	if len(out) > dedupLogMaxLines {
		out = append(out[:dedupLogMaxLines],
			fmt.Sprintf("... [token-saver: log truncated at %d lines]", dedupLogMaxLines))
	}
	return strings.Join(out, "\n")
}

// filterSmartTruncate keeps head+tail of oversized blobs.
func filterSmartTruncate(text string) string {
	lines := strings.Split(text, "\n")
	if len(lines) <= smartTruncateThreshold {
		return text
	}
	omitted := len(lines) - smartTruncateHead - smartTruncateTail
	head := lines[:smartTruncateHead]
	tail := lines[len(lines)-smartTruncateTail:]
	marker := fmt.Sprintf("... [token-saver: %d middle lines truncated]", omitted)
	return strings.Join(append(append(head, marker), tail...), "\n")
}

// formatBytes renders a byte count for stats logging.
func formatBytes(n int64) string {
	switch {
	case n >= 1024*1024:
		return fmt.Sprintf("%.1fMiB", float64(n)/(1024*1024))
	case n >= 1024:
		return fmt.Sprintf("%.1fKiB", float64(n)/1024)
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// percent computes a safe percentage.
func percent(part, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(part) * 100 / float64(total)
}

// sortedFilterNames returns enabled filter names in a stable order (config
// surface introspection and tests).
func (fs filterSet) sortedFilterNames() []string {
	names := make([]string, 0, len(fs.enabled))
	for name := range fs.enabled {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
