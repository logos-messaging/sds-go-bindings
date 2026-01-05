package sds

import (
	"encoding/json"
	"time"
	"unsafe"

	"go.uber.org/zap"
)

const requestTimeout = 30 * time.Second
const EventChanBufferSize = 1024

type EventCallbacks struct {
	OnMessageReady        func(messageId MessageID, channelId string)
	OnMessageSent         func(messageId MessageID, channelId string)
	OnMissingDependencies func(messageId MessageID, missingDeps []HistoryEntry, channelId string)
	OnPeriodicSync        func()
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

func init() {
	rmRegistry = make(map[unsafe.Pointer]*ReliabilityManager)
}

func registerReliabilityManager(rm *ReliabilityManager) {
	_, ok := rmRegistry[rm.rmCtx]
	if !ok {
		rmRegistry[rm.rmCtx] = rm
	}
}

func unregisterReliabilityManager(rm *ReliabilityManager) {
	delete(rmRegistry, rm.rmCtx)
}

type jsonEvent struct {
	EventType string `json:"eventType"`
}

type msgEvent struct {
	MessageId MessageID `json:"messageId"`
	ChannelId string    `json:"channelId"`
}

type missingDepsEvent struct {
	MessageId   MessageID   `json:"messageId"`
	MissingDeps []HistoryEntry `json:"missingDeps"`
	ChannelId   string         `json:"channelId"`
}

func (rm *ReliabilityManager) RegisterCallbacks(callbacks EventCallbacks) {
	rm.callbacks = callbacks
}

func (rm *ReliabilityManager) OnEvent(eventStr string) {
	jsonEvent := jsonEvent{}
	err := json.Unmarshal([]byte(eventStr), &jsonEvent)
	if err != nil {
		rm.logger.Error("failed to unmarshal sds event string", zap.Error(err))
		return
	}

	switch jsonEvent.EventType {
	case "message_ready":
		rm.parseMessageReadyEvent(eventStr)
	case "message_sent":
		rm.parseMessageSentEvent(eventStr)
	case "missing_dependencies":
		rm.parseMissingDepsEvent(eventStr)
	case "periodic_sync":
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

func (rm *ReliabilityManager) parseMessageReadyEvent(eventStr string) {
	msgEvent := msgEvent{}
	err := json.Unmarshal([]byte(eventStr), &msgEvent)
	if err != nil {
		rm.logger.Error("failed to parse message ready event", zap.Error(err))
	}

	if rm.callbacks.OnMessageReady != nil {
		rm.callbacks.OnMessageReady(msgEvent.MessageId, msgEvent.ChannelId)
	}
}

func (rm *ReliabilityManager) parseMessageSentEvent(eventStr string) {
	msgEvent := msgEvent{}
	err := json.Unmarshal([]byte(eventStr), &msgEvent)
	if err != nil {
		rm.logger.Error("failed to parse message sent event", zap.Error(err))
		return
	}

	if rm.callbacks.OnMessageSent != nil {
		rm.callbacks.OnMessageSent(msgEvent.MessageId, msgEvent.ChannelId)
	}
}

func (rm *ReliabilityManager) parseMissingDepsEvent(eventStr string) {
	missingDepsEvent := missingDepsEvent{}
	err := json.Unmarshal([]byte(eventStr), &missingDepsEvent)
	if err != nil {
		rm.logger.Error("failed to parse missing dependencies event", zap.Error(err))
		return
	}

	if rm.callbacks.OnMissingDependencies != nil {
		rm.callbacks.OnMissingDependencies(missingDepsEvent.MessageId, missingDepsEvent.MissingDeps, missingDepsEvent.ChannelId)
	}
}
