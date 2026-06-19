package sds

import (
	"sync"
	"time"
	"unsafe"

	"github.com/fxamacker/cbor/v2"
	"go.uber.org/zap"
)

const requestTimeout = 30 * time.Second
const EventChanBufferSize = 1024

// sdsEventNames are the events the library emits; the bindings register one
// listener per name via sds_add_event_listener.
var sdsEventNames = []string{
	"message_ready",
	"message_sent",
	"missing_dependencies",
	"periodic_sync",
	"repair_ready",
}

type EventCallbacks struct {
	OnMessageReady        func(messageId MessageID, channelId string)
	OnMessageSent         func(messageId MessageID, channelId string)
	OnMissingDependencies func(messageId MessageID, missingDeps []HistoryEntry, channelId string)
	OnPeriodicSync        func()
	OnRepairReady         func(message []byte, channelId string)
	RetrievalHintProvider func(messageId MessageID) []byte
}

// ReliabilityManager represents an instance of a nim-sds ReliabilityManager
type ReliabilityManager struct {
	logger    *zap.Logger
	rmCtx     unsafe.Pointer
	callbacks EventCallbacks
}

// The event callback sends back the rm ctx to know to which
// rm is the event being emited for. Since we only have a global
// callback in the go side, We register all the rm's that we create
// so we can later obtain which instance of `ReliabilityManager` it should
// be invoked depending on the ctx received
var rmRegistry map[unsafe.Pointer]*ReliabilityManager

// rmRegistryMu guards rmRegistry. libsds invokes the global callbacks on its
// own worker/event threads, so reads from those callbacks race the register/
// unregister writes done on the goroutines that create and clean up managers.
var rmRegistryMu sync.RWMutex

func init() {
	rmRegistry = make(map[unsafe.Pointer]*ReliabilityManager)
}

// lookupReliabilityManager resolves the manager for a ctx under the read lock.
// Used by the global callbacks, which run on libsds threads.
func lookupReliabilityManager(ctx unsafe.Pointer) (*ReliabilityManager, bool) {
	rmRegistryMu.RLock()
	defer rmRegistryMu.RUnlock()
	rm, ok := rmRegistry[ctx]
	return rm, ok
}

func registerReliabilityManager(rm *ReliabilityManager) {
	rmRegistryMu.Lock()
	defer rmRegistryMu.Unlock()
	_, ok := rmRegistry[rm.rmCtx]
	if !ok {
		rmRegistry[rm.rmCtx] = rm
	}
}

func unregisterReliabilityManager(rm *ReliabilityManager) {
	rmRegistryMu.Lock()
	defer rmRegistryMu.Unlock()
	delete(rmRegistry, rm.rmCtx)
}

// eventEnvelope mirrors nim-ffi's CBOR wire shape:
// { "eventType": <name>, "payload": <event object> }. The payload is decoded
// lazily once the event type is known.
type eventEnvelope struct {
	EventType string          `cbor:"eventType"`
	Payload   cbor.RawMessage `cbor:"payload"`
}

// sdsMissingDep is the CBOR shape of one missing dependency, shared by the
// unwrap response and the missing_dependencies event.
type sdsMissingDep struct {
	MessageId     string `cbor:"messageId"`
	RetrievalHint []byte `cbor:"retrievalHint"`
}

type msgEvent struct {
	MessageId MessageID `cbor:"messageId"`
	ChannelId string    `cbor:"channelId"`
}

type missingDepsEvent struct {
	MessageId   MessageID       `cbor:"messageId"`
	MissingDeps []sdsMissingDep `cbor:"missingDeps"`
	ChannelId   string          `cbor:"channelId"`
}

type repairReadyEvent struct {
	Message   []byte `cbor:"message"`
	ChannelId string `cbor:"channelId"`
}

func (rm *ReliabilityManager) RegisterCallbacks(callbacks EventCallbacks) {
	rm.callbacks = callbacks
}

func (rm *ReliabilityManager) OnEvent(data []byte) {
	envelope := eventEnvelope{}
	if err := cbor.Unmarshal(data, &envelope); err != nil {
		rm.logger.Error("failed to unmarshal sds event", zap.Error(err))
		return
	}

	switch envelope.EventType {
	case "message_ready":
		rm.parseMessageReadyEvent(envelope.Payload)
	case "message_sent":
		rm.parseMessageSentEvent(envelope.Payload)
	case "missing_dependencies":
		rm.parseMissingDepsEvent(envelope.Payload)
	case "periodic_sync":
		if rm.callbacks.OnPeriodicSync != nil {
			rm.callbacks.OnPeriodicSync()
		}
	case "repair_ready":
		rm.parseRepairReadyEvent(envelope.Payload)
	}
}

func (rm *ReliabilityManager) OnCallbackError(callerRet int, err string) {
	rm.logger.Error("sds callback error",
		zap.Int("retCode", callerRet),
		zap.String("errMsg", err))
}

func (rm *ReliabilityManager) parseMessageReadyEvent(payload []byte) {
	msgEvent := msgEvent{}
	if err := cbor.Unmarshal(payload, &msgEvent); err != nil {
		rm.logger.Error("failed to parse message ready event", zap.Error(err))
		return
	}

	if rm.callbacks.OnMessageReady != nil {
		rm.callbacks.OnMessageReady(msgEvent.MessageId, msgEvent.ChannelId)
	}
}

func (rm *ReliabilityManager) parseMessageSentEvent(payload []byte) {
	msgEvent := msgEvent{}
	if err := cbor.Unmarshal(payload, &msgEvent); err != nil {
		rm.logger.Error("failed to parse message sent event", zap.Error(err))
		return
	}

	if rm.callbacks.OnMessageSent != nil {
		rm.callbacks.OnMessageSent(msgEvent.MessageId, msgEvent.ChannelId)
	}
}

func (rm *ReliabilityManager) parseMissingDepsEvent(payload []byte) {
	missingDepsEvent := missingDepsEvent{}
	if err := cbor.Unmarshal(payload, &missingDepsEvent); err != nil {
		rm.logger.Error("failed to parse missing dependencies event", zap.Error(err))
		return
	}

	if rm.callbacks.OnMissingDependencies != nil {
		deps := make([]HistoryEntry, len(missingDepsEvent.MissingDeps))
		for i, d := range missingDepsEvent.MissingDeps {
			deps[i] = HistoryEntry{MessageID: MessageID(d.MessageId), RetrievalHint: d.RetrievalHint}
		}
		rm.callbacks.OnMissingDependencies(missingDepsEvent.MessageId, deps, missingDepsEvent.ChannelId)
	}
}

func (rm *ReliabilityManager) parseRepairReadyEvent(payload []byte) {
	repairReadyEvent := repairReadyEvent{}
	if err := cbor.Unmarshal(payload, &repairReadyEvent); err != nil {
		rm.logger.Error("failed to parse repair ready event", zap.Error(err))
		return
	}

	if rm.callbacks.OnRepairReady != nil {
		rm.callbacks.OnRepairReady(repairReadyEvent.Message, repairReadyEvent.ChannelId)
	}
}
