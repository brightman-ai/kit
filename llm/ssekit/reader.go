package ssekit

import (
	"bufio"
	"io"
	"strings"
)

// Event represents a single parsed SSE event from a stream.
type Event struct {
	Type string // from "event:" line; empty if data-only
	Data string // concatenated data lines (no trailing newline)
}

// Reader parses Server-Sent Events from an io.Reader.
// It handles multi-line data fields, event types, and comment lines.
type Reader struct {
	scanner *bufio.Scanner
}

// NewReader creates an SSE reader. The reader parses the standard SSE format:
//
//	event: {type}\n
//	data: {line1}\n
//	data: {line2}\n
//	\n
func NewReader(r io.Reader) *Reader {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64*1024), 1024*1024) // 1MB max line
	return &Reader{scanner: s}
}

// Next reads the next SSE event. Returns io.EOF when the stream ends.
// Comment lines (starting with ":") are silently skipped.
// Empty events (no data) are silently skipped.
func (r *Reader) Next() (Event, error) {
	var (
		eventType string
		dataLines []string
	)

	for r.scanner.Scan() {
		line := r.scanner.Text()

		// Empty line = end of event
		if line == "" {
			if len(dataLines) > 0 {
				return Event{
					Type: eventType,
					Data: strings.Join(dataLines, "\n"),
				}, nil
			}
			// Empty event (no data lines), reset and continue
			eventType = ""
			continue
		}

		// Comment line
		if strings.HasPrefix(line, ":") {
			continue
		}

		// Parse field
		if strings.HasPrefix(line, "event:") {
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			data := strings.TrimPrefix(line, "data:")
			if len(data) > 0 && data[0] == ' ' {
				data = data[1:] // strip single leading space per SSE spec
			}
			dataLines = append(dataLines, data)
		} else if strings.HasPrefix(line, "data: ") {
			dataLines = append(dataLines, strings.TrimPrefix(line, "data: "))
		}
		// "id:", "retry:" fields are silently ignored for now
	}

	if err := r.scanner.Err(); err != nil {
		return Event{}, err
	}

	// Stream ended — emit any pending event
	if len(dataLines) > 0 {
		return Event{
			Type: eventType,
			Data: strings.Join(dataLines, "\n"),
		}, nil
	}

	return Event{}, io.EOF
}
