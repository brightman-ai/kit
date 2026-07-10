package transcript

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// TouchedFile is one "涉及文件" row: a work-product file the session's edit tools
// touched, addressed by a source-root-relative path (the absolute host path never
// leaves this layer). It is the domain result of ExtractTouched, consumed by every
// file-view host (share visitor / ws owner / terminal) through one endpoint shape.
type TouchedFile struct {
	Name      string `json:"name"`
	Path      string `json:"path"` // relative to rootDir
	IsDir     bool   `json:"is_dir"`
	Size      int64  `json:"size"`
	TouchedAt int64  `json:"touched_at,omitempty"` // unix ms of the tool_use that touched it
	Tool      string `json:"tool,omitempty"`       // the edit tool (Write/Edit/…)
}

// editToolFilePath are the tools whose tool_input.file_path names a real edited
// file. Mirrors deepwork-terminal's proven allowlist: only the EDIT tools produce
// a work-product path; Read counts only for images; Bash is excluded (its
// unstructured `command` would surface noise like rm/cat).
var editToolFilePath = map[string]bool{
	"Write": true, "Edit": true, "MultiEdit": true, "NotebookEdit": true,
}

var imageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".bmp": true, ".ico": true, ".svg": true, ".avif": true,
}

// TouchedPath is the edit-tool allowlist PREDICATE — the irreducible domain rule of
// "which tool_use named a work-product file": only the edit tools count via file_path,
// Read counts only for images, everything else (Bash, path-free tools) does not.
//
// This is the cross-repo SSOT for the rule (not the traversal): kit's ExtractTouched
// applies it over a whole parsed Transcript (pro share/owner per-session view), while
// deepwork-terminal's incremental tail-window project scanner applies it per raw jsonl
// tool line (its perf-critical cross-project "recent files" view keeps its own reader,
// but delegates the allowlist decision here so the rule can never diverge).
func TouchedPath(toolName string, toolInput map[string]interface{}) (string, bool) {
	if editToolFilePath[toolName] {
		if v, ok := toolInput["file_path"].(string); ok && strings.TrimSpace(v) != "" {
			return v, true
		}
	}
	if toolName == "Read" {
		if v, ok := toolInput["file_path"].(string); ok && strings.TrimSpace(v) != "" {
			if imageExts[strings.ToLower(filepath.Ext(v))] {
				return v, true
			}
		}
	}
	return "", false
}

// touchedInputPath adapts a parsed Block onto the shared predicate.
func touchedInputPath(b *Block) (string, bool) {
	if b == nil {
		return "", false
	}
	return TouchedPath(b.ToolName, b.ToolInput)
}

// ExtractTouched walks the transcript (incl. agent subflows) and returns the files
// the session touched, root-clamped, existence-checked, newest-first. Dedup keeps the
// newest tool_use per path. Every path is clamped to rootDir so a tool that touched
// outside the shared project never leaks into the list.
func ExtractTouched(tr *Transcript, rootDir string) []TouchedFile {
	if tr == nil {
		return nil
	}
	root := filepath.Clean(rootDir)
	type hit struct {
		tsMs int64
		tool string
	}
	seen := map[string]hit{}

	var walk func(blocks []Block, tsMs int64)
	walk = func(blocks []Block, tsMs int64) {
		for i := range blocks {
			b := &blocks[i]
			if b.Type == BlockTool || b.Type == BlockAgent {
				if p, ok := touchedInputPath(b); ok {
					abs := p
					if !filepath.IsAbs(abs) {
						abs = filepath.Join(root, abs)
					}
					abs = filepath.Clean(abs)
					if abs == root || strings.HasPrefix(abs, root+string(os.PathSeparator)) {
						if cur, ok := seen[abs]; !ok || tsMs >= cur.tsMs {
							seen[abs] = hit{tsMs: tsMs, tool: b.ToolName}
						}
					}
				}
			}
			if len(b.Children) > 0 {
				walk(b.Children, tsMs)
			}
		}
	}
	for ti := range tr.Turns {
		var tsMs int64
		if tr.Turns[ti].At != nil {
			tsMs = tr.Turns[ti].At.UnixMilli()
		}
		walk(tr.Turns[ti].Blocks, tsMs)
	}

	out := make([]TouchedFile, 0, len(seen))
	for abs, h := range seen {
		fi, err := os.Stat(abs)
		if err != nil || fi.IsDir() {
			continue // deleted after the edit, or a dir → not a previewable file
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			continue
		}
		out = append(out, TouchedFile{
			Name:      filepath.Base(abs),
			Path:      rel,
			IsDir:     false,
			Size:      fi.Size(),
			TouchedAt: h.tsMs,
			Tool:      h.tool,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TouchedAt > out[j].TouchedAt })
	return out
}
