package helper

import (
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// StreamResult is passed to each dataHandler invocation, providing methods
// to record soft errors, signal fatal stops, or mark normal completion.
// StreamScannerHandler checks IsStopped() after each callback invocation.
type StreamResult struct {
	status             *relaycommon.StreamStatus
	stopped            bool
	rejected           bool
	visibilityDeclared bool
	clientVisible      bool
	protocolCommitted  bool
}

func newStreamResult(status *relaycommon.StreamStatus) *StreamResult {
	return &StreamResult{status: status}
}

// Error records a soft error. The stream continues processing.
// Can be called multiple times per chunk.
func (r *StreamResult) Error(err error) {
	if err == nil {
		return
	}
	r.status.RecordError(err.Error())
	r.rejected = true
}

// Stop records a fatal error and marks the stream to stop after accepting this
// chunk. Use RejectAndStop when the current frame is itself an error event and
// must not count as downstream-visible protocol data.
func (r *StreamResult) Stop(err error) {
	if err != nil {
		r.status.RecordError(err.Error())
	}
	r.status.SetEndReason(relaycommon.StreamEndReasonHandlerStop, err)
	r.stopped = true
}

// RejectAndStop records one fatal error, rejects the current frame, and stops
// scanning. Rejected frames do not increment ReceivedResponseCount and cannot
// stop the first-token timer or make transparent retry unsafe.
func (r *StreamResult) RejectAndStop(err error) {
	if err != nil {
		r.status.RecordError(err.Error())
	}
	r.status.SetEndReason(relaycommon.StreamEndReasonHandlerStop, err)
	r.rejected = true
	r.stopped = true
}

// Done signals that the handler has finished processing normally
// (e.g., Dify "message_end"). The stream stops after this chunk.
func (r *StreamResult) Done() {
	r.status.SetEndReason(relaycommon.StreamEndReasonDone, nil)
	r.stopped = true
}

// IsStopped returns whether Stop() or Done() was called during this chunk.
func (r *StreamResult) IsStopped() bool {
	return r.stopped
}

func (r *StreamResult) IsAccepted() bool {
	return !r.rejected
}

// ClientVisible declares that this callback successfully flushed valid
// business data to the downstream client. It is intentionally separate from
// acceptance: an adaptor may accept and cache a frame without writing it.
func (r *StreamResult) ClientVisible() {
	r.visibilityDeclared = true
	r.clientVisible = true
	r.protocolCommitted = true
	r.status.MarkClientVisible()
}

// ProtocolCommitted declares that this callback wrote SSE protocol data which
// is not a valid first token (for example an empty delta or usage-only frame).
// It does not stop the first-token timer, but it makes transparent replay
// unsafe if the current attempt subsequently fails.
func (r *StreamResult) ProtocolCommitted() {
	r.visibilityDeclared = true
	r.protocolCommitted = true
	r.status.MarkProtocolCommitted()
}

// Buffered declares that the adaptor accepted the frame but kept it entirely
// server-side. Buffered data remains retryable until something is flushed.
func (r *StreamResult) Buffered() {
	r.visibilityDeclared = true
	r.status.TrackVisibility()
}

func (r *StreamResult) isClientVisible() bool {
	return r.clientVisible
}

func (r *StreamResult) isProtocolCommitted() bool {
	return r.protocolCommitted
}

func (r *StreamResult) visibilityWasDeclared() bool {
	return r.visibilityDeclared
}

// reset clears the per-chunk state so the object can be reused.
func (r *StreamResult) reset() {
	r.stopped = false
	r.rejected = false
	r.visibilityDeclared = false
	r.clientVisible = false
	r.protocolCommitted = false
}
