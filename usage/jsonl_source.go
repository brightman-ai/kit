// Package usage provides subscription quota inspection and usage reporting.
// jsonl_source.go: TokenSource backed by Claude JSONL transcript files.
//
// Data path: ~/.claude/projects/**/*.jsonl
// Each JSONL file contains rows with type="assistant" that have message.usage.
// Because Claude streams updates, duplicate rows per message are possible —
// we use max-per-key dedup (same strategy as agent_intel.UsageAccumulator).
//
// Reset-on-restart: this source is stateless/read-only; it rescans files on
// every DailyTokens call. There is no persistent DB backing.
// Limitation: counts only Claude CLI sessions (Codex/Gemini not yet included).
package usage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// JSONLTokenSource implements TokenSource by scanning Claude JSONL transcripts.
// It rescans on every call — suitable for low-frequency reporting endpoints.
type JSONLTokenSource struct {
	// claudeProjectsDir is the root scanning directory.
	// Defaults to ~/.claude/projects when empty.
	claudeProjectsDir string
}

// NewJSONLTokenSource creates a new source scanning the default claude projects dir.
func NewJSONLTokenSource() *JSONLTokenSource {
	return &JSONLTokenSource{}
}

// NewJSONLTokenSourceAt creates a source for a custom projects directory (tests).
func NewJSONLTokenSourceAt(dir string) *JSONLTokenSource {
	return &JSONLTokenSource{claudeProjectsDir: dir}
}

func (s *JSONLTokenSource) projectsDir() string {
	if s.claudeProjectsDir != "" {
		return s.claudeProjectsDir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "projects")
}

// dedupKey identifies a unique assistant message across all JSONL files.
type dedupKey struct {
	file      string
	messageID string
}

// rangeKey identifies a unique assistant message within a per-day bucket (codex
// H4): the date partitions the buckets, then (file, messageID) dedups within.
type rangeKey struct {
	date      string
	file      string
	messageID string
}

// tokenEntry holds the max-seen token counts for a dedupKey.
type tokenEntry struct {
	input       int64
	output      int64
	cacheRead   int64
	cacheCreate int64
}

// modelKey identifies a unique assistant message within a per-(date, model) bucket
// (CHG-014 R3 cost dim): partition by (date, model), then dedup by (file, messageID).
type modelKey struct {
	date      string
	model     string
	file      string
	messageID string
}

// DailyTokens returns deduplicated token totals for the given UTC date (YYYY-MM-DD).
// Scans all *.jsonl files under ~/.claude/projects/ and returns (input, output, cacheRead, nil).
// Returns (0, 0, 0, nil) when no data exists for that date.
func (s *JSONLTokenSource) DailyTokens(date string) (inputTokens, outputTokens, cacheReadTokens int64, err error) {
	root := s.projectsDir()
	byKey := make(map[dedupKey]tokenEntry)

	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		scanJSONLForDate(path, date, byKey)
		return nil
	})
	if walkErr != nil && !os.IsNotExist(walkErr) {
		// If the root doesn't exist that's fine — return zeros.
		return 0, 0, 0, nil
	}

	for _, e := range byKey {
		inputTokens += e.input
		outputTokens += e.output
		cacheReadTokens += e.cacheRead
	}
	return inputTokens, outputTokens, cacheReadTokens, nil
}

// ScanRange implements RangeTokenSource (codex H4): it walks the JSONL tree ONCE
// and buckets every in-range assistant message by its UTC date, returning a
// date→DayTokens map. This replaces the old per-day DailyTokens path that walked
// the entire tree once PER DAY (7×/30× per report request). Dedup is per
// (date, file, messageID) with max-per-key, matching DailyTokens semantics.
func (s *JSONLTokenSource) ScanRange(startDate, endDate string) (map[string]DayTokens, error) {
	root := s.projectsDir()
	if startDate > endDate {
		startDate, endDate = endDate, startDate
	}
	byKey := make(map[rangeKey]tokenEntry)

	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		scanJSONLForRange(path, startDate, endDate, byKey)
		return nil
	})

	out := make(map[string]DayTokens)
	for k, e := range byKey {
		bt := out[k.date]
		bt.InputTokens += e.input
		bt.OutputTokens += e.output
		bt.CacheReadTokens += e.cacheRead
		out[k.date] = bt
	}
	return out, nil
}

// ScanModelRange implements ModelScanSource (CHG-014 R3 cost dim): one tree-walk
// that buckets every in-range assistant message by (UTC date, model), capturing all
// four token categories (incl. cache_creation, which the aggregate ScanRange drops).
// Dedup is per (date, model, file, messageID) with max-per-key, matching ScanRange.
func (s *JSONLTokenSource) ScanModelRange(startDate, endDate string) ([]ModelTokens, error) {
	root := s.projectsDir()
	if startDate > endDate {
		startDate, endDate = endDate, startDate
	}
	byKey := make(map[modelKey]tokenEntry)

	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		scanJSONLForModelRange(path, startDate, endDate, byKey)
		return nil
	})

	// Re-aggregate dedup'd entries up to (date, model).
	type dm struct {
		date  string
		model string
	}
	agg := make(map[dm]tokenEntry)
	for k, e := range byKey {
		key := dm{date: k.date, model: k.model}
		prev := agg[key]
		agg[key] = tokenEntry{
			input:       prev.input + e.input,
			output:      prev.output + e.output,
			cacheRead:   prev.cacheRead + e.cacheRead,
			cacheCreate: prev.cacheCreate + e.cacheCreate,
		}
	}
	out := make([]ModelTokens, 0, len(agg))
	for k, e := range agg {
		out = append(out, ModelTokens{
			Date:              k.date,
			Model:             k.model,
			InputTokens:       e.input,
			OutputTokens:      e.output,
			CacheReadTokens:   e.cacheRead,
			CacheCreateTokens: e.cacheCreate,
		})
	}
	return out, nil
}

// scanJSONLForModelRange reads path once and records max-per-(date,model,file,msgID)
// token usage for every assistant row whose UTC date falls within [start, end].
func scanJSONLForModelRange(path, startDate, endDate string, byKey map[modelKey]tokenEntry) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	br := bufio.NewReaderSize(f, 256*1024)
	for {
		line, readErr := br.ReadBytes('\n')
		if len(line) > 0 {
			processJSONLLineModelRange(path, startDate, endDate, bytes.TrimRight(line, "\r\n"), byKey)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			break
		}
	}
}

// processJSONLLineModelRange parses one assistant row, computes its UTC date + model,
// and — if in range — records the four token categories (incl. cache_creation).
func processJSONLLineModelRange(filePath, startDate, endDate string, trimmed []byte, byKey map[modelKey]tokenEntry) {
	if len(trimmed) == 0 || !bytes.Contains(trimmed, []byte(`"assistant"`)) {
		return
	}
	var row struct {
		Type      string          `json:"type"`
		Timestamp string          `json:"timestamp"`
		Message   json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(trimmed, &row); err != nil || row.Type != "assistant" || row.Timestamp == "" {
		return
	}
	t, err := time.Parse(time.RFC3339Nano, row.Timestamp)
	if err != nil {
		return
	}
	date := t.UTC().Format("2006-01-02")
	if date < startDate || date > endDate {
		return
	}

	var msg struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage struct {
			InputTokens              float64 `json:"input_tokens"`
			OutputTokens             float64 `json:"output_tokens"`
			CacheReadInputTokens     float64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens float64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(row.Message, &msg); err != nil || msg.ID == "" {
		return
	}
	model := msg.Model
	if model == "" {
		model = "unknown"
	}

	k := modelKey{date: date, model: model, file: filePath, messageID: msg.ID}
	prev := byKey[k]
	byKey[k] = tokenEntry{
		input:       maxInt64(prev.input, int64(msg.Usage.InputTokens)),
		output:      maxInt64(prev.output, int64(msg.Usage.OutputTokens)),
		cacheRead:   maxInt64(prev.cacheRead, int64(msg.Usage.CacheReadInputTokens)),
		cacheCreate: maxInt64(prev.cacheCreate, int64(msg.Usage.CacheCreationInputTokens)),
	}
}

// scanJSONLForRange reads path once and accumulates max-per-key token usage for
// every assistant row whose UTC date falls within [startDate, endDate].
func scanJSONLForRange(path, startDate, endDate string, byKey map[rangeKey]tokenEntry) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	br := bufio.NewReaderSize(f, 256*1024)
	for {
		line, readErr := br.ReadBytes('\n')
		if len(line) > 0 {
			processJSONLLineRange(path, startDate, endDate, bytes.TrimRight(line, "\r\n"), byKey)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			break
		}
	}
}

// processJSONLLineRange is the range variant of processJSONLLine: it parses one
// row, computes its UTC date, and — if the date is in [startDate, endDate] —
// records max-per-(date,file,messageID) token usage.
func processJSONLLineRange(filePath, startDate, endDate string, trimmed []byte, byKey map[rangeKey]tokenEntry) {
	if len(trimmed) == 0 {
		return
	}
	if !bytes.Contains(trimmed, []byte(`"assistant"`)) {
		return
	}

	var row struct {
		Type      string          `json:"type"`
		Timestamp string          `json:"timestamp"`
		Message   json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(trimmed, &row); err != nil {
		return
	}
	if row.Type != "assistant" || row.Timestamp == "" {
		return
	}
	t, err := time.Parse(time.RFC3339Nano, row.Timestamp)
	if err != nil {
		return
	}
	date := t.UTC().Format("2006-01-02")
	// Lexical comparison is valid for fixed-width YYYY-MM-DD dates.
	if date < startDate || date > endDate {
		return
	}

	var msg struct {
		ID    string `json:"id"`
		Usage struct {
			InputTokens          float64 `json:"input_tokens"`
			OutputTokens         float64 `json:"output_tokens"`
			CacheReadInputTokens float64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(row.Message, &msg); err != nil {
		return
	}
	if msg.ID == "" {
		return
	}

	k := rangeKey{date: date, file: filePath, messageID: msg.ID}
	prev := byKey[k]
	byKey[k] = tokenEntry{
		input:     maxInt64(prev.input, int64(msg.Usage.InputTokens)),
		output:    maxInt64(prev.output, int64(msg.Usage.OutputTokens)),
		cacheRead: maxInt64(prev.cacheRead, int64(msg.Usage.CacheReadInputTokens)),
	}
}

// scanJSONLForDate reads path and accumulates max-per-key token usage for rows
// whose timestamp falls on date (UTC, YYYY-MM-DD format).
func scanJSONLForDate(path string, date string, byKey map[dedupKey]tokenEntry) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	br := bufio.NewReaderSize(f, 256*1024)
	for {
		line, readErr := br.ReadBytes('\n')
		if len(line) > 0 {
			processJSONLLine(path, date, bytes.TrimRight(line, "\r\n"), byKey)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			break
		}
	}
}

func processJSONLLine(filePath, date string, trimmed []byte, byKey map[dedupKey]tokenEntry) {
	if len(trimmed) == 0 {
		return
	}
	// Fast pre-filter: skip lines that don't look like assistant rows.
	if !bytes.Contains(trimmed, []byte(`"assistant"`)) {
		return
	}

	var row struct {
		Type      string          `json:"type"`
		Timestamp string          `json:"timestamp"`
		Message   json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(trimmed, &row); err != nil {
		return
	}
	if row.Type != "assistant" {
		return
	}

	// Validate timestamp matches requested date.
	if row.Timestamp == "" {
		return
	}
	t, err := time.Parse(time.RFC3339Nano, row.Timestamp)
	if err != nil {
		return
	}
	if t.UTC().Format("2006-01-02") != date {
		return
	}

	// Extract message.id and message.usage.
	var msg struct {
		ID    string `json:"id"`
		Usage struct {
			InputTokens              float64 `json:"input_tokens"`
			OutputTokens             float64 `json:"output_tokens"`
			CacheReadInputTokens     float64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens float64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(row.Message, &msg); err != nil {
		return
	}
	if msg.ID == "" {
		return
	}

	k := dedupKey{file: filePath, messageID: msg.ID}
	prev := byKey[k]
	byKey[k] = tokenEntry{
		input:     maxInt64(prev.input, int64(msg.Usage.InputTokens)),
		output:    maxInt64(prev.output, int64(msg.Usage.OutputTokens)),
		cacheRead: maxInt64(prev.cacheRead, int64(msg.Usage.CacheReadInputTokens)),
	}
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
