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
