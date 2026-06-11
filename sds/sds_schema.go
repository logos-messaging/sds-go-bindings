package sds

// CBOR wire types for the nim-ffi (v0.2.0+) libsds C API. Requests, responses
// and events are CBOR-marshalled; the field names below must match the Nim
// structs in nim-sds `library/libsds.nim` exactly (cbor_serialization encodes
// objects as definite-length maps keyed by the field name).
//
// The nim-ffi `.ffi.`/`.ffiCtor.` macros wrap each proc's non-context params in
// a generated request object whose field name is the Nim parameter name. So a
// proc `sdsWrapOutgoingMessage(rm, req: SdsWrapRequest)` decodes a request of
// shape `{ req: { ... } }` — i.e. the payload is nested under the param name.
// Procs with no extra params get a single `_placeholder: uint8` field.

// --- Inner payload structs (the documented logical payloads) ----------------

type sdsConfig struct {
	ParticipantID string `cbor:"participantId"`
}

type sdsWrapRequest struct {
	Message   []byte `cbor:"message"`
	MessageID string `cbor:"messageId"`
	ChannelID string `cbor:"channelId"`
}

type sdsUnwrapRequest struct {
	Message []byte `cbor:"message"`
}

type sdsMarkDependenciesRequest struct {
	MessageIDs []string `cbor:"messageIds"`
	ChannelID  string   `cbor:"channelId"`
}

type sdsMissingDep struct {
	MessageID     string `cbor:"messageId"`
	RetrievalHint []byte `cbor:"retrievalHint"`
}

// --- Request envelopes (nested under the Nim param name) ---------------------

type sdsCreateReq struct {
	Config sdsConfig `cbor:"config"`
}

type sdsWrapReq struct {
	Req sdsWrapRequest `cbor:"req"`
}

type sdsUnwrapReq struct {
	Req sdsUnwrapRequest `cbor:"req"`
}

type sdsMarkDependenciesReq struct {
	Req sdsMarkDependenciesRequest `cbor:"req"`
}

// sdsEmptyReq is the request for procs with no extra params (reset,
// startPeriodicTasks); the macro generates a single `_placeholder` field.
type sdsEmptyReq struct {
	Placeholder uint8 `cbor:"_placeholder"`
}

// --- Response payloads ------------------------------------------------------

type sdsWrapResponse struct {
	Message []byte `cbor:"message"`
}

type sdsUnwrapResponse struct {
	Message     []byte          `cbor:"message"`
	ChannelID   string          `cbor:"channelId"`
	MissingDeps []sdsMissingDep `cbor:"missingDeps"`
}

// --- Event envelope + payloads ----------------------------------------------

// Event wire names emitted by libsds.
const (
	eventMessageReady        = "message_ready"
	eventMessageSent         = "message_sent"
	eventMissingDependencies = "missing_dependencies"
	eventPeriodicSync        = "periodic_sync"
	eventRepairReady         = "repair_ready"
)

type sdsMessageEventPayload struct {
	MessageID string `cbor:"messageId"`
	ChannelID string `cbor:"channelId"`
}

type sdsMissingDependenciesPayload struct {
	MessageID   string          `cbor:"messageId"`
	ChannelID   string          `cbor:"channelId"`
	MissingDeps []sdsMissingDep `cbor:"missingDeps"`
}
