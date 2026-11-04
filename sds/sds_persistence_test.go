//go:build !lint

package sds

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type persistenceCall struct {
	operation string
	channelID string
}

type recordingPersistence struct {
	mu    sync.Mutex
	calls []persistenceCall
}

func (p *recordingPersistence) record(operation, channelID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, persistenceCall{operation, channelID})
}

func (p *recordingPersistence) SaveChannelMeta(channelID string, meta []byte) error {
	p.record("save", channelID)
	return nil
}

func (p *recordingPersistence) UpdateHistory(channelID string, update HistoryUpdate) error {
	p.record("update", channelID)
	return nil
}

func (p *recordingPersistence) LoadChannel(channelID string) (ChannelData, error) {
	p.record("load", channelID)
	return ChannelData{}, nil
}

func (p *recordingPersistence) DropChannel(channelID string) error {
	p.record("drop", channelID)
	return nil
}

func TestPersistenceCallbackChannelID(t *testing.T) {
	for _, channelID := range []string{"persistence-channel", "channel\x00with-null"} {
		t.Run(channelID, func(t *testing.T) {
			persistence := &recordingPersistence{}
			rm, err := NewReliabilityManager(nil, persistence)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, rm.Cleanup()) })

			wrapped, err := rm.WrapOutgoingMessage([]byte("payload"), "message", channelID)
			require.NoError(t, err)
			_, err = rm.UnwrapReceivedMessage(wrapped)
			require.NoError(t, err)

			persistence.mu.Lock()
			defer persistence.mu.Unlock()
			operations := make(map[string]bool)
			for _, call := range persistence.calls {
				require.Equal(t, channelID, call.channelID)
				operations[call.operation] = true
			}
			require.True(t, operations["load"])
			require.True(t, operations["save"])
			require.True(t, operations["update"])
		})
	}
}
