package sds

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Test basic creation, cleanup, and reset
func TestLifecycle(t *testing.T) {
	rm, err := NewReliabilityManager()
	require.NoError(t, err)
	require.NotNil(t, rm, "Expected ReliabilityManager to be not nil")

	defer rm.Cleanup() // Ensure cleanup even on test failure

	err = rm.Reset()
	require.NoError(t, err)
}

// Test wrapping and unwrapping a simple message
func TestWrapUnwrap(t *testing.T) {
	rm, err := NewReliabilityManager()
	require.NoError(t, err)
	defer rm.Cleanup()

	channelID := "test-wrap-unwrap"
	originalPayload := []byte("hello reliability")
	messageID := MessageID("msg-wrap-1")

	wrappedMsg, err := rm.WrapOutgoingMessage(originalPayload, messageID, channelID)
	require.NoError(t, err)

	require.Greater(t, len(wrappedMsg), 0, "Expected non-empty wrapped message")

	// Simulate receiving the wrapped message
	unwrappedMessage, err := rm.UnwrapReceivedMessage(wrappedMsg)
	require.NoError(t, err)

	require.Equal(t, string(*unwrappedMessage.Message), string(originalPayload), "Expected unwrapped and original payloads to be equal")
	require.Equal(t, len(*unwrappedMessage.MissingDeps), 0, "Expexted to be no missing dependencies")
}

// Test dependency handling
func TestDependencies(t *testing.T) {
	rm, err := NewReliabilityManager()
	require.NoError(t, err)
	defer rm.Cleanup()

	channelID := "test-deps"

	// 1. Send message 1 (will become a dependency)
	payload1 := []byte("message one")
	msgID1 := MessageID("msg-dep-1")
	wrappedMsg1, err := rm.WrapOutgoingMessage(payload1, msgID1, channelID)
	require.NoError(t, err)

	// Simulate receiving msg1 to add it to history (implicitly acknowledges it)
	_, err = rm.UnwrapReceivedMessage(wrappedMsg1)
	require.NoError(t, err)

	// 2. Send message 2 (depends on message 1 implicitly via causal history)
	payload2 := []byte("message two")
	msgID2 := MessageID("msg-dep-2")
	wrappedMsg2, err := rm.WrapOutgoingMessage(payload2, msgID2, channelID)
	require.NoError(t, err)

	// 3. Create a new manager to simulate a different peer receiving msg2 without msg1
	rm2, err := NewReliabilityManager()
	require.NoError(t, err)
	defer rm2.Cleanup()

	// 4. Unwrap message 2 on the second manager - should report msg1 as missing
	unwrappedMessage2, err := rm2.UnwrapReceivedMessage(wrappedMsg2)
	require.NoError(t, err)

	require.Greater(t, len(*unwrappedMessage2.MissingDeps), 0, "Expected missing dependencies, got none")

	foundDep1 := false
	for _, dep := range *unwrappedMessage2.MissingDeps {
		if dep.MessageID == msgID1 {
			foundDep1 = true
			break
		}
	}
	require.True(t, foundDep1, "Expected missing dependency %q, got %v", msgID1, *unwrappedMessage2.MissingDeps)

	// 5. Mark the dependency as met
	err = rm2.MarkDependenciesMet([]MessageID{msgID1}, channelID)
	require.NoError(t, err)
}

// Test OnMessageReady callback
func TestCallback_OnMessageReady(t *testing.T) {
	// Create sender and receiver RMs
	senderRm, err := NewReliabilityManager()
	require.NoError(t, err)
	defer senderRm.Cleanup()

	receiverRm, err := NewReliabilityManager()
	require.NoError(t, err)
	defer receiverRm.Cleanup()

	channelID := "test-cb-ready"

	// Use a channel for signaling
	readyChan := make(chan MessageID, 1)

	callbacks := EventCallbacks{
		OnMessageReady: func(messageId MessageID, chId string) {
			require.Equal(t, channelID, chId)
			// Non-blocking send to channel
			select {
			case readyChan <- messageId:
			default:
				// Avoid blocking if channel is full or test already timed out
			}
		},
	}

	// Register callback only on the receiver
	receiverRm.RegisterCallbacks(callbacks)

	// Scenario: Wrap message on sender, unwrap on receiver
	payload := []byte("ready test")
	msgID := MessageID("cb-ready-1")

	// Wrap on sender
	wrappedMsg, err := senderRm.WrapOutgoingMessage(payload, msgID, channelID)
	require.NoError(t, err)

	// Unwrap on receiver
	_, err = receiverRm.UnwrapReceivedMessage(wrappedMsg)
	require.NoError(t, err)

	// Verification - Wait on channel with timeout
	select {
	case receivedMsgID := <-readyChan:
		// Mark as called implicitly since we received on channel
		if receivedMsgID != msgID {
			t.Errorf("OnMessageReady called with wrong ID: got %q, want %q", receivedMsgID, msgID)
		}
	case <-time.After(2 * time.Second):
		// If timeout occurs, the channel receive failed.
		t.Errorf("Timed out waiting for OnMessageReady callback on readyChan")
	}
}

// Test OnMessageSent callback (via causal history ACK)
func TestCallback_OnMessageSent(t *testing.T) {
	// Create two RMs
	rm1, err := NewReliabilityManager()
	require.NoError(t, err)
	defer rm1.Cleanup()

	rm2, err := NewReliabilityManager()
	require.NoError(t, err)
	defer rm2.Cleanup()

	channelID := "test-cb-sent"

	var wg sync.WaitGroup
	sentCalled := false
	var sentMsgID MessageID
	var cbMutex sync.Mutex

	callbacks := EventCallbacks{
		OnMessageSent: func(messageId MessageID, chId string) {
			require.Equal(t, channelID, chId)
			cbMutex.Lock()
			sentCalled = true
			sentMsgID = messageId
			cbMutex.Unlock()
			wg.Done()
		},
	}

	// Register callback on rm1 (the original sender)
	rm1.RegisterCallbacks(callbacks)

	// Scenario: rm1 sends msg1, rm2 receives msg1,
	// rm2 sends msg2 (acking msg1), rm1 receives msg2.

	// 1. rm1 sends msg1
	payload1 := []byte("sent test 1")
	msgID1 := MessageID("cb-sent-1")
	wrappedMsg1, err := rm1.WrapOutgoingMessage(payload1, msgID1, channelID)
	require.NoError(t, err)
	// Note: msg1 is now in rm1's outgoing buffer

	// 2. rm2 receives msg1 (to update its state)
	_, err = rm2.UnwrapReceivedMessage(wrappedMsg1)
	require.NoError(t, err)

	// 3. rm2 sends msg2 (will include msg1 in causal history)
	payload2 := []byte("sent test 2")
	msgID2 := MessageID("cb-sent-2")
	wrappedMsg2, err := rm2.WrapOutgoingMessage(payload2, msgID2, channelID)
	require.NoError(t, err)

	// 4. rm1 receives msg2 (should trigger ACK for msg1)
	wg.Add(1) // Expect OnMessageSent for msg1 on rm1
	_, err = rm1.UnwrapReceivedMessage(wrappedMsg2)
	require.NoError(t, err)

	// Verification
	waitTimeout(&wg, 2*time.Second, t)

	cbMutex.Lock()
	defer cbMutex.Unlock()
	if !sentCalled {
		t.Errorf("OnMessageSent was not called")
	}
	// We primarily care that msg1 was ACKed.
	if sentMsgID != msgID1 {
		t.Errorf("OnMessageSent called with wrong ID: got %q, want %q", sentMsgID, msgID1)
	}
}

// Test OnMissingDependencies callback
func TestCallback_OnMissingDependencies(t *testing.T) {
	// Use separate sender/receiver RMs explicitly
	senderRm, err := NewReliabilityManager()
	require.NoError(t, err)
	defer senderRm.Cleanup()

	receiverRm, err := NewReliabilityManager()
	require.NoError(t, err)
	defer receiverRm.Cleanup()

	channelID := "test-cb-missing"

	var wg sync.WaitGroup
	missingCalled := false
	var missingMsgID MessageID
	var missingDepsList []MessageID
	var cbMutex sync.Mutex

	callbacks := EventCallbacks{
		OnMissingDependencies: func(messageId MessageID, missingDeps []HistoryEntry, chId string) {
			require.Equal(t, channelID, chId)
			cbMutex.Lock()
			missingCalled = true
			missingMsgID = messageId
			missingDepsList = make([]MessageID, len(missingDeps))
			for i, dep := range missingDeps {
				missingDepsList[i] = dep.MessageID
			}
			cbMutex.Unlock()
			wg.Done()
		},
	}

	// Register callback only on the receiver rm
	receiverRm.RegisterCallbacks(callbacks)

	// Scenario: Sender sends msg1, then sender sends msg2 (depends on msg1),
	// then receiver receives msg2 (which hasn't seen msg1).

	// 1. Sender sends msg1
	payload1 := []byte("missing test 1")
	msgID1 := MessageID("cb-miss-1")
	_, err = senderRm.WrapOutgoingMessage(payload1, msgID1, channelID)
	require.NoError(t, err)

	// 2. Sender sends msg2 (depends on msg1)
	payload2 := []byte("missing test 2")
	msgID2 := MessageID("cb-miss-2")
	wrappedMsg2, err := senderRm.WrapOutgoingMessage(payload2, msgID2, channelID)
	require.NoError(t, err)

	// 3. Receiver receives msg2 (haven't seen msg1)
	wg.Add(1) // Expect OnMissingDependencies
	_, err = receiverRm.UnwrapReceivedMessage(wrappedMsg2)
	require.NoError(t, err)

	// Verification
	waitTimeout(&wg, 2*time.Second, t)

	cbMutex.Lock()
	defer cbMutex.Unlock()
	if !missingCalled {
		t.Errorf("OnMissingDependencies was not called")
	}
	if missingMsgID != msgID2 {
		t.Errorf("OnMissingDependencies called for wrong ID: got %q, want %q", missingMsgID, msgID2)
	}
	foundDep := false
	for _, dep := range missingDepsList {
		if dep == msgID1 {
			foundDep = true
			break
		}
	}
	if !foundDep {
		t.Errorf("OnMissingDependencies did not report %q as missing, got: %v", msgID1, missingDepsList)
	}
}

// Test OnPeriodicSync callback
func TestCallback_OnPeriodicSync(t *testing.T) {
	rm, err := NewReliabilityManager()
	require.NoError(t, err)
	defer rm.Cleanup()

	syncCalled := false
	var cbMutex sync.Mutex
	// Use a channel to signal when the callback is hit
	syncChan := make(chan bool, 1)

	callbacks := EventCallbacks{
		OnPeriodicSync: func() {
			cbMutex.Lock()
			if !syncCalled { // Only signal the first time
				syncCalled = true
				syncChan <- true
			}
			cbMutex.Unlock()
		},
	}

	rm.RegisterCallbacks(callbacks)

	// Start periodic tasks
	err = rm.StartPeriodicTasks()
	require.NoError(t, err)

	// --- Verification ---
	// Wait for the periodic sync callback with a timeout (needs to be longer than sync interval)
	select {
	case <-syncChan:
		// Success
	case <-time.After(10 * time.Second):
		t.Errorf("Timed out waiting for OnPeriodicSync callback")
	}

	cbMutex.Lock()
	defer cbMutex.Unlock()
	if !syncCalled {
		// This might happen if the timeout was too short
		t.Logf("Warning: OnPeriodicSync might not have been called within the test timeout")
	}
}

// Combined Test for multiple callbacks
func TestCallbacks_Combined(t *testing.T) {
	// Create sender and receiver RMs
	senderRm, err := NewReliabilityManager()
	require.NoError(t, err)
	defer senderRm.Cleanup()

	receiverRm, err := NewReliabilityManager()
	require.NoError(t, err)
	defer receiverRm.Cleanup()

	channelID := "test-cb-combined"

	// Channels for synchronization
	readyChan1 := make(chan bool, 1)
	sentChan1 := make(chan bool, 1)
	missingChan := make(chan []MessageID, 1)

	// Use maps for verification
	receivedReady := make(map[MessageID]bool)
	receivedSent := make(map[MessageID]bool)
	var cbMutex sync.Mutex

	callbacksReceiver := EventCallbacks{
		OnMessageReady: func(messageId MessageID, chId string) {
			require.Equal(t, channelID, chId)
			cbMutex.Lock()
			receivedReady[messageId] = true
			cbMutex.Unlock()
			if messageId == "cb-comb-1" {
				// Use non-blocking send
				select {
				case readyChan1 <- true:
				default:
				}
			}
		},
		OnMessageSent: func(messageId MessageID, chId string) {
			// This callback is registered on Receiver, but Sent events
			// are typically relevant to the Sender. We don't expect this.
			t.Errorf("Unexpected OnMessageSent call on Receiver for %s", messageId)
		},
		OnMissingDependencies: func(messageId MessageID, missingDeps []HistoryEntry, chId string) {
			// This callback is registered on Receiver, used for receiverRm2 below
		},
	}

	callbacksSender := EventCallbacks{
		OnMessageReady: func(messageId MessageID, chId string) {
			// Not expected on sender in this test flow
		},
		OnMessageSent: func(messageId MessageID, chId string) {
			require.Equal(t, channelID, chId)
			cbMutex.Lock()
			receivedSent[messageId] = true
			cbMutex.Unlock()
			if messageId == "cb-comb-1" {
				select {
				case sentChan1 <- true:
				default:
				}
			}
		},
		OnMissingDependencies: func(messageId MessageID, missingDeps []HistoryEntry, chId string) {
			// Not expected on sender
		},
	}

	// Register callbacks
	receiverRm.RegisterCallbacks(callbacksReceiver)
	senderRm.RegisterCallbacks(callbacksSender)

	// --- Test Scenario ---
	msgID1 := MessageID("cb-comb-1")
	msgID2 := MessageID("cb-comb-2")
	msgID3 := MessageID("cb-comb-3")
	payload1 := []byte("combined test 1")
	payload2 := []byte("combined test 2")
	payload3 := []byte("combined test 3")

	// 1. Sender sends msg1
	wrappedMsg1, err := senderRm.WrapOutgoingMessage(payload1, msgID1, channelID)
	require.NoError(t, err)

	// 2. Receiver receives msg1
	_, err = receiverRm.UnwrapReceivedMessage(wrappedMsg1)
	require.NoError(t, err)

	// 3. Receiver sends msg2 (depends on msg1 implicitly via state)
	wrappedMsg2, err := receiverRm.WrapOutgoingMessage(payload2, msgID2, channelID)
	require.NoError(t, err)

	// 4. Sender receives msg2 from Receiver (acks msg1 for sender)
	_, err = senderRm.UnwrapReceivedMessage(wrappedMsg2)
	require.NoError(t, err)

	// 5. Sender sends msg3 (depends on msg2)
	wrappedMsg3, err := senderRm.WrapOutgoingMessage(payload3, msgID3, channelID)
	require.NoError(t, err)

	// 6. Create Receiver2, register missing deps callback
	receiverRm2, err := NewReliabilityManager()
	require.NoError(t, err)
	defer receiverRm2.Cleanup()

	callbacksReceiver2 := EventCallbacks{
		OnMissingDependencies: func(messageId MessageID, missingDeps []HistoryEntry, chId string) {
			require.Equal(t, channelID, chId)
			if messageId == msgID3 {
				// Convert []HistoryEntry to []MessageID for the channel
				deps := make([]MessageID, len(missingDeps))
				for i, d := range missingDeps {
					deps[i] = d.MessageID
				}
				select {
				case missingChan <- deps:
				default:
				}
			}
		},
	}

	receiverRm2.RegisterCallbacks(callbacksReceiver2)

	// 7. Receiver2 receives msg3 (should report missing msg1, msg2)
	_, err = receiverRm2.UnwrapReceivedMessage(wrappedMsg3)
	require.NoError(t, err)

	// --- Verification ---
	timeout := 5 * time.Second
	expectedReady1 := false
	expectedSent1 := false
	var reportedMissingDeps []MessageID
	missingDepsReceived := false

	receivedCount := 0
	expectedCount := 3 // ready1, sent1, missingDeps
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for receivedCount < expectedCount {
		select {
		case <-readyChan1:
			if !expectedReady1 { // Avoid double counting if signaled twice
				expectedReady1 = true
				receivedCount++
			}
		case <-sentChan1:
			if !expectedSent1 {
				expectedSent1 = true
				receivedCount++
			}
		case deps := <-missingChan:
			if !missingDepsReceived {
				reportedMissingDeps = deps
				missingDepsReceived = true
				receivedCount++
			}
		case <-timer.C:
			t.Fatalf("Timed out waiting for combined callbacks (received %d out of %d)", receivedCount, expectedCount)
		}
	}

	// Check results
	cbMutex.Lock()
	defer cbMutex.Unlock()

	if !expectedReady1 || !receivedReady[msgID1] {
		t.Errorf("OnMessageReady not called/verified for %s", msgID1)
	}
	if !expectedSent1 || !receivedSent[msgID1] {
		t.Errorf("OnMessageSent not called/verified for %s", msgID1)
	}
	if !missingDepsReceived {
		t.Errorf("OnMissingDependencies not called/verified for %s", msgID3)
	} else {
		foundDep1 := false
		foundDep2 := false
		for _, dep := range reportedMissingDeps {
			if dep == msgID1 {
				foundDep1 = true
			}
			if dep == msgID2 {
				foundDep2 = true
			}
		}
		if !foundDep1 || !foundDep2 {
			t.Errorf("OnMissingDependencies for %s reported wrong deps: got %v, want %s and %s", msgID3, reportedMissingDeps, msgID1, msgID2)
		}
	}
}

// Helper function to wait for WaitGroup with a timeout
func waitTimeout(wg *sync.WaitGroup, timeout time.Duration, t *testing.T) {
	c := make(chan struct{})
	go func() {
		defer close(c)
		wg.Wait()
	}()
	select {
	case <-c:
		// Completed normally
	case <-time.After(timeout):
		t.Fatalf("Timed out waiting for callbacks")
	}
}

// Test multi-channel functionality - one RM can handle messages from different channels
func TestMultiChannel_SingleRM(t *testing.T) {
	rm, err := NewReliabilityManager()
	require.NoError(t, err)
	defer rm.Cleanup()

	// Test with two different channels
	channel1 := "test-channel-1"
	channel2 := "test-channel-2"

	// Create and wrap messages for different channels
	msg1 := []byte("message for channel 1")
	msgID1 := MessageID("msg1")
	wrappedMsg1, err := rm.WrapOutgoingMessage(msg1, msgID1, channel1)
	require.NoError(t, err)

	msg2 := []byte("message for channel 2")
	msgID2 := MessageID("msg2")
	wrappedMsg2, err := rm.WrapOutgoingMessage(msg2, msgID2, channel2)
	require.NoError(t, err)

	// Unwrap messages - should extract channel ID and route correctly
	unwrapped1, err := rm.UnwrapReceivedMessage(wrappedMsg1)
	require.NoError(t, err)
	require.Equal(t, msg1, *unwrapped1.Message)

	// Verify channel ID is extracted correctly
	require.NotNil(t, unwrapped1.ChannelId, "Expected ChannelId to be not nil")
	require.Equal(t, channel1, *unwrapped1.ChannelId)

	unwrapped2, err := rm.UnwrapReceivedMessage(wrappedMsg2)
	require.NoError(t, err)
	require.Equal(t, msg2, *unwrapped2.Message)

	// Verify channel ID is extracted correctly
	require.NotNil(t, unwrapped2.ChannelId, "Expected ChannelId to be not nil")
	require.Equal(t, channel2, *unwrapped2.ChannelId)

	// Test that the same RM can handle dependencies for different channels
	err = rm.MarkDependenciesMet([]MessageID{msgID1}, channel1)
	require.NoError(t, err)

	err = rm.MarkDependenciesMet([]MessageID{msgID2}, channel2)
	require.NoError(t, err)
}

// Test that callbacks are correctly triggered for multiple channels
func TestMultiChannelCallbacks(t *testing.T) {
	// rm1 is the manager we are testing callbacks on
	rm1, err := NewReliabilityManager()
	require.NoError(t, err)
	defer rm1.Cleanup()

	// rm2 simulates another peer
	rm2, err := NewReliabilityManager()
	require.NoError(t, err)
	defer rm2.Cleanup()

	channel1 := "callback-channel-1"
	channel2 := "callback-channel-2"

	var wg sync.WaitGroup
	var cbMutex sync.Mutex
	readyMessages := make(map[MessageID]string)
	sentMessages := make(map[MessageID]string)

	callbacks := EventCallbacks{
		OnMessageReady: func(messageId MessageID, channelId string) {
			// This will be triggered when rm1 receives messages from rm2
			cbMutex.Lock()
			readyMessages[messageId] = channelId
			cbMutex.Unlock()
			wg.Done()
		},
		OnMessageSent: func(messageId MessageID, channelId string) {
			// This will be triggered when rm1's messages are acked by rm2
			cbMutex.Lock()
			sentMessages[messageId] = channelId
			cbMutex.Unlock()
			wg.Done()
		},
	}
	rm1.RegisterCallbacks(callbacks)

	// --- Test Scenario ---
	// 1. rm1 sends one message on each channel.
	msgID1_ch1 := MessageID("msg-on-ch1")
	wrappedMsg1_ch1, err := rm1.WrapOutgoingMessage([]byte("payload1"), msgID1_ch1, channel1)
	require.NoError(t, err)

	msgID2_ch2 := MessageID("msg-on-ch2")
	wrappedMsg2_ch2, err := rm1.WrapOutgoingMessage([]byte("payload2"), msgID2_ch2, channel2)
	require.NoError(t, err)

	// 2. rm2 receives these messages to update its history.
	_, err = rm2.UnwrapReceivedMessage(wrappedMsg1_ch1)
	require.NoError(t, err)
	_, err = rm2.UnwrapReceivedMessage(wrappedMsg2_ch2)
	require.NoError(t, err)

	// 3. rm2 sends messages back on each channel, which will act as ACKs for rm1's messages.
	ackID1_ch1 := MessageID("ack-on-ch1")
	wrappedAck1_ch1, err := rm2.WrapOutgoingMessage([]byte("ack_payload1"), ackID1_ch1, channel1)
	require.NoError(t, err)

	ackID2_ch2 := MessageID("ack-on-ch2")
	wrappedAck2_ch2, err := rm2.WrapOutgoingMessage([]byte("ack_payload2"), ackID2_ch2, channel2)
	require.NoError(t, err)

	// 4. rm1 receives the "ack" messages. This should trigger:
	//    - OnMessageSent for msgID1_ch1 and msgID2_ch2
	//    - OnMessageReady for ackID1_ch1 and ackID2_ch2
	wg.Add(4) // Expect 2 sent and 2 ready callbacks
	_, err = rm1.UnwrapReceivedMessage(wrappedAck1_ch1)
	require.NoError(t, err)
	_, err = rm1.UnwrapReceivedMessage(wrappedAck2_ch2)
	require.NoError(t, err)

	// --- Verification ---
	waitTimeout(&wg, 5*time.Second, t)

	cbMutex.Lock()
	defer cbMutex.Unlock()

	// Check that both original messages were marked as sent and on the correct channel
	require.Equal(t, channel1, sentMessages[msgID1_ch1], "OnMessageSent for msg1 has incorrect channel")
	require.Equal(t, channel2, sentMessages[msgID2_ch2], "OnMessageSent for msg2 has incorrect channel")
	require.Len(t, sentMessages, 2, "Expected exactly 2 sent messages")

	// Check that both ack messages were marked as ready and on the correct channel
	require.Equal(t, channel1, readyMessages[ackID1_ch1], "OnMessageReady for ack1 has incorrect channel")
	require.Equal(t, channel2, readyMessages[ackID2_ch2], "OnMessageReady for ack2 has incorrect channel")
	require.Len(t, readyMessages, 2, "Expected exactly 2 ready messages")
}

func TestRetrievalHints(t *testing.T) {
	rm, err := NewReliabilityManager()
	require.NoError(t, err)
	defer rm.Cleanup()

	channelID := "test-retrieval-hints"

	// Set a retrieval hint provider
	rm.RegisterCallbacks(EventCallbacks{
		RetrievalHintProvider: func(messageId MessageID) []byte {
			return []byte("hint-for-" + messageId)
		},
	})

	// 1. Send a message to populate the history
	payload1 := []byte("message one")
	msgID1 := MessageID("msg-hint-1")
	wrappedMsg1, err := rm.WrapOutgoingMessage(payload1, msgID1, channelID)
	require.NoError(t, err)

	// 2. Receive the message to add it to history
	_, err = rm.UnwrapReceivedMessage(wrappedMsg1)
	require.NoError(t, err)

	// 3. Send a second message, which will include the first in its causal history
	payload2 := []byte("message two")
	msgID2 := MessageID("msg-hint-2")
	wrappedMsg2, err := rm.WrapOutgoingMessage(payload2, msgID2, channelID)
	require.NoError(t, err)

	// 4. Unwrap the second message to inspect its causal history
	// We need a new RM to avoid acknowledging the message
	rm2, err := NewReliabilityManager()
	require.NoError(t, err)
	defer rm2.Cleanup()

	rm2.RegisterCallbacks(EventCallbacks{
		RetrievalHintProvider: func(messageId MessageID) []byte {
			return []byte("hint-for-" + messageId)
		},
	})

	unwrappedMsg2, err := rm2.UnwrapReceivedMessage(wrappedMsg2)
	require.NoError(t, err)

	// 5. Check that the causal history contains the retrieval hint
	require.Greater(t, len(*unwrappedMsg2.MissingDeps), 0, "Expected missing dependencies")
	foundDep := false
	for _, dep := range *unwrappedMsg2.MissingDeps {
		if dep.MessageID == msgID1 {
			foundDep = true
			require.Equal(t, []byte("hint-for-"+msgID1), dep.RetrievalHint, "Retrieval hint does not match")
			break
		}
	}
	require.True(t, foundDep, "Expected to find dependency %s", msgID1)
}
