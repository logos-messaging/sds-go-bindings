//go:build !lint

package sds

/*
	#include <libsds.h>
	#include <stdlib.h>
	#include <string.h>

	extern void sdsGlobalEventCallback(int ret, char* msg, size_t len, void* userData);

	extern void sdsGlobalRetrievalHintProvider(char* messageId, char** hint, size_t* hintLen, void* userData);

	// Result of one FFI request. ret/msg/len are plain C values (no Go pointers
	// are ever stored in here — the waiter channel lives in a Go-side sync.Map
	// keyed by this struct's address).
	typedef struct {
		int ret;
		char* msg;
		size_t len;
	} SdsResp;

	// libsds hands the callback a buffer that is only valid for the duration of
	// the callback, so we copy it into our own libc buffer (freed by freeResp).
	static char* cGoMemDup(const char* src, size_t len) {
		if (src == NULL || len == 0) {
			return NULL;
		}
		char* dst = (char*) malloc(len);
		if (dst != NULL) {
			memcpy(dst, src, len);
		}
		return dst;
	}

	static void* allocResp() {
		return calloc(1, sizeof(SdsResp));
	}

	static void freeResp(void* resp) {
		if (resp != NULL) {
			SdsResp* r = (SdsResp*) resp;
			if (r->msg != NULL) {
				free(r->msg);
			}
			free(r);
		}
	}

	static char* getMyCharPtr(void* resp) {
		if (resp == NULL) {
			return NULL;
		}
		return ((SdsResp*) resp)->msg;
	}

	static size_t getMyCharLen(void* resp) {
		if (resp == NULL) {
			return 0;
		}
		return ((SdsResp*) resp)->len;
	}

	static int getRet(void* resp) {
		if (resp == NULL) {
			return 0;
		}
		return ((SdsResp*) resp)->ret;
	}

	// resp must be set != NULL when the caller wants the result back.
	void SdsGoCallback(int ret, char* msg, size_t len, void* resp);

	static void* cGoSdsCreate(void* reqCbor, size_t reqCborLen, void* resp) {
		return sds_create((const uint8_t*) reqCbor, reqCborLen, (SdsCallBack) SdsGoCallback, resp);
	}

	static unsigned long long cGoSdsAddEventListener(void* rmCtx, const char* eventName) {
		// 'sdsGlobalEventCallback' is shared amongst all ReliabilityManager
		// instances; we pass rmCtx as userData so the Go side can look up which
		// manager the event is for (cgo can export funcs but not methods).
		return sds_add_event_listener(rmCtx, eventName, (SdsCallBack) sdsGlobalEventCallback, rmCtx);
	}

	static int cGoSdsSetRetrievalHintProvider(void* rmCtx) {
		return sds_set_retrieval_hint_provider(rmCtx, (SdsRetrievalHintProvider) sdsGlobalRetrievalHintProvider, rmCtx);
	}

	static int cGoSdsDestroy(void* rmCtx) {
		return sds_destroy(rmCtx);
	}

	static int cGoSdsReset(void* rmCtx, void* reqCbor, size_t reqCborLen, void* resp) {
		return sds_reset(rmCtx, (SdsCallBack) SdsGoCallback, resp, (const uint8_t*) reqCbor, reqCborLen);
	}

	static int cGoSdsWrapOutgoingMessage(void* rmCtx, void* reqCbor, size_t reqCborLen, void* resp) {
		return sds_wrap_outgoing_message(rmCtx, (SdsCallBack) SdsGoCallback, resp, (const uint8_t*) reqCbor, reqCborLen);
	}

	static int cGoSdsUnwrapReceivedMessage(void* rmCtx, void* reqCbor, size_t reqCborLen, void* resp) {
		return sds_unwrap_received_message(rmCtx, (SdsCallBack) SdsGoCallback, resp, (const uint8_t*) reqCbor, reqCborLen);
	}

	static int cGoSdsMarkDependenciesMet(void* rmCtx, void* reqCbor, size_t reqCborLen, void* resp) {
		return sds_mark_dependencies_met(rmCtx, (SdsCallBack) SdsGoCallback, resp, (const uint8_t*) reqCbor, reqCborLen);
	}

	static int cGoSdsStartPeriodicTasks(void* rmCtx, void* reqCbor, size_t reqCborLen, void* resp) {
		return sds_start_periodic_tasks(rmCtx, (SdsCallBack) SdsGoCallback, resp, (const uint8_t*) reqCbor, reqCborLen);
	}
*/
import "C"
import (
	"errors"
	"sync"
	"unsafe"

	"github.com/fxamacker/cbor/v2"
	errorspkg "github.com/pkg/errors"
	"go.uber.org/zap"
)

var errEmptyReliabilityManager = errors.New("empty reliability manager")

// emptyReqCbor is the CBOR encoding of an empty map (0xa0), used for methods
// whose request envelope has no fields (sds_reset, sds_start_periodic_tasks).
// The Nim side still decodes the payload into the (fieldless) Req object, which
// rejects a zero-length buffer.
var emptyReqCbor = []byte{0xa0}

// respWaiters maps a C SdsResp* (the request's userData) to the channel the
// caller is blocked on. Keeping the channel here — rather than in the C struct —
// avoids storing a Go pointer in C memory (forbidden by the cgo pointer rules).
var respWaiters sync.Map

// CBOR request/response wire types. Field names (cbor tags) must match the Nim
// {.ffi.} object field names in library/libsds.nim exactly. Each method's params
// are wrapped under the Nim param name ("req"/"config") that the macro packs
// into the per-proc request envelope.

type sdsConfig struct {
	ParticipantId string `cbor:"participantId"`
}

type sdsCreateReq struct {
	Config sdsConfig `cbor:"config"`
}

type sdsWrapRequest struct {
	Message   []byte `cbor:"message"`
	MessageId string `cbor:"messageId"`
	ChannelId string `cbor:"channelId"`
}

type sdsWrapReq struct {
	Req sdsWrapRequest `cbor:"req"`
}

type sdsWrapResponse struct {
	Message []byte `cbor:"message"`
}

type sdsUnwrapRequest struct {
	Message []byte `cbor:"message"`
}

type sdsUnwrapReq struct {
	Req sdsUnwrapRequest `cbor:"req"`
}

type sdsUnwrapResponse struct {
	Message     []byte          `cbor:"message"`
	ChannelId   string          `cbor:"channelId"`
	MissingDeps []sdsMissingDep `cbor:"missingDeps"`
}

type sdsMarkDependenciesRequest struct {
	MessageIds []string `cbor:"messageIds"`
	ChannelId  string   `cbor:"channelId"`
}

type sdsMarkDepsReq struct {
	Req sdsMarkDependenciesRequest `cbor:"req"`
}

//export SdsGoCallback
func SdsGoCallback(ret C.int, msg *C.char, length C.size_t, resp unsafe.Pointer) {
	if resp == nil {
		return
	}
	// Winner-takes-all: only the first delivery for this resp owns it. A
	// duplicate or late fire (after the waiter has been served and the resp
	// possibly freed/reused) finds nothing and must not touch resp.
	chVal, ok := respWaiters.LoadAndDelete(resp)
	if !ok {
		return
	}
	m := (*C.SdsResp)(resp)
	m.ret = ret
	// Copy the payload now: libsds frees it as soon as this callback returns.
	if msg != nil && length > 0 {
		m.msg = C.cGoMemDup(msg, length)
		m.len = length
	}
	close(chVal.(chan struct{}))
}

// nonNilBytes makes empty slices encode as a CBOR byte string (0x40) instead of
// CBOR null, which the Nim seq[byte] decoder expects.
func nonNilBytes(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

func cBytes(b []byte) (unsafe.Pointer, C.size_t) {
	if len(b) == 0 {
		return nil, 0
	}
	return C.CBytes(b), C.size_t(len(b))
}

func respErr(resp unsafe.Pointer) string {
	if l := C.getMyCharLen(resp); l > 0 {
		return C.GoStringN(C.getMyCharPtr(resp), C.int(l))
	}
	return ""
}

// call dispatches a request to the FFI worker thread and blocks until the
// result callback fires, returning the (copied) CBOR response payload.
func (rm *ReliabilityManager) call(
	reqBytes []byte, invoke func(reqPtr unsafe.Pointer, reqLen C.size_t, resp unsafe.Pointer),
) ([]byte, error) {
	resp := C.allocResp()
	defer C.freeResp(resp)

	ch := make(chan struct{})
	respWaiters.Store(resp, ch)
	defer respWaiters.Delete(resp)

	reqPtr, reqLen := cBytes(reqBytes)
	if reqPtr != nil {
		defer C.free(reqPtr)
	}

	invoke(reqPtr, reqLen, resp)
	<-ch

	if C.getRet(resp) != C.RET_OK {
		return nil, errors.New(respErr(resp))
	}

	var data []byte
	if l := C.getMyCharLen(resp); l > 0 {
		data = C.GoBytes(unsafe.Pointer(C.getMyCharPtr(resp)), C.int(l))
	}
	return data, nil
}

func NewReliabilityManager(logger *zap.Logger) (*ReliabilityManager, error) {
	if logger == nil {
		logger = zap.NewNop()
	}

	rm := &ReliabilityManager{
		logger: logger,
	}

	rm.logger.Info("creating new reliability manager")

	reqBytes, err := cbor.Marshal(sdsCreateReq{Config: sdsConfig{ParticipantId: ""}})
	if err != nil {
		return nil, errorspkg.Wrap(err, "failed to marshal create request")
	}

	resp := C.allocResp()
	defer C.freeResp(resp)

	ch := make(chan struct{})
	respWaiters.Store(resp, ch)
	defer respWaiters.Delete(resp)

	reqPtr, reqLen := cBytes(reqBytes)
	if reqPtr != nil {
		defer C.free(reqPtr)
	}

	ctx := C.cGoSdsCreate(reqPtr, reqLen, resp)
	<-ch

	if C.getRet(resp) != C.RET_OK {
		errMsg := respErr(resp)
		if ctx != nil {
			C.cGoSdsDestroy(ctx)
		}
		return nil, errors.New("error creating reliability manager: " + errMsg)
	}
	if ctx == nil {
		return nil, errors.New("error creating reliability manager: nil context")
	}

	rm.rmCtx = ctx
	registerReliabilityManager(rm)

	// Register one listener per event name plus the retrieval-hint provider.
	for _, ev := range sdsEventNames {
		cev := C.CString(ev)
		C.cGoSdsAddEventListener(rm.rmCtx, cev)
		C.free(unsafe.Pointer(cev))
	}
	C.cGoSdsSetRetrievalHintProvider(rm.rmCtx)

	rm.logger.Debug("successfully created reliability manager")
	return rm, nil
}

//export sdsGlobalEventCallback
func sdsGlobalEventCallback(callerRet C.int, msg *C.char, length C.size_t, userData unsafe.Pointer) {
	rm, ok := lookupReliabilityManager(userData) // userData carries rm's ctx
	if !ok {
		return
	}

	if callerRet != C.RET_OK {
		errStr := ""
		if msg != nil && length > 0 {
			errStr = C.GoStringN(msg, C.int(length))
		}
		rm.OnCallbackError(int(callerRet), errStr)
		return
	}

	if msg == nil || length == 0 {
		return
	}
	// Decode immediately: the buffer is only valid during this callback.
	data := C.GoBytes(unsafe.Pointer(msg), C.int(length))
	rm.OnEvent(data)
}

//export sdsGlobalRetrievalHintProvider
func sdsGlobalRetrievalHintProvider(messageId *C.char, hint **C.char, hintLen *C.size_t, userData unsafe.Pointer) {
	rm, ok := lookupReliabilityManager(userData)
	if !ok {
		return
	}
	if rm.callbacks.RetrievalHintProvider == nil {
		return
	}

	msgId := C.GoString(messageId)
	hintBytes := rm.callbacks.RetrievalHintProvider(MessageID(msgId))
	if len(hintBytes) > 0 {
		// libsds takes ownership and frees this with libc free.
		*hint = (*C.char)(C.CBytes(hintBytes))
		*hintLen = C.size_t(len(hintBytes))
	}
}

func (rm *ReliabilityManager) Cleanup() error {
	if rm == nil {
		return errEmptyReliabilityManager
	}

	rm.logger.Debug("cleaning up reliability manager")

	if C.cGoSdsDestroy(rm.rmCtx) != C.RET_OK {
		return errors.New("error CleanupReliabilityManager")
	}

	unregisterReliabilityManager(rm)
	rm.logger.Debug("cleaned up reliability manager")
	return nil
}

func (rm *ReliabilityManager) Reset() error {
	if rm == nil {
		return errEmptyReliabilityManager
	}

	rm.logger.Debug("resetting reliability manager")

	_, err := rm.call(emptyReqCbor, func(reqPtr unsafe.Pointer, reqLen C.size_t, resp unsafe.Pointer) {
		C.cGoSdsReset(rm.rmCtx, reqPtr, reqLen, resp)
	})
	if err != nil {
		return errors.New("error ResetReliabilityManager: " + err.Error())
	}

	rm.logger.Debug("successfully resetted reliability manager")
	return nil
}

func (rm *ReliabilityManager) WrapOutgoingMessage(message []byte, messageId MessageID, channelId string) ([]byte, error) {
	if rm == nil {
		return nil, errEmptyReliabilityManager
	}

	logger := rm.logger.With(zap.String("messageId", string(messageId)))
	logger.Debug("wrapping outgoing message")

	reqBytes, err := cbor.Marshal(sdsWrapReq{
		Req: sdsWrapRequest{
			Message:   nonNilBytes(message),
			MessageId: string(messageId),
			ChannelId: channelId,
		},
	})
	if err != nil {
		return nil, errorspkg.Wrap(err, "failed to marshal wrap request")
	}

	data, err := rm.call(reqBytes, func(reqPtr unsafe.Pointer, reqLen C.size_t, resp unsafe.Pointer) {
		C.cGoSdsWrapOutgoingMessage(rm.rmCtx, reqPtr, reqLen, resp)
	})
	if err != nil {
		return nil, errors.New("error WrapOutgoingMessage: " + err.Error())
	}

	if len(data) == 0 {
		logger.Debug("received empty response for wrap")
		return nil, nil
	}

	var resp sdsWrapResponse
	if err := cbor.Unmarshal(data, &resp); err != nil {
		return nil, errorspkg.Wrap(err, "failed to unmarshal wrap response")
	}

	logger.Debug("successfully wrapped message")
	return resp.Message, nil
}

func (rm *ReliabilityManager) UnwrapReceivedMessage(message []byte) (*UnwrappedMessage, error) {
	if rm == nil {
		return nil, errEmptyReliabilityManager
	}

	reqBytes, err := cbor.Marshal(sdsUnwrapReq{Req: sdsUnwrapRequest{Message: nonNilBytes(message)}})
	if err != nil {
		return nil, errorspkg.Wrap(err, "failed to marshal unwrap request")
	}

	data, err := rm.call(reqBytes, func(reqPtr unsafe.Pointer, reqLen C.size_t, resp unsafe.Pointer) {
		C.cGoSdsUnwrapReceivedMessage(rm.rmCtx, reqPtr, reqLen, resp)
	})
	if err != nil {
		return nil, errors.New("error UnwrapReceivedMessage: " + err.Error())
	}

	if len(data) == 0 {
		rm.logger.Debug("received empty response for unwrap")
		return nil, nil
	}

	var resp sdsUnwrapResponse
	if err := cbor.Unmarshal(data, &resp); err != nil {
		return nil, errorspkg.Wrap(err, "failed to unmarshal unwrapped message")
	}

	msg := resp.Message
	channelId := resp.ChannelId
	deps := make([]HistoryEntry, len(resp.MissingDeps))
	for i, d := range resp.MissingDeps {
		deps[i] = HistoryEntry{MessageID: MessageID(d.MessageId), RetrievalHint: d.RetrievalHint}
	}

	rm.logger.Debug("successfully unwrapped message")
	return &UnwrappedMessage{Message: &msg, MissingDeps: &deps, ChannelId: &channelId}, nil
}

// MarkDependenciesMet informs the library that dependencies are met
func (rm *ReliabilityManager) MarkDependenciesMet(messageIDs []MessageID, channelId string) error {
	if rm == nil {
		return errEmptyReliabilityManager
	}

	if len(messageIDs) == 0 {
		return nil // Nothing to do
	}

	ids := make([]string, len(messageIDs))
	for i, id := range messageIDs {
		ids[i] = string(id)
	}

	reqBytes, err := cbor.Marshal(sdsMarkDepsReq{
		Req: sdsMarkDependenciesRequest{MessageIds: ids, ChannelId: channelId},
	})
	if err != nil {
		return errorspkg.Wrap(err, "failed to marshal mark-dependencies request")
	}

	_, err = rm.call(reqBytes, func(reqPtr unsafe.Pointer, reqLen C.size_t, resp unsafe.Pointer) {
		C.cGoSdsMarkDependenciesMet(rm.rmCtx, reqPtr, reqLen, resp)
	})
	if err != nil {
		return errors.New("error MarkDependenciesMet: " + err.Error())
	}

	rm.logger.Debug("successfully marked dependencies as met")
	return nil
}

func (rm *ReliabilityManager) StartPeriodicTasks() error {
	if rm == nil {
		return errEmptyReliabilityManager
	}

	rm.logger.Debug("starting periodic tasks")

	_, err := rm.call(emptyReqCbor, func(reqPtr unsafe.Pointer, reqLen C.size_t, resp unsafe.Pointer) {
		C.cGoSdsStartPeriodicTasks(rm.rmCtx, reqPtr, reqLen, resp)
	})
	if err != nil {
		return errors.New("error StartPeriodicTasks: " + err.Error())
	}

	rm.logger.Debug("successfully started periodic tasks")
	return nil
}
