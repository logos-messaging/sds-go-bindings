//go:build !lint

package sds

/*
	#include <libsds.h>
	#include <stdlib.h>
	#include <string.h>

	extern void sdsGlobalEventCallback(int ret, char* msg, size_t len, void* userData);

	extern void sdsGlobalRetrievalHintProvider(char* messageId, char** hint, size_t* hintLen, void* userData);

	typedef struct {
		int ret;
		char* msg;
		size_t len;
	} SdsResp;

	static void* allocResp(void) {
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

	// libsds frees the buffer it hands the callback as soon as the callback
	// returns, so the Go side must copy it before the waiting goroutine reads
	// it. cGoMemDup makes that copy onto the C heap (freed by freeResp).
	static char* cGoMemDup(const void* src, size_t len) {
		if (src == NULL || len == 0) {
			return NULL;
		}
		char* dst = (char*) malloc(len);
		if (dst != NULL) {
			memcpy(dst, src, len);
		}
		return dst;
	}

	static char* getMyCharPtr(void* resp) {
		if (resp == NULL) {
			return NULL;
		}
		SdsResp* m = (SdsResp*) resp;
		return m->msg;
	}

	static size_t getMyCharLen(void* resp) {
		if (resp == NULL) {
			return 0;
		}
		SdsResp* m = (SdsResp*) resp;
		return m->len;
	}

	static int getRet(void* resp) {
		if (resp == NULL) {
			return 0;
		}
		SdsResp* m = (SdsResp*) resp;
		return m->ret;
	}

	// SdsGoCallback is the result callback for the request/response FFI calls.
	void SdsGoCallback(int ret, char* msg, size_t len, void* resp);

	static void* cGoSdsCreate(const char* configJson, void* resp) {
		return sds_create(configJson, (SdsCallBack) SdsGoCallback, resp);
	}

	static void cGoSdsSetEventCallback(void* ctx) {
		// 'sdsGlobalEventCallback' is shared by all manager instances; we pass the
		// ctx as userData so the dispatcher can route the event to the instance
		// that registered it (cgo can export Go funcs but not methods).
		sds_set_event_callback(ctx, (SdsCallBack) sdsGlobalEventCallback, ctx);
	}

	static int cGoSdsSetRetrievalHintProvider(void* ctx) {
		return sds_set_retrieval_hint_provider(ctx, (SdsRetrievalHintProvider) sdsGlobalRetrievalHintProvider, ctx);
	}

	static int cGoSdsWrapOutgoingMessage(void* ctx, const char* reqJson, void* resp) {
		return sds_wrap_outgoing_message(ctx, (SdsCallBack) SdsGoCallback, resp, reqJson);
	}

	static int cGoSdsUnwrapReceivedMessage(void* ctx, const char* reqJson, void* resp) {
		return sds_unwrap_received_message(ctx, (SdsCallBack) SdsGoCallback, resp, reqJson);
	}

	static int cGoSdsMarkDependenciesMet(void* ctx, const char* reqJson, void* resp) {
		return sds_mark_dependencies_met(ctx, (SdsCallBack) SdsGoCallback, resp, reqJson);
	}

	static int cGoSdsReset(void* ctx, void* resp) {
		return sds_reset(ctx, (SdsCallBack) SdsGoCallback, resp);
	}

	static int cGoSdsStartPeriodicTasks(void* ctx, void* resp) {
		return sds_start_periodic_tasks(ctx, (SdsCallBack) SdsGoCallback, resp);
	}

	static int cGoSdsDestroy(void* ctx, void* resp) {
		return sds_destroy(ctx, (SdsCallBack) SdsGoCallback, resp);
	}
*/
import "C"
import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"unsafe"

	"go.uber.org/zap"
)

var (
	errEmptyReliabilityManager = errors.New("empty reliability manager")
)

// respWaiters maps an in-flight call's resp pointer (a stable C-heap address)
// to the channel its result callback closes. We deliberately do NOT store a Go
// pointer (e.g. &sync.WaitGroup) in the C SdsResp: a goroutine parked in the
// call can have its stack moved by the GC, leaving the C-held Go pointer stale
// — a use-after-free that corrupts memory once the callback fires on the worker
// thread. Keying by the C pointer keeps all Go pointers on the Go side.
var respWaiters sync.Map // uintptr(resp) -> chan struct{}

// awaitResp allocates a resp, registers its waiter, runs `fire` (which
// dispatches the FFI call passing resp as userData), and blocks until the
// result callback closes the channel. The caller owns the returned resp and
// must C.freeResp it.
func awaitResp(fire func(resp unsafe.Pointer)) unsafe.Pointer {
	resp := C.allocResp()
	done := make(chan struct{})
	respWaiters.Store(uintptr(resp), done)
	fire(resp)
	<-done
	respWaiters.Delete(uintptr(resp))
	return resp
}

// jsonByteArray marshals to a JSON array of byte values (e.g. [104,105]).
// libsds (nim-ffi) (de)serialises `seq[byte]` as a JSON number array, NOT
// base64 — Go's default []byte marshalling (base64 string) would not decode.
type jsonByteArray []byte

func (b jsonByteArray) MarshalJSON() ([]byte, error) {
	if len(b) == 0 {
		return []byte("[]"), nil
	}
	buf := make([]byte, 0, len(b)*4+2)
	buf = append(buf, '[')
	for i, v := range b {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = strconv.AppendUint(buf, uint64(v), 10)
	}
	buf = append(buf, ']')
	return buf, nil
}

// Request payloads. Field names/types must match the {.ffi.} request objects
// declared in nim-sds' library/libsds.nim. Responses come back as JSON too;
// incoming []byte fields decode fine from a JSON number array.
type sdsConfig struct {
	ParticipantId string `json:"participantId"`
}

type sdsWrapRequest struct {
	Message   jsonByteArray `json:"message"`
	MessageId string        `json:"messageId"`
	ChannelId string        `json:"channelId"`
}

type sdsWrapResponse struct {
	Message []byte `json:"message"`
}

type sdsUnwrapRequest struct {
	Message jsonByteArray `json:"message"`
}

type sdsMarkDependenciesRequest struct {
	MessageIds []string `json:"messageIds"`
	ChannelId  string   `json:"channelId"`
}

//export SdsGoCallback
func SdsGoCallback(ret C.int, msg *C.char, length C.size_t, resp unsafe.Pointer) {
	if resp == nil {
		return
	}
	m := (*C.SdsResp)(resp)
	m.ret = ret
	// libsds frees 'msg' as soon as this callback returns, so copy the bytes
	// before unblocking the waiting goroutine that reads them.
	if msg != nil && length > 0 {
		m.msg = C.cGoMemDup(unsafe.Pointer(msg), length)
		m.len = length
	}
	if ch, ok := respWaiters.Load(uintptr(resp)); ok {
		close(ch.(chan struct{}))
	}
}

func respString(resp unsafe.Pointer) string {
	return C.GoStringN(C.getMyCharPtr(resp), C.int(C.getMyCharLen(resp)))
}

// request dispatches one FFI call and blocks until the result callback fires.
func (rm *ReliabilityManager) request(
	errPrefix string,
	fn func(resp unsafe.Pointer) C.int,
) (string, error) {
	resp := awaitResp(func(r unsafe.Pointer) { fn(r) })
	defer C.freeResp(resp)

	if C.getRet(resp) != C.RET_OK {
		return "", fmt.Errorf("%s: %s", errPrefix, respString(resp))
	}
	return respString(resp), nil
}

func NewReliabilityManager(logger *zap.Logger) (*ReliabilityManager, error) {
	if logger == nil {
		logger = zap.NewNop()
	}

	rm := &ReliabilityManager{
		logger: logger,
	}

	rm.logger.Info("creating new reliability manager")

	// An empty participantId disables SDS-R, matching the previous behaviour.
	configJson, err := json.Marshal(sdsConfig{ParticipantId: ""})
	if err != nil {
		return nil, fmt.Errorf("failed to encode config: %w", err)
	}
	cConfig := C.CString(string(configJson))
	defer C.free(unsafe.Pointer(cConfig))

	resp := awaitResp(func(r unsafe.Pointer) {
		rm.rmCtx = C.cGoSdsCreate(cConfig, r)
	})
	defer C.freeResp(resp)

	if rm.rmCtx == nil || C.getRet(resp) != C.RET_OK {
		return nil, fmt.Errorf("error creating reliability manager: %s", respString(resp))
	}

	// Register before wiring the event callback, since the callback routes by
	// ctx through the registry.
	registerReliabilityManager(rm)
	C.cGoSdsSetEventCallback(rm.rmCtx)
	C.cGoSdsSetRetrievalHintProvider(rm.rmCtx)

	rm.logger.Debug("successfully created reliability manager")
	return rm, nil
}

//export sdsGlobalEventCallback
func sdsGlobalEventCallback(callerRet C.int, msg *C.char, length C.size_t, userData unsafe.Pointer) {
	msgStr := C.GoStringN(msg, C.int(length))
	rm, ok := lookupReliabilityManager(userData) // userData contains rm's ctx
	if !ok {
		return
	}

	if callerRet == C.RET_OK {
		rm.OnEvent(msgStr)
	} else {
		rm.OnCallbackError(int(callerRet), msgStr)
	}
}

//export sdsGlobalRetrievalHintProvider
func sdsGlobalRetrievalHintProvider(messageId *C.char, hint **C.char, hintLen *C.size_t, userData unsafe.Pointer) {
	msgId := C.GoString(messageId)
	rm, ok := lookupReliabilityManager(userData)
	if ok {
		if rm.callbacks.RetrievalHintProvider != nil {
			hintBytes := rm.callbacks.RetrievalHintProvider(MessageID(msgId))
			if len(hintBytes) > 0 {
				*hint = (*C.char)(C.CBytes(hintBytes))
				*hintLen = C.size_t(len(hintBytes))
			}
		}
	}
}

func (rm *ReliabilityManager) Cleanup() error {
	if rm == nil {
		return errEmptyReliabilityManager
	}

	rm.logger.Debug("cleaning up reliability manager")

	_, err := rm.request("error CleanupReliabilityManager", func(resp unsafe.Pointer) C.int {
		return C.cGoSdsDestroy(rm.rmCtx, resp)
	})
	if err != nil {
		return err
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

	_, err := rm.request("error ResetReliabilityManager", func(resp unsafe.Pointer) C.int {
		return C.cGoSdsReset(rm.rmCtx, resp)
	})
	if err != nil {
		return err
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

	reqJson, err := json.Marshal(sdsWrapRequest{
		Message:   message,
		MessageId: string(messageId),
		ChannelId: channelId,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to encode wrap request: %w", err)
	}
	cReq := C.CString(string(reqJson))
	defer C.free(unsafe.Pointer(cReq))

	respJson, err := rm.request("error WrapOutgoingMessage", func(resp unsafe.Pointer) C.int {
		return C.cGoSdsWrapOutgoingMessage(rm.rmCtx, cReq, resp)
	})
	if err != nil {
		return nil, err
	}

	var wrapResp sdsWrapResponse
	if err := json.Unmarshal([]byte(respJson), &wrapResp); err != nil {
		return nil, fmt.Errorf("failed to decode wrap response: %w", err)
	}

	logger.Debug("successfully wrapped message")
	return wrapResp.Message, nil
}

func (rm *ReliabilityManager) UnwrapReceivedMessage(message []byte) (*UnwrappedMessage, error) {
	if rm == nil {
		return nil, errEmptyReliabilityManager
	}

	reqJson, err := json.Marshal(sdsUnwrapRequest{Message: message})
	if err != nil {
		return nil, fmt.Errorf("failed to encode unwrap request: %w", err)
	}
	cReq := C.CString(string(reqJson))
	defer C.free(unsafe.Pointer(cReq))

	respJson, err := rm.request("error UnwrapReceivedMessage", func(resp unsafe.Pointer) C.int {
		return C.cGoSdsUnwrapReceivedMessage(rm.rmCtx, cReq, resp)
	})
	if err != nil {
		return nil, err
	}

	var unwrappedMessage UnwrappedMessage
	if err := json.Unmarshal([]byte(respJson), &unwrappedMessage); err != nil {
		return nil, fmt.Errorf("failed to decode unwrap response: %w", err)
	}

	rm.logger.Debug("successfully unwrapped message")
	return &unwrappedMessage, nil
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
	reqJson, err := json.Marshal(sdsMarkDependenciesRequest{MessageIds: ids, ChannelId: channelId})
	if err != nil {
		return fmt.Errorf("failed to encode mark-dependencies request: %w", err)
	}
	cReq := C.CString(string(reqJson))
	defer C.free(unsafe.Pointer(cReq))

	_, err = rm.request("error MarkDependenciesMet", func(resp unsafe.Pointer) C.int {
		return C.cGoSdsMarkDependenciesMet(rm.rmCtx, cReq, resp)
	})
	if err != nil {
		return err
	}

	rm.logger.Debug("successfully marked dependencies as met")
	return nil
}

func (rm *ReliabilityManager) StartPeriodicTasks() error {
	if rm == nil {
		return errEmptyReliabilityManager
	}

	rm.logger.Debug("starting periodic tasks")

	_, err := rm.request("error StartPeriodicTasks", func(resp unsafe.Pointer) C.int {
		return C.cGoSdsStartPeriodicTasks(rm.rmCtx, resp)
	})
	if err != nil {
		return err
	}

	rm.logger.Debug("successfully started periodic tasks")
	return nil
}
