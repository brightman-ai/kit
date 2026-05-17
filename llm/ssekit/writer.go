// Package ssekit provides production-grade Server-Sent Events (SSE) writer and reader.
// Zero LLM knowledge — pure SSE transport over net/http.
// Designed for extraction to github.com/brightman-ai/kit/ssekit.
package ssekit

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Writer writes SSE events to an http.ResponseWriter.
// It sets the required headers on first write and flushes after each event.
type Writer struct {
	w           io.Writer
	flusher     http.Flusher
	headersSent bool
	raw         http.ResponseWriter // nil when wrapping plain io.Writer
}

// NewWriter creates an SSE writer from an http.ResponseWriter.
// Headers (Content-Type, Cache-Control, Connection) are set on first Write call.
func NewWriter(w http.ResponseWriter) *Writer {
	f, _ := w.(http.Flusher)
	return &Writer{w: w, flusher: f, raw: w}
}

// NewWriterFromIO creates an SSE writer from a plain io.Writer (for testing).
func NewWriterFromIO(w io.Writer) *Writer {
	return &Writer{w: w}
}

func (s *Writer) ensureHeaders() {
	if s.headersSent || s.raw == nil {
		return
	}
	s.raw.Header().Set("Content-Type", "text/event-stream")
	s.raw.Header().Set("Cache-Control", "no-cache")
	s.raw.Header().Set("Connection", "keep-alive")
	s.raw.Header().Set("X-Accel-Buffering", "no")
	s.headersSent = true
}

// WriteData writes a data-only SSE event: "data: {data}\n\n".
func (s *Writer) WriteData(data []byte) error {
	s.ensureHeaders()
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", data); err != nil {
		return err
	}
	s.flush()
	return nil
}

// WriteEvent writes a named SSE event: "event: {event}\ndata: {data}\n\n".
func (s *Writer) WriteEvent(event string, data []byte) error {
	s.ensureHeaders()
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return err
	}
	s.flush()
	return nil
}

// WriteJSON marshals v as JSON and writes it as an SSE data event.
// This is the primary method for streaming structured events.
func (s *Writer) WriteJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("ssekit: marshal: %w", err)
	}
	return s.WriteData(data)
}

// WriteEventWithID writes a named SSE event with an ID field: "id: {id}\nevent: {event}\ndata: {data}\n\n".
// The ID field enables client-side resume via Last-Event-ID.
func (s *Writer) WriteEventWithID(id, event string, data []byte) error {
	s.ensureHeaders()
	if _, err := fmt.Fprintf(s.w, "id: %s\nevent: %s\ndata: %s\n\n", id, event, data); err != nil {
		return err
	}
	s.flush()
	return nil
}

// WriteComment writes an SSE comment line (": {text}\n\n"), useful for keepalive.
func (s *Writer) WriteComment(text string) error {
	s.ensureHeaders()
	if _, err := fmt.Fprintf(s.w, ": %s\n\n", text); err != nil {
		return err
	}
	s.flush()
	return nil
}

func (s *Writer) flush() {
	if s.flusher != nil {
		s.flusher.Flush()
	}
}
