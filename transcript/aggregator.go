package transcript

import (
	"context"
	"sort"
)

// Aggregator fans a project across every wired SessionSource, merges the
// per-source session lists, applies the deepwork soft-delete filter, and sorts
// by recency. Adding a runtime = registering one more Source; this layer is
// unchanged (SSOT-SESSION-LOADER.md §6).
type Aggregator struct {
	sources []SessionSource
	hidden  HiddenStore
}

// NewAggregator wires the sources + the soft-delete store (hidden may be nil to
// disable filtering, e.g. in tests).
func NewAggregator(hidden HiddenStore, sources ...SessionSource) *Aggregator {
	return &Aggregator{sources: sources, hidden: hidden}
}

// ListResult carries the merged sessions plus the set of sources that errored
// (degraded), mirroring the fleet handler's honesty contract.
type ListResult struct {
	Sessions []SessionMeta
	Degraded []string // source kinds that failed
	Wired    int      // number of wired sources
	Failed   int      // number that errored
}

// List aggregates sessions for projectDir. includeHidden controls whether
// soft-deleted rows are returned (default list hides them). Hidden state is
// always stamped onto every row so the UI can show the badge.
func (a *Aggregator) List(ctx context.Context, projectDir string, includeHidden bool) (*ListResult, error) {
	res := &ListResult{Sessions: make([]SessionMeta, 0, 32)}

	for _, src := range a.sources {
		res.Wired++
		metas, err := src.ListSessions(ctx, projectDir)
		if err != nil {
			res.Failed++
			res.Degraded = append(res.Degraded, src.Kind())
			continue
		}

		// Stamp hidden + soft-rename overlays from the deepwork-owned table
		// (one batch read per source). friendly_name overrides the SSOT title;
		// the runtime transcript itself is never modified (SSOT protection).
		var overlays map[string]SessionOverlay
		if a.hidden != nil {
			if ov, herr := a.hidden.Overlays(ctx, src.Kind()); herr == nil {
				overlays = ov
			}
		}
		for i := range metas {
			if overlays != nil {
				if ov, ok := overlays[metas[i].ID]; ok {
					if ov.Hidden {
						metas[i].Hidden = true
					}
					if ov.FriendlyName != "" {
						metas[i].FriendlyName = ov.FriendlyName
						metas[i].Title = ov.FriendlyName
					}
				}
			}
			if metas[i].Hidden && !includeHidden {
				continue
			}
			res.Sessions = append(res.Sessions, metas[i])
		}
	}

	// Newest activity first.
	sort.SliceStable(res.Sessions, func(i, j int) bool {
		return res.Sessions[i].UpdatedAt.After(res.Sessions[j].UpdatedAt)
	})
	return res, nil
}

// DirCounter is an optional, cheap fast-path a SessionSource may implement to
// count its sessions grouped by originating project dir (cwd) in a SINGLE sweep
// of its storage — without the per-file full parse ListSessions does and without
// being re-invoked once per workspace.
//
// CHG-014 P5 perf fix: GET /api/workspaces enriches every workspace with a
// session_count. The naive path called ListSessions(projectDir) once per
// workspace, and CodexSource.ListSessions full-parses every rollout-*.jsonl
// (162 MB / 475 files here) on each call → O(workspaces × allCodexFiles) ≈ 8 s.
// CountByDir scans each source's storage ONCE, returns cwd→count, so the handler
// resolves N workspaces with N O(1) map lookups → O(allFiles) total. For codex
// the sweep reads only the first line (session_meta carries cwd) per file, not
// the whole transcript, cutting the bytes read by ~100×.
type DirCounter interface {
	// CountSessionsByDir returns a map of project dir (cwd) → session count for
	// every session this source stored, in one sweep. Hidden/soft-delete is NOT
	// applied here (the panel hint counts SSOT sessions; the aggregator applies
	// the overlay filter only on the detailed List path).
	CountSessionsByDir(ctx context.Context) (map[string]int, error)
}

// CountByDir returns, per project dir, the merged session count across every
// wired source — computed with a single sweep per source. Sources implementing
// DirCounter use their cheap fast-path; the rest fall back to ListSessions("")
// (enumerate-all) grouped by SsotPath dir is not derivable generically, so a
// non-DirCounter source is simply skipped from the by-dir map and must be
// counted via the per-workspace path. In practice claude/codex implement the
// fast-path and deepwork is workspace-scoped (counted separately by the caller).
//
// The returned map keys are exact project dirs (cwd / root_dir); a workspace
// resolves its count with one lookup. Missing key → 0 (honest empty), never an
// error: a degraded source must not break the whole list.
func (a *Aggregator) CountByDir(ctx context.Context) map[string]int {
	counts := map[string]int{}
	for _, src := range a.sources {
		dc, ok := src.(DirCounter)
		if !ok {
			continue
		}
		byDir, err := dc.CountSessionsByDir(ctx)
		if err != nil {
			continue // honest degradation: skip this source's contribution
		}
		for dir, n := range byDir {
			counts[dir] += n
		}
	}
	return counts
}

// SourceByKind returns the wired source for a kind, or nil.
func (a *Aggregator) SourceByKind(kind string) SessionSource {
	for _, src := range a.sources {
		if src.Kind() == kind {
			return src
		}
	}
	return nil
}

// LoadTranscript dispatches a transcript load to the named source.
func (a *Aggregator) LoadTranscript(ctx context.Context, source string, ref SessionRef) (*Transcript, error) {
	src := a.SourceByKind(source)
	if src == nil {
		return nil, ErrUnknownSource
	}
	return src.LoadTranscript(ctx, ref)
}

// LoadTranscriptWindow dispatches the bounded-tail contract to capable sources.
// Small/native sources retain a correctness-first full-load fallback.
func (a *Aggregator) LoadTranscriptWindow(ctx context.Context, source string, ref SessionRef, req WindowRequest) (*WindowResult, error) {
	src := a.SourceByKind(source)
	if src == nil {
		return nil, ErrUnknownSource
	}
	if windowed, ok := src.(WindowSource); ok {
		return windowed.LoadTranscriptWindow(ctx, ref, req)
	}
	tr, err := src.LoadTranscript(ctx, ref)
	if err != nil {
		return nil, err
	}
	runs := ProjectAgentRuns(tr)
	limit := normalizeWindowLimit(req.Limit)
	start := len(runs) - limit
	if start < 0 {
		start = 0
	}
	tr.Runs = runs[start:]
	return &WindowResult{Transcript: tr, Before: int64(start), HasMore: start > 0}, nil
}
