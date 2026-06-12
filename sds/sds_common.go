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

type EventCallbacks struct {
	OnMessageReady        func(messageId MessageID, channelId string)
	OnMessageSent         func(messageId MessageID, channelId string)
	OnMissingDependencies func(messageId MessageID, missingDeps []MessageID, channelId string)
	OnPeriodicSync        func()
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
// be invoked depending on the ctx received.
//
// rmRegistryMu guards the map: it is written by register/unregister (on
// Create/Cleanup) and read by sdsGlobalEventCallback on the nim-ffi event
// thread, so concurrent managers would otherwise trigger a fatal
// "concurrent map read and map write".
var (
	rmRegistryMu sync.RWMutex
	rmRegistry   map[unsafe.Pointer]*ReliabilityManager
)

func init() {
	rmRegistry = make(map[unsafe.Pointer]*ReliabilityManager)
}

func registerReliabilityManager(rm *ReliabilityManager) {
	rmRegistryMu.Lock()
	defer rmRegistryMu.Unlock()
	if _, ok := rmRegistry[rm.rmCtx]; !ok {
		rmRegistry[rm.rmCtx] = rm
	}
}

func unregisterReliabilityManager(rm *ReliabilityManager) {
	rmRegistryMu.Lock()
	defer rmRegistryMu.Unlock()
	delete(rmRegistry, rm.rmCtx)
}

func lookupReliabilityManager(ctx unsafe.Pointer) (*ReliabilityManager, bool) {
	rmRegistryMu.RLock()
	defer rmRegistryMu.RUnlock()
	rm, ok := rmRegistry[ctx]
	return rm, ok
}

// sdsEventEnvelope is the CBOR wrapper libsds emits for every event:
// { eventType: <wire name>, payload: <event struct> }.
type sdsEventEnvelope struct {
	EventType string          `cbor:"eventType"`
	Payload   cbor.RawMessage `cbor:"payload"`
}

func (rm *ReliabilityManager) RegisterCallbacks(callbacks EventCallbacks) {
	rm.callbacks = callbacks
}

// onEvent decodes the CBOR event envelope and dispatches to the registered
// typed callbacks.
func (rm *ReliabilityManager) onEvent(eventCbor []byte) {
	var env sdsEventEnvelope
	if err := cbor.Unmarshal(eventCbor, &env); err != nil {
		rm.logger.Error("failed to decode sds event envelope", zap.Error(err))
		return
	}

	switch env.EventType {
	case eventMessageReady:
		rm.dispatchMessageEvent(env.Payload, rm.callbacks.OnMessageReady)
	case eventMessageSent:
		rm.dispatchMessageEvent(env.Payload, rm.callbacks.OnMessageSent)
	case eventMissingDependencies:
		rm.dispatchMissingDepsEvent(env.Payload)
	case eventPeriodicSync:
		if rm.callbacks.OnPeriodicSync != nil {
			rm.callbacks.OnPeriodicSync()
		}
	}
}

func (rm *ReliabilityManager) OnCallbackError(callerRet int, err string) {
	rm.logger.Error("sds callback error",
		zap.Int("retCode", callerRet),
		zap.String("errMsg", err))
}

func (rm *ReliabilityManager) dispatchMessageEvent(payload cbor.RawMessage, cb func(MessageID, string)) {
	if cb == nil {
		return
	}
	var p sdsMessageEventPayload
	if err := cbor.Unmarshal(payload, &p); err != nil {
		rm.logger.Error("failed to decode sds message event", zap.Error(err))
		return
	}
	cb(MessageID(p.MessageID), p.ChannelID)
}

func (rm *ReliabilityManager) dispatchMissingDepsEvent(payload cbor.RawMessage) {
	if rm.callbacks.OnMissingDependencies == nil {
		return
	}
	var p sdsMissingDependenciesPayload
	if err := cbor.Unmarshal(payload, &p); err != nil {
		rm.logger.Error("failed to decode sds missing dependencies event", zap.Error(err))
		return
	}
	deps := make([]MessageID, len(p.MissingDeps))
	for i, d := range p.MissingDeps {
		deps[i] = MessageID(d.MessageID)
	}
	rm.callbacks.OnMissingDependencies(MessageID(p.MessageID), deps, p.ChannelID)
}
