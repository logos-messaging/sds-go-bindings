package sds

type MessageID string

type HistoryEntry struct {
	MessageID     MessageID `json:"messageId"`
	RetrievalHint []byte    `json:"retrievalHint"`
}

type UnwrappedMessage struct {
	Message     *[]byte         `json:"message"`
	MissingDeps *[]HistoryEntry `json:"missingDeps"`
	ChannelId   *string         `json:"channelId"`
}

// ValidationMessage contains decoded SDS content and opaque host-provided context.
// SenderID is untrusted wire metadata, not an authenticated sender identity.
// The slices are owned by Go and may be retained by the validator.
type ValidationMessage struct {
	ChannelID         string
	Content           []byte
	SenderID          string
	ValidationContext []byte
}

// Validator authorizes an incoming message before SDS processes its metadata.
// Validate runs synchronously on the native worker, including for duplicates
// and empty-content syncs. It must not re-enter the same ReliabilityManager.
type Validator interface {
	Validate(message ValidationMessage) (bool, error)
}

// TickEvent is the result for one ensured channel. A payload is present when
// the channel is due for sync; Error is set when generation failed for it.
type TickEvent struct {
	ChannelID string `json:"channelId"`
	Payload   []byte `json:"payload,omitempty"`
	Error     string `json:"error,omitempty"`
}

type HistoryUpdate struct {
	Append []HistoryMessage `json:"append"`
	Evict  []string         `json:"evict"`
}

type HistoryMessage struct {
	MessageID        string `json:"messageId"`
	LamportTimestamp int64  `json:"lamportTimestamp"`
	Data             []byte `json:"data"`
}

type ChannelData struct {
	Meta           []byte   `json:"meta"`
	MessageHistory [][]byte `json:"messageHistory"`
}

// PersistenceCallbacks stores and restores per-channel SDS state.
// Metadata and serialized history messages are opaque bytes and must be preserved
// unchanged. Implementations must return storage errors rather than discard them.
// SDS logs save/update failures without aborting the protocol operation;
// load/drop failures propagate to the caller.
type PersistenceCallbacks interface {
	// SaveChannelMeta replaces the channel's complete metadata snapshot.
	// Saving the same snapshot repeatedly must be safe.
	SaveChannelMeta(channelID string, meta []byte) error

	// UpdateHistory atomically stores Append entries (using each entry's Data
	// as the serialized message) and deletes Evict entries by message ID.
	// Reapplying an update must be safe because SDS may retry failed writes.
	UpdateHistory(channelID string, update HistoryUpdate) error

	// LoadChannel returns the persisted metadata and serialized message history.
	// History must be ordered by LamportTimestamp ascending, then MessageID
	// ascending. A new channel must return an empty ChannelData and nil error.
	LoadChannel(channelID string) (ChannelData, error)

	// DropChannel atomically deletes all metadata and history for the channel.
	// It must return an error if deletion fails.
	DropChannel(channelID string) error
}
