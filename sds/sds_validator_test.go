//go:build !lint

package sds

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type validatorFunc func(ValidationMessage) (bool, error)

func (f validatorFunc) Validate(message ValidationMessage) (bool, error) {
	return f(message)
}

func TestValidatorDecisions(t *testing.T) {
	payload := []byte{0, 1, 255}
	channelID := "channel\x00id"
	sender, err := NewReliabilityManager(nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sender.Cleanup()) })
	wrapped, err := sender.WrapOutgoingMessage(payload, "message", channelID)
	require.NoError(t, err)

	for _, decision := range []struct {
		name     string
		accepted bool
		failure  error
		expected error
	}{
		{name: "accept", accepted: true},
		{name: "reject", expected: ErrMessageRejected},
		{name: "fail", accepted: true, failure: errors.New("permission lookup failed"), expected: ErrValidationFailed},
	} {
		t.Run(decision.name, func(t *testing.T) {
			core, logs := observer.New(zap.DebugLevel)
			persistence := &recordingPersistence{}
			receiver, err := NewReliabilityManager(zap.New(core), persistence)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, receiver.Cleanup()) })
			inputs := make(chan ValidationMessage, 1)
			require.NoError(t, receiver.SetValidator(validatorFunc(func(message ValidationMessage) (bool, error) {
				inputs <- message
				return decision.accepted, decision.failure
			})))

			context := []byte("authenticated\x00sender")
			received, err := receiver.UnwrapReceivedMessageWithContext(wrapped, context)
			require.Len(t, inputs, 1)
			message := <-inputs
			require.Equal(t, channelID, message.ChannelID)
			require.Equal(t, payload, message.Content)
			require.Empty(t, message.SenderID)
			require.Equal(t, context, message.ValidationContext)
			if decision.expected == nil {
				require.NoError(t, err)
				require.Equal(t, payload, *received.Message)
			} else {
				require.Nil(t, received)
				require.ErrorIs(t, err, decision.expected)
				require.NotErrorIs(t, err, ErrNotSDS)
				persistence.mu.Lock()
				require.Empty(t, persistence.calls)
				persistence.mu.Unlock()
				events, tickErr := receiver.Tick()
				require.NoError(t, tickErr)
				require.Empty(t, events)
			}
			if decision.failure != nil {
				entries := logs.FilterMessage("SDS message validation failed").All()
				require.Len(t, entries, 1)
				require.Equal(t, decision.failure.Error(), entries[0].ContextMap()["error"])
			}
		})
	}
}

func TestValidatorRejectsAcknowledgementsAndDependencies(t *testing.T) {
	persistence := &recordingPersistence{}
	receiver, err := NewReliabilityManager(nil, persistence)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, receiver.Cleanup()) })
	sender, err := NewReliabilityManager(nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sender.Cleanup()) })

	const channelID = "metadata-channel"
	pending, err := receiver.WrapOutgoingMessage([]byte("pending"), "pending-ack", channelID)
	require.NoError(t, err)
	_, err = sender.UnwrapReceivedMessage(pending)
	require.NoError(t, err)
	_, err = sender.WrapOutgoingMessage([]byte("fake"), "fake-dependency", channelID)
	require.NoError(t, err)
	wrapped, err := sender.WrapOutgoingMessage([]byte("unauthorized"), "unauthorized", channelID)
	require.NoError(t, err)

	sent := make(chan MessageID, 4)
	missing := make(chan []HistoryEntry, 4)
	receiver.RegisterCallbacks(EventCallbacks{
		OnMessageSent: func(id MessageID, _ string) { sent <- id },
		OnMissingDependencies: func(_ MessageID, deps []HistoryEntry, _ string) {
			missing <- deps
		},
	})
	require.NoError(t, receiver.SetValidator(validatorFunc(func(ValidationMessage) (bool, error) {
		return false, nil
	})))
	persistence.mu.Lock()
	before := len(persistence.calls)
	persistence.mu.Unlock()
	_, err = receiver.UnwrapReceivedMessage(wrapped)
	require.ErrorIs(t, err, ErrMessageRejected)
	require.Empty(t, sent)
	require.Empty(t, missing)
	persistence.mu.Lock()
	require.Len(t, persistence.calls, before)
	persistence.mu.Unlock()

	require.NoError(t, receiver.SetValidator(nil))
	_, err = receiver.UnwrapReceivedMessage(wrapped)
	require.NoError(t, err)
	select {
	case id := <-sent:
		require.Equal(t, MessageID("pending-ack"), id)
	case <-time.After(time.Second):
		t.Fatal("accepted acknowledgement did not emit message_sent")
	}
	select {
	case deps := <-missing:
		require.Equal(t, MessageID("fake-dependency"), deps[0].MessageID)
	case <-time.After(time.Second):
		t.Fatal("accepted causal history did not emit missing_dependencies")
	}
}

func TestValidatorContextIsolation(t *testing.T) {
	sender, err := NewReliabilityManager(nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sender.Cleanup()) })
	payload := []byte("retained-content")
	wrapped, err := sender.WrapOutgoingMessage(payload, "message", "channel")
	require.NoError(t, err)

	const requests = 8
	inputs := make(chan ValidationMessage, requests)
	receiver, err := NewReliabilityManager(nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, receiver.Cleanup()) })
	require.NoError(t, receiver.SetValidator(validatorFunc(func(message ValidationMessage) (bool, error) {
		inputs <- message
		return true, nil
	})))
	other, err := NewReliabilityManager(nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, other.Cleanup()) })
	require.NoError(t, other.SetValidator(validatorFunc(func(ValidationMessage) (bool, error) {
		return false, nil
	})))

	results := make(chan error, requests)
	var workers sync.WaitGroup
	for i := 0; i < requests; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			_, err := receiver.UnwrapReceivedMessageWithContext(wrapped, []byte(fmt.Sprintf("context-%d", i)))
			results <- err
		}(i)
	}
	workers.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	require.Len(t, inputs, requests)
	contexts := make(map[string]bool)
	for i := 0; i < requests; i++ {
		message := <-inputs
		require.Equal(t, payload, message.Content)
		contexts[string(message.ValidationContext)] = true
	}
	for i := 0; i < requests; i++ {
		require.True(t, contexts[fmt.Sprintf("context-%d", i)])
	}
	_, err = other.UnwrapReceivedMessageWithContext(wrapped, []byte("other-context"))
	require.ErrorIs(t, err, ErrMessageRejected)
	require.Empty(t, inputs)
}

func TestValidatorLifecycle(t *testing.T) {
	receiver, err := NewReliabilityManager(nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if _, ok := lookupReliabilityManager(receiver.rmCtx); ok {
			require.NoError(t, receiver.Cleanup())
		}
	})
	wrapped, err := receiver.WrapOutgoingMessage([]byte("content"), "duplicate", "channel")
	require.NoError(t, err)
	inputs := make(chan ValidationMessage, 2)
	require.NoError(t, receiver.SetValidator(validatorFunc(func(message ValidationMessage) (bool, error) {
		inputs <- message
		return false, nil
	})))
	_, err = receiver.UnwrapReceivedMessage(wrapped)
	require.ErrorIs(t, err, ErrMessageRejected)
	require.Len(t, inputs, 1)
	require.Empty(t, (<-inputs).ValidationContext)
	require.NoError(t, receiver.Reset())
	_, err = receiver.UnwrapReceivedMessage(wrapped)
	require.ErrorIs(t, err, ErrMessageRejected)
	require.Len(t, inputs, 1)
	<-inputs

	require.NoError(t, receiver.SetValidator(validatorFunc(func(ValidationMessage) (bool, error) {
		return true, nil
	})))
	_, err = receiver.UnwrapReceivedMessage(wrapped)
	require.NoError(t, err)
	require.NoError(t, receiver.SetValidator(nil))
	_, err = receiver.UnwrapReceivedMessage(wrapped)
	require.NoError(t, err)
	require.NoError(t, receiver.Cleanup())
	require.Nil(t, receiver.validator)
	require.Error(t, receiver.SetValidator(nil))

	fresh, err := NewReliabilityManager(nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, fresh.Cleanup()) })
	_, err = fresh.UnwrapReceivedMessage(wrapped)
	require.NoError(t, err)
}

func TestValidatorSyncAndMalformedInput(t *testing.T) {
	sender, err := newReliabilityManager(nil, nil, 2*time.Millisecond)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sender.Cleanup()) })
	require.NoError(t, sender.EnsureChannel("sync-channel"))
	sync := waitForTickPayload(t, sender, "sync-channel")
	persistence := &recordingPersistence{}
	receiver, err := NewReliabilityManager(nil, persistence)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, receiver.Cleanup()) })
	inputs := make(chan ValidationMessage, 1)
	require.NoError(t, receiver.SetValidator(validatorFunc(func(message ValidationMessage) (bool, error) {
		inputs <- message
		return false, nil
	})))

	_, err = receiver.UnwrapReceivedMessage([]byte("legacy message"))
	require.ErrorIs(t, err, ErrNotSDS)
	require.Empty(t, inputs)
	_, err = receiver.UnwrapReceivedMessageWithContext(sync.Payload, []byte("sync-sender"))
	require.ErrorIs(t, err, ErrMessageRejected)
	require.Len(t, inputs, 1)
	message := <-inputs
	require.Equal(t, "sync-channel", message.ChannelID)
	require.Empty(t, message.Content)
	require.Equal(t, []byte("sync-sender"), message.ValidationContext)
	persistence.mu.Lock()
	require.Empty(t, persistence.calls)
	persistence.mu.Unlock()

	require.NoError(t, receiver.SetValidator(nil))
	received, err := receiver.UnwrapReceivedMessage(sync.Payload)
	require.NoError(t, err)
	require.Nil(t, received.Message)
}
