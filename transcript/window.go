package transcript

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
)

const (
	defaultWindowLimit = 12
	maxWindowLimit     = 50
	reverseScanChunk   = 4 << 20
)

type boundaryClassifier func([]byte) (bool, string)

type sourceWindowRange struct {
	start      int64
	end        int64
	hasMore    bool
	version    string
	generation string
	reset      bool
}

func normalizeWindowLimit(limit int) int {
	if limit <= 0 {
		return defaultWindowLimit
	}
	if limit > maxWindowLimit {
		return maxWindowLimit
	}
	return limit
}

// resolveSourceWindow scans backward in bounded chunks until it finds enough runtime
// yield boundaries. It never parses the immutable prefix merely to serve the tail.
func resolveSourceWindow(ctx context.Context, path string, req WindowRequest, classify boundaryClassifier) (sourceWindowRange, error) {
	f, err := os.Open(path)
	if err != nil {
		return sourceWindowRange{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return sourceWindowRange{}, err
	}

	size := st.Size()
	version := fmt.Sprintf("%x-%x", size, st.ModTime().UnixNano())
	generation, err := sourceGeneration(f, size)
	if err != nil {
		return sourceWindowRange{}, err
	}

	end := size
	reset := req.Generation != "" && req.Generation != generation
	if req.Before != nil && !reset {
		candidate := *req.Before
		if candidate >= 0 && candidate <= size && validLineBoundary(f, candidate, size) {
			end = candidate
		} else {
			reset = true
		}
	}

	start, err := reverseWindowStart(ctx, f, end, normalizeWindowLimit(req.Limit), classify)
	if err != nil {
		return sourceWindowRange{}, err
	}
	return sourceWindowRange{
		start: start, end: end, hasMore: start > 0,
		version: version, generation: generation, reset: reset,
	}, nil
}

func validLineBoundary(f *os.File, cursor, size int64) bool {
	if cursor == 0 || cursor == size {
		return true
	}
	var b [1]byte
	_, err := f.ReadAt(b[:], cursor-1)
	return err == nil && b[0] == '\n'
}

// sourceGeneration hashes the immutable prefix. Unlike size/mtime it survives append,
// so an older-page cursor remains valid while the file grows but resets on replacement.
func sourceGeneration(f *os.File, size int64) (string, error) {
	n := int64(64 << 10)
	if size < n {
		n = size
	}
	b := make([]byte, n)
	if n > 0 {
		if _, err := f.ReadAt(b, 0); err != nil && err != io.EOF {
			return "", err
		}
	}
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		b = b[:i+1]
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:8]), nil
}

func reverseWindowStart(ctx context.Context, f *os.File, end int64, limit int, classify boundaryClassifier) (int64, error) {
	base := end
	data := make([]byte, 0, reverseScanChunk)
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		start := base - reverseScanChunk
		if start < 0 {
			start = 0
		}
		chunk := make([]byte, base-start)
		if len(chunk) > 0 {
			if _, err := f.ReadAt(chunk, start); err != nil && err != io.EOF {
				return 0, err
			}
			merged := make([]byte, 0, len(chunk)+len(data))
			merged = append(merged, chunk...)
			merged = append(merged, data...)
			data = merged
		}
		base = start

		boundaries := classifyBoundaries(data, base, base > 0, classify)
		// One extra boundary is the cursor immediately before the oldest requested
		// completed run. An open tail may overfetch one run; continuity wins over a
		// gap, and the bound remains O(limit) runtime rounds.
		if len(boundaries) >= limit+1 {
			return boundaries[len(boundaries)-(limit+1)], nil
		}
		if base == 0 {
			return 0, nil
		}
	}
}

func classifyBoundaries(data []byte, base int64, skipPartialPrefix bool, classify boundaryClassifier) []int64 {
	start := 0
	if skipPartialPrefix {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			start = i + 1
		} else {
			return nil
		}
	}
	boundaries := make([]int64, 0, 16)
	seen := make(map[string]struct{})
	for start < len(data) {
		rel := bytes.IndexByte(data[start:], '\n')
		lineEnd := len(data)
		cursorEnd := len(data)
		if rel >= 0 {
			lineEnd = start + rel
			cursorEnd = lineEnd + 1
		}
		line := bytes.TrimSpace(data[start:lineEnd])
		if ok, key := classify(line); ok {
			if key == "" {
				key = fmt.Sprintf("%d", base+int64(cursorEnd))
			}
			if _, duplicate := seen[key]; !duplicate {
				seen[key] = struct{}{}
				boundaries = append(boundaries, base+int64(cursorEnd))
			}
		}
		if rel < 0 {
			break
		}
		start = cursorEnd
	}
	return boundaries
}

func sectionReader(path string, start, end int64) (io.ReadCloser, io.Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	return f, io.NewSectionReader(f, start, end-start), nil
}
