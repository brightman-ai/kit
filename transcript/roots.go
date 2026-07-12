// Package transcript — roots.go: the SINGLE answer to "where does this runtime keep its
// files on disk".
//
// Every layer that reads a CLI runtime's on-disk state (session lists, agent trees, token
// usage, quota snapshots, presence probes) needs the same two directories. Resolving them
// independently is how a host ends up reading one ~/.claude for quota and a different one
// for spend: kit/usage once hardcoded ~/.claude/projects while this package honoured
// DW_CLAUDE_PROJECTS, so a CLAUDE_CONFIG_DIR user got two different truths. One resolver,
// no drift.
//
// Env precedence (per runtime, most specific first):
//
//	claude: DW_CLAUDE_PROJECTS (exact projects dir) → CLAUDE_CONFIG_DIR/projects → ~/.claude/projects
//	codex:  DW_CODEX_HOME (exact home)              → ~/.codex
//
// CLAUDE_CONFIG_DIR is the claude CLI's own knob, so honouring it means we look where the
// user's CLI actually lives. DW_* are ours, and win because they are the explicit override.
package transcript

import (
	"os"
	"path/filepath"
	"strings"
)

// ClaudeHome returns the claude CLI's config dir (~/.claude, or CLAUDE_CONFIG_DIR).
func ClaudeHome() string {
	if dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

// ClaudeProjectsRoot returns the dir holding claude's per-project transcript shards.
func ClaudeProjectsRoot() string {
	if dir := strings.TrimSpace(os.Getenv("DW_CLAUDE_PROJECTS")); dir != "" {
		return dir
	}
	return filepath.Join(ClaudeHome(), "projects")
}

// CodexHome returns the codex CLI's home dir (~/.codex, or DW_CODEX_HOME).
func CodexHome() string {
	if dir := strings.TrimSpace(os.Getenv("DW_CODEX_HOME")); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex")
}

// CodexSessionsRoot returns the dir holding codex's rollout transcripts.
func CodexSessionsRoot() string {
	return filepath.Join(CodexHome(), "sessions")
}
