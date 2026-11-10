//go:build !lint

package sds

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestOutgoingEventsDistinguishAcknowledgmentFromExpiry(t *testing.T) {
	var acknowledged, expired []msgEvent
	rm := &ReliabilityManager{logger: zap.NewNop()}
	rm.RegisterCallbacks(EventCallbacks{
		OnMessageSent: func(messageID MessageID, channelID string) {
			acknowledged = append(acknowledged, msgEvent{MessageId: messageID, ChannelId: channelID})
		},
		OnMessageExpired: func(messageID MessageID, channelID string) {
			expired = append(expired, msgEvent{MessageId: messageID, ChannelId: channelID})
		},
	})

	rm.OnEvent(`{"eventType":"message_expired","messageId":"expired","channelId":"channel"}`)
	require.Empty(t, acknowledged)
	require.Equal(t, []msgEvent{{MessageId: "expired", ChannelId: "channel"}}, expired)

	rm.OnEvent(`{"eventType":"message_sent","messageId":"acknowledged","channelId":"channel"}`)
	require.Equal(t, []msgEvent{{MessageId: "acknowledged", ChannelId: "channel"}}, acknowledged)
	require.Len(t, expired, 1)
}

func TestExpiryWithoutHandlerDoesNotReportDelivery(t *testing.T) {
	rm := &ReliabilityManager{logger: zap.NewNop()}
	rm.RegisterCallbacks(EventCallbacks{
		OnMessageSent: func(MessageID, string) {
			t.Fatal("expiry must not report delivery")
		},
	})

	require.NotPanics(t, func() {
		rm.OnEvent(`{"eventType":"message_expired","messageId":"expired","channelId":"channel"}`)
	})
}

func TestMalformedExpiryEventLogsErrorWithoutCallback(t *testing.T) {
	core, logs := observer.New(zap.ErrorLevel)
	rm := &ReliabilityManager{logger: zap.New(core)}
	rm.RegisterCallbacks(EventCallbacks{
		OnMessageExpired: func(MessageID, string) {
			t.Fatal("malformed expiry must not invoke callback")
		},
		OnMessageSent: func(MessageID, string) {
			t.Fatal("malformed expiry must not report delivery")
		},
	})

	rm.OnEvent(`{"eventType":"message_expired","messageId":123,"channelId":"channel"}`)
	require.Equal(t, 1, logs.FilterMessage("failed to parse message expired event").Len())
}

func TestExpiryEventListenerRegistered(t *testing.T) {
	require.Contains(t, eventNames, "message_expired")
}
