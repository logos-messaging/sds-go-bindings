//go:build !lint

package sds

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type authorizerFunc func(AuthorizationMessage) (bool, error)

func (f authorizerFunc) Authorize(message AuthorizationMessage) (bool, error) {
	return f(message)
}

func TestAuthorizerDecisions(t *testing.T) {
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
		{name: "fail", accepted: true, failure: errors.New("permission lookup failed"), expected: ErrAuthorizationFailed},
	} {
		t.Run(decision.name, func(t *testing.T) {
			core, logs := observer.New(zap.DebugLevel)
			receiver, err := NewReliabilityManager(zap.New(core), nil)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, receiver.Cleanup()) })
			inputs := make(chan AuthorizationMessage, 1)
			require.NoError(t, receiver.SetAuthorizer(authorizerFunc(func(message AuthorizationMessage) (bool, error) {
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
			require.Equal(t, context, message.AuthorizationContext)
			if decision.expected == nil {
				require.NoError(t, err)
				require.Equal(t, payload, *received.Message)
			} else {
				require.Nil(t, received)
				require.ErrorIs(t, err, decision.expected)
				require.NotErrorIs(t, err, ErrNotSDS)
			}
			if decision.failure != nil {
				entries := logs.FilterMessage("SDS message authorization failed").All()
				require.Len(t, entries, 1)
				require.Equal(t, decision.failure.Error(), entries[0].ContextMap()["error"])
			}
		})
	}
}

func TestAuthorizerContextIsolation(t *testing.T) {
	sender, err := NewReliabilityManager(nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sender.Cleanup()) })

	const requests = 8
	inputs := make(chan AuthorizationMessage, requests)
	receiver, err := NewReliabilityManager(nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, receiver.Cleanup()) })
	require.NoError(t, receiver.SetAuthorizer(authorizerFunc(func(message AuthorizationMessage) (bool, error) {
		inputs <- message
		return true, nil
	})))
	other, err := NewReliabilityManager(nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, other.Cleanup()) })
	require.NoError(t, other.SetAuthorizer(authorizerFunc(func(AuthorizationMessage) (bool, error) {
		return false, nil
	})))

	type request struct {
		wrapped []byte
		payload []byte
		context []byte
	}
	requestsToSend := make([]request, requests)
	expectedByChannel := make(map[string]request, requests)
	for i := range requests {
		channel := fmt.Sprintf("channel-%d", i)
		payload := []byte(fmt.Sprintf("payload-%d", i))
		context := []byte(fmt.Sprintf("context-%d", i))
		wrapped, err := sender.WrapOutgoingMessage(payload, MessageID(fmt.Sprintf("message-%d", i)), channel)
		require.NoError(t, err)
		req := request{wrapped: wrapped, payload: payload, context: context}
		requestsToSend[i] = req
		expectedByChannel[channel] = req
	}

	results := make(chan error, requests)
	var workers sync.WaitGroup
	for i := 0; i < requests; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			req := requestsToSend[i]
			_, err := receiver.UnwrapReceivedMessageWithContext(req.wrapped, req.context)
			results <- err
		}(i)
	}
	workers.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	require.Len(t, inputs, requests)
	receivedByChannel := make(map[string]AuthorizationMessage, requests)
	for i := 0; i < requests; i++ {
		message := <-inputs
		receivedByChannel[message.ChannelID] = message
	}
	for channel, req := range expectedByChannel {
		message, ok := receivedByChannel[channel]
		require.True(t, ok, "authorizer did not receive request for %s", channel)
		require.Equal(t, req.payload, message.Content)
		require.Equal(t, req.context, message.AuthorizationContext)
	}
	_, err = other.UnwrapReceivedMessageWithContext(requestsToSend[0].wrapped, []byte("other-context"))
	require.ErrorIs(t, err, ErrMessageRejected)
	require.Empty(t, inputs)
}

func TestAuthorizerLifecycle(t *testing.T) {
	receiver, err := NewReliabilityManager(nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if _, ok := lookupReliabilityManager(receiver.rmCtx); ok {
			require.NoError(t, receiver.Cleanup())
		}
	})
	sender, err := NewReliabilityManager(nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sender.Cleanup()) })
	rejected, err := sender.WrapOutgoingMessage([]byte("rejected"), "rejected", "reject-channel")
	require.NoError(t, err)
	accepted, err := sender.WrapOutgoingMessage([]byte("accepted"), "accepted", "accept-channel")
	require.NoError(t, err)
	inputs := make(chan AuthorizationMessage, 1)
	require.NoError(t, receiver.SetAuthorizer(authorizerFunc(func(message AuthorizationMessage) (bool, error) {
		inputs <- message
		return false, nil
	})))
	_, err = receiver.UnwrapReceivedMessage(rejected)
	require.ErrorIs(t, err, ErrMessageRejected)
	require.Len(t, inputs, 1)
	require.Empty(t, (<-inputs).AuthorizationContext)
	require.NoError(t, receiver.SetAuthorizer(nil))
	received, err := receiver.UnwrapReceivedMessage(accepted)
	require.NoError(t, err)
	require.Equal(t, []byte("accepted"), *received.Message)
	require.Empty(t, inputs)
	require.NoError(t, receiver.Cleanup())
	require.Nil(t, receiver.authorizer)
	require.Error(t, receiver.SetAuthorizer(nil))

	fresh, err := NewReliabilityManager(nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, fresh.Cleanup()) })
	_, err = fresh.UnwrapReceivedMessage(accepted)
	require.NoError(t, err)
}

func TestAuthorizerRegistrationBeforeCrossThreadUnwrap(t *testing.T) {
	sender, err := NewReliabilityManager(nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sender.Cleanup()) })
	wrapped, err := sender.WrapOutgoingMessage([]byte("content"), "message", "channel")
	require.NoError(t, err)

	receiver, err := NewReliabilityManager(nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, receiver.Cleanup()) })
	called := make(chan AuthorizationMessage, 1)
	setterDone := make(chan error, 1)
	releaseSetter := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		setterDone <- receiver.SetAuthorizer(authorizerFunc(func(message AuthorizationMessage) (bool, error) {
			called <- message
			return false, nil
		}))
		<-releaseSetter
	}()
	defer close(releaseSetter)
	require.NoError(t, <-setterDone)

	unwrapDone := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		_, err := receiver.UnwrapReceivedMessage(wrapped)
		unwrapDone <- err
	}()
	require.ErrorIs(t, <-unwrapDone, ErrMessageRejected)
	require.Equal(t, "channel", (<-called).ChannelID)
}

func TestAuthorizerDoesNotInterceptNonSDS(t *testing.T) {
	receiver, err := NewReliabilityManager(nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, receiver.Cleanup()) })
	var calls int
	require.NoError(t, receiver.SetAuthorizer(authorizerFunc(func(message AuthorizationMessage) (bool, error) {
		calls++
		return true, nil
	})))

	_, err = receiver.UnwrapReceivedMessage([]byte("legacy message"))
	require.ErrorIs(t, err, ErrNotSDS)
	require.Zero(t, calls)
}
