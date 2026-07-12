// Package usage — presence.go: axis 1 of the quota model, "does this account exist on
// this host?".
//
// This is a USER fact and it is deliberately kept independent of whether the CLI can be
// executed. Probing the binary (axis 4) answers a different question, and letting it
// answer this one is what made a logged-in, actively-billing Claude account disappear
// from the UI the moment a self-update dropped the executable bit.
//
// Evidence is a SET, not a single file — a lone credentials file can be residue from an
// explicit logout, so we report WHICH evidence we found and let the UI phrase itself
// honestly ("已登录" vs "仅存历史记录") instead of over-claiming.
//
// Read-only, existence-level probes. We look at whether an auth file is populated and
// (for codex) which FIELD carries the credential — never at the credential's value, and
// nothing here is ever surfaced to the frontend.
//
// WHERE the files live is not decided here: kit/transcript owns root resolution for both
// runtimes, so presence, spend and quota can never read three different ~/.claude dirs.
package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/brightman-ai/kit/transcript"
)

// ── path resolution (delegated to kit/transcript — see transcript/roots.go) ───

func claudeCredentialsPath() string {
	return filepath.Join(transcript.ClaudeHome(), ".credentials.json")
}
func claudeProjectsDir() string { return transcript.ClaudeProjectsRoot() }
func codexAuthPath() string     { return filepath.Join(transcript.CodexHome(), "auth.json") }
func codexSessionsDir() string  { return transcript.CodexSessionsRoot() }

// ── evidence collection ──────────────────────────────────────────────────────

// claudePresenceEvidence gathers the account artifacts that prove a claude account
// lives on this host. hasSnapshot is passed in because the caller has already read the
// (parsed) rate-limit drop file and we must not disagree with it.
func claudePresenceEvidence(hasSnapshot bool) []string {
	var evidence []string
	if fileHasContent(claudeCredentialsPath()) {
		evidence = append(evidence, EvidenceCredentials)
	}
	if hasSnapshot {
		evidence = append(evidence, EvidenceSnapshot)
	}
	if transcript.HasAnyFile(claudeProjectsDir(), transcript.JSONLSuffix) {
		evidence = append(evidence, EvidenceSessions)
	}
	return evidence
}

// codexPresenceEvidence is the codex counterpart.
func codexPresenceEvidence(hasSnapshot bool) []string {
	var evidence []string
	if fileHasContent(codexAuthPath()) {
		evidence = append(evidence, EvidenceCredentials)
	}
	if hasSnapshot {
		evidence = append(evidence, EvidenceSnapshot)
	}
	if transcript.HasAnyFile(codexSessionsDir(), transcript.JSONLSuffix) {
		evidence = append(evidence, EvidenceSessions)
	}
	return evidence
}

// codexAuthShape is auth.json's SHAPE — which credential field is populated, never the
// value. An API key means pay-per-token billing; an OAuth token set means a ChatGPT
// subscription with 5h/7d windows.
type codexAuthShape struct {
	APIKey string `json:"OPENAI_API_KEY"`
	Tokens *struct {
		// Presence of the object is the signal; its contents are never read.
		AccountID string `json:"account_id"`
	} `json:"tokens"`
}

// codexBilling infers codex's billing mode. A rate-limit snapshot is proof of a
// subscription (API-key sessions have no such window by design). Otherwise fall back to
// the auth file's shape. Anything we cannot prove stays BillingUnknown — the UI files it
// under 「未分类」 rather than guessing which kind of money the user is spending.
func codexBilling(hasSnapshot bool) string {
	if hasSnapshot {
		return BillingSubscription
	}
	data, err := os.ReadFile(codexAuthPath()) //nolint:gosec — read-only shape probe; value never used
	if err != nil {
		return BillingUnknown
	}
	var shape codexAuthShape
	if json.Unmarshal(data, &shape) != nil {
		return BillingUnknown
	}
	switch {
	case shape.Tokens != nil:
		return BillingSubscription
	case shape.APIKey != "":
		return BillingAPI
	default:
		return BillingUnknown
	}
}

// ── low-level probes ─────────────────────────────────────────────────────────

// existsOnPath reports whether a file named `name` sits in a PATH dir, WITHOUT caring
// whether it can be executed. exec.LookPath answers "can I run this"; this answers "is it
// installed" — and the gap between the two is precisely the broken-executable case.
func existsOnPath(name string) bool {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		// Stat (not Lstat) so a symlink counts only when its target really exists —
		// a dangling link is a broken install, not an install.
		if fi, err := os.Stat(filepath.Join(dir, name)); err == nil && !fi.IsDir() {
			return true
		}
	}
	return false
}

// fileHasContent reports whether path exists and is non-empty. An empty (or truncated)
// credentials file proves nothing.
func fileHasContent(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir() && fi.Size() > 0
}

// snapshotTime resolves when a reading was captured: the reading's own epoch when it
// carries one, else the drop file's mtime. Zero when neither is available (the caller
// then reports no snapshot at all rather than inventing a timestamp).
func snapshotTime(capturedAtEpoch int64, path string) time.Time {
	if capturedAtEpoch > 0 {
		return time.Unix(capturedAtEpoch, 0)
	}
	if fi, err := os.Stat(path); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}
