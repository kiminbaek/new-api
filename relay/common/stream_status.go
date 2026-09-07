package common

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type StreamEndReason string

const (
	StreamEndReasonNone              StreamEndReason = ""
	StreamEndReasonDone              StreamEndReason = "done"
	StreamEndReasonTimeout           StreamEndReason = "timeout"
	StreamEndReasonFirstTokenTimeout StreamEndReason = "first_token_timeout"
	StreamEndReasonClientGone        StreamEndReason = "client_gone"
	StreamEndReasonScannerErr        StreamEndReason = "scanner_error"
	StreamEndReasonHandlerStop       StreamEndReason = "handler_stop"
	StreamEndReasonEOF               StreamEndReason = "eof"
	StreamEndReasonPanic             StreamEndReason = "panic"
	StreamEndReasonPingFail          StreamEndReason = "ping_fail"
)

const maxStreamErrorEntries = 20

type StreamErrorEntry struct {
	Message   string
	Timestamp time.Time
}

type StreamStatus struct {
	EndReason StreamEndReason
	EndError  error
	endOnce   sync.Once

	mu         sync.Mutex
	Errors     []StreamErrorEntry
	ErrorCount int

	visibilityTracked atomic.Bool
	clientVisible     atomic.Bool
	protocolCommitted atomic.Bool
}

func NewStreamStatus() *StreamStatus {
	return &StreamStatus{}
}

func (s *StreamStatus) SetEndReason(reason StreamEndReason, err error) {
	if s == nil {
		return
	}
	s.endOnce.Do(func() {
		s.EndReason = reason
		s.EndError = err
	})
}

// MarkClientVisible records that valid business data (content, reasoning, or
// tool-call data) has been flushed to the downstream client. Once true, a
// relay attempt must never be transparently replayed on another upstream.
func (s *StreamStatus) MarkClientVisible() {
	if s == nil {
		return
	}
	s.visibilityTracked.Store(true)
	s.clientVisible.Store(true)
	s.protocolCommitted.Store(true)
}

func (s *StreamStatus) ClientVisible() bool {
	return s != nil && s.clientVisible.Load()
}

// MarkProtocolCommitted records that bytes belonging to this SSE response
// have reached the client. Keepalives do not count as a first token, but they
// still make transparent replay unsafe because a second attempt would splice
// a new protocol stream into the existing response.
func (s *StreamStatus) MarkProtocolCommitted() {
	if s != nil {
		s.visibilityTracked.Store(true)
		s.protocolCommitted.Store(true)
	}
}

// TrackVisibility opts an adaptor into explicit downstream visibility. Until
// opted in, ReceivedResponseCount remains the compatibility signal for older
// adaptors which write accepted chunks directly.
func (s *StreamStatus) TrackVisibility() {
	if s != nil {
		s.visibilityTracked.Store(true)
	}
}

func (s *StreamStatus) VisibilityTracked() bool {
	return s != nil && s.visibilityTracked.Load()
}

func (s *StreamStatus) ProtocolCommitted() bool {
	return s != nil && s.protocolCommitted.Load()
}

func (s *StreamStatus) RecordError(msg string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ErrorCount++
	if len(s.Errors) < maxStreamErrorEntries {
		s.Errors = append(s.Errors, StreamErrorEntry{
			Message:   msg,
			Timestamp: time.Now(),
		})
	}
}

func (s *StreamStatus) HasErrors() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ErrorCount > 0
}

func (s *StreamStatus) TotalErrorCount() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ErrorCount
}

func (s *StreamStatus) IsNormalEnd() bool {
	if s == nil {
		return true
	}
	return s.EndReason == StreamEndReasonDone ||
		s.EndReason == StreamEndReasonEOF ||
		s.EndReason == StreamEndReasonHandlerStop
}

func (s *StreamStatus) Summary() string {
	if s == nil {
		return "StreamStatus<nil>"
	}
	b := &strings.Builder{}
	fmt.Fprintf(b, "reason=%s", s.EndReason)
	if s.EndError != nil {
		fmt.Fprintf(b, " end_error=%q", s.EndError.Error())
	}
	s.mu.Lock()
	if s.ErrorCount > 0 {
		fmt.Fprintf(b, " soft_errors=%d", s.ErrorCount)
	}
	s.mu.Unlock()
	return b.String()
}
