//go:build !lint

package sds

/*
	#include <libsds.h>
	#include <stdlib.h>
	#include <stdint.h>
	#include <string.h>

	// Event callback shared by all ReliabilityManager instances; userData carries
	// the ctx handle so the Go side can route the event to the right manager.
	extern void sdsGlobalEventCallback(int ret, char* msg, size_t len, void* userData);

	// Result callback for synchronous request/response calls. `resp` is an
	// SdsResp* that captures the return code and a copy of the CBOR payload.
	void SdsGoCallback(int ret, char* msg, size_t len, void* resp);

	typedef struct {
		int ret;
		char* msg;       // owned copy of the CBOR payload (freed by freeResp)
		size_t len;
		uintptr_t ffiWg; // cgo.Handle for the *sync.WaitGroup the caller blocks on
	} SdsResp;

	static void* allocResp(uintptr_t wg) {
		SdsResp* r = calloc(1, sizeof(SdsResp));
		r->ffiWg = wg;
		return r;
	}

	static void freeResp(void* resp) {
		if (resp != NULL) {
			SdsResp* m = (SdsResp*) resp;
			if (m->msg != NULL) {
				free(m->msg);
			}
			free(m);
		}
	}

	// Copy the callback payload into a buffer owned by resp. The libsds buffer is
	// only valid for the duration of the callback, so we copy before returning.
	static void setRespMsg(void* resp, const char* msg, size_t len) {
		if (resp == NULL || msg == NULL || len == 0) {
			return;
		}
		SdsResp* m = (SdsResp*) resp;
		m->msg = (char*) malloc(len);
		memcpy(m->msg, msg, len);
		m->len = len;
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

	// --- Thin wrappers casting the Go-exported callbacks to SdsCallBack --------

	static void* cGoSdsCreate(const void* reqCbor, size_t reqCborLen, void* resp) {
		return sds_create((const uint8_t*) reqCbor, reqCborLen, (SdsCallBack) SdsGoCallback, resp);
	}

	static int cGoSdsDestroy(void* ctx) {
		return sds_destroy(ctx);
	}

	static unsigned long long cGoSdsAddEventListener(void* ctx, const char* eventName) {
		return sds_add_event_listener(ctx, eventName, (SdsCallBack) sdsGlobalEventCallback, ctx);
	}

	static int cGoSdsWrapOutgoingMessage(void* ctx, const void* reqCbor, size_t reqCborLen, void* resp) {
		return sds_wrap_outgoing_message(ctx, (SdsCallBack) SdsGoCallback, resp, (const uint8_t*) reqCbor, reqCborLen);
	}

	static int cGoSdsUnwrapReceivedMessage(void* ctx, const void* reqCbor, size_t reqCborLen, void* resp) {
		return sds_unwrap_received_message(ctx, (SdsCallBack) SdsGoCallback, resp, (const uint8_t*) reqCbor, reqCborLen);
	}

	static int cGoSdsMarkDependenciesMet(void* ctx, const void* reqCbor, size_t reqCborLen, void* resp) {
		return sds_mark_dependencies_met(ctx, (SdsCallBack) SdsGoCallback, resp, (const uint8_t*) reqCbor, reqCborLen);
	}

	static int cGoSdsReset(void* ctx, const void* reqCbor, size_t reqCborLen, void* resp) {
		return sds_reset(ctx, (SdsCallBack) SdsGoCallback, resp, (const uint8_t*) reqCbor, reqCborLen);
	}

	static int cGoSdsStartPeriodicTasks(void* ctx, const void* reqCbor, size_t reqCborLen, void* resp) {
		return sds_start_periodic_tasks(ctx, (SdsCallBack) SdsGoCallback, resp, (const uint8_t*) reqCbor, reqCborLen);
	}
*/
import "C"
import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"runtime/cgo"
	"sync"
	"unsafe"

	"github.com/fxamacker/cbor/v2"
	errorspkg "github.com/pkg/errors"
	"go.uber.org/zap"
)

var (
	errEmptyReliabilityManager = errors.New("empty reliability manager")
)

// eventNames are the libsds events we subscribe to. Each is registered with the
// shared sdsGlobalEventCallback; the CBOR envelope carries the event type.
var eventNames = []string{
	eventMessageReady,
	eventMessageSent,
	eventMissingDependencies,
	eventPeriodicSync,
}

// randomParticipantID returns a random hex-encoded SDS-R participant identity.
func randomParticipantID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

//export SdsGoCallback
func SdsGoCallback(ret C.int, msg *C.char, len C.size_t, resp unsafe.Pointer) {
	if resp != nil {
		m := (*C.SdsResp)(resp)
		m.ret = ret
		// Copy the CBOR payload into resp-owned memory; the libsds buffer is only
		// valid during this callback.
		C.setRespMsg(resp, msg, len)
		// ffiWg is a cgo.Handle, not a raw Go pointer: the callback fires on the
		// nim-ffi thread while the caller is parked in wg.Wait(), and a raw
		// pointer to the caller's (stack) WaitGroup can dangle when GC relocates
		// that stack. The handle resolves to a stable *sync.WaitGroup.
		wg := cgo.Handle(m.ffiWg).Value().(*sync.WaitGroup)
		wg.Done()
	}
}

// call runs a libsds request that delivers its CBOR result through SdsGoCallback,
// blocks until the callback fires, and returns the (copied) result bytes.
func sdsCall(invoke func(resp unsafe.Pointer)) (int, []byte) {
	wg := &sync.WaitGroup{}
	// Pass the WaitGroup as a cgo.Handle (a stable uintptr token) rather than a
	// raw &wg: storing a Go pointer in C memory and dereferencing it from the
	// callback thread violates the cgo pointer rules and crashes under GC.
	h := cgo.NewHandle(wg)
	defer h.Delete()

	resp := C.allocResp(C.uintptr_t(h))
	defer C.freeResp(resp)

	wg.Add(1)
	invoke(resp)
	wg.Wait()

	ret := int(C.getRet(resp))
	var data []byte
	if n := C.getMyCharLen(resp); n > 0 {
		data = C.GoBytes(unsafe.Pointer(C.getMyCharPtr(resp)), C.int(n))
	}
	return ret, data
}

func respError(prefix string, ret int, data []byte) error {
	if len(data) > 0 {
		// Error payloads from the FFI layer are plain UTF-8 strings, not CBOR.
		return errors.New(prefix + ": " + string(data))
	}
	return errorspkg.Errorf("%s: ret code %d", prefix, ret)
}

// withReqCbor CBOR-encodes req and invokes fn with a pointer+len into the bytes,
// keeping the buffer alive for the duration of the call.
func withReqCbor(req interface{}, fn func(ptr unsafe.Pointer, length C.size_t)) error {
	reqBytes, err := cbor.Marshal(req)
	if err != nil {
		return errorspkg.Wrap(err, "failed to CBOR-encode request")
	}
	var ptr unsafe.Pointer
	if len(reqBytes) > 0 {
		ptr = C.CBytes(reqBytes)
		defer C.free(ptr)
	}
	fn(ptr, C.size_t(len(reqBytes)))
	return nil
}

func NewReliabilityManager(logger *zap.Logger) (*ReliabilityManager, error) {
	if logger == nil {
		logger = zap.NewNop()
	}

	rm := &ReliabilityManager{
		logger: logger,
	}

	rm.logger.Info("creating new reliability manager")

	// participantId is the per-manager SDS-R identity. A non-empty id enables
	// SDS-R (repair/retrieval); we generate a random one so each manager has a
	// distinct identity.
	participantID, err := randomParticipantID()
	if err != nil {
		return nil, errorspkg.Wrap(err, "failed to generate participant id")
	}
	createReq := sdsCreateReq{Config: sdsConfig{ParticipantID: participantID}}

	var ret int
	err = withReqCbor(createReq, func(ptr unsafe.Pointer, length C.size_t) {
		var data []byte
		ret, data = sdsCall(func(resp unsafe.Pointer) {
			rm.rmCtx = C.cGoSdsCreate(ptr, length, resp)
		})
		_ = data // create returns the ctx via the C return value; payload unused
	})
	if err != nil {
		return nil, err
	}
	if rm.rmCtx == nil || ret != C.RET_OK {
		return nil, errors.New("failed to create reliability manager")
	}

	for _, name := range eventNames {
		cName := C.CString(name)
		listenerID := C.cGoSdsAddEventListener(rm.rmCtx, cName)
		C.free(unsafe.Pointer(cName))
		if listenerID == 0 {
			rm.logger.Warn("failed to subscribe to sds event", zap.String("event", name))
		}
	}

	registerReliabilityManager(rm)

	rm.logger.Debug("successfully created reliability manager")
	return rm, nil
}

//export sdsGlobalEventCallback
func sdsGlobalEventCallback(callerRet C.int, msg *C.char, len C.size_t, userData unsafe.Pointer) {
	// Copy the event bytes immediately; the libsds buffer is callback-scoped.
	var eventCbor []byte
	if len > 0 {
		eventCbor = C.GoBytes(unsafe.Pointer(msg), C.int(len))
	}

	rm, ok := rmRegistry[userData] // userData carries the rm's ctx handle
	if !ok {
		return
	}

	if callerRet == C.RET_OK {
		rm.onEvent(eventCbor)
	} else {
		rm.OnCallbackError(int(callerRet), string(eventCbor))
	}
}

func (rm *ReliabilityManager) Cleanup() error {
	if rm == nil {
		return errEmptyReliabilityManager
	}

	rm.logger.Debug("cleaning up reliability manager")

	ret := int(C.cGoSdsDestroy(rm.rmCtx))
	if ret != C.RET_OK {
		return errorspkg.Errorf("error CleanupReliabilityManager: ret code %d", ret)
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

	var ret int
	var data []byte
	err := withReqCbor(sdsEmptyReq{}, func(ptr unsafe.Pointer, length C.size_t) {
		ret, data = sdsCall(func(resp unsafe.Pointer) {
			C.cGoSdsReset(rm.rmCtx, ptr, length, resp)
		})
	})
	if err != nil {
		return err
	}
	if ret != C.RET_OK {
		return respError("error ResetReliabilityManager", ret, data)
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

	req := sdsWrapReq{Req: sdsWrapRequest{
		Message:   message,
		MessageID: string(messageId),
		ChannelID: channelId,
	}}

	var ret int
	var data []byte
	err := withReqCbor(req, func(ptr unsafe.Pointer, length C.size_t) {
		ret, data = sdsCall(func(resp unsafe.Pointer) {
			C.cGoSdsWrapOutgoingMessage(rm.rmCtx, ptr, length, resp)
		})
	})
	if err != nil {
		return nil, err
	}
	if ret != C.RET_OK {
		return nil, respError("error WrapOutgoingMessage", ret, data)
	}

	var wrapResp sdsWrapResponse
	if err := cbor.Unmarshal(data, &wrapResp); err != nil {
		return nil, errorspkg.Wrap(err, "failed to decode wrap response")
	}

	logger.Debug("successfully wrapped message")
	return wrapResp.Message, nil
}

func (rm *ReliabilityManager) UnwrapReceivedMessage(message []byte) (*UnwrappedMessage, error) {
	if rm == nil {
		return nil, errEmptyReliabilityManager
	}

	req := sdsUnwrapReq{Req: sdsUnwrapRequest{Message: message}}

	var ret int
	var data []byte
	err := withReqCbor(req, func(ptr unsafe.Pointer, length C.size_t) {
		ret, data = sdsCall(func(resp unsafe.Pointer) {
			C.cGoSdsUnwrapReceivedMessage(rm.rmCtx, ptr, length, resp)
		})
	})
	if err != nil {
		return nil, err
	}
	if ret != C.RET_OK {
		return nil, respError("error UnwrapReceivedMessage", ret, data)
	}

	var unwrapResp sdsUnwrapResponse
	if err := cbor.Unmarshal(data, &unwrapResp); err != nil {
		return nil, errorspkg.Wrap(err, "failed to decode unwrap response")
	}

	rm.logger.Debug("successfully unwrapped message")

	msg := unwrapResp.Message
	channelId := unwrapResp.ChannelID
	missingDeps := make([]MessageID, len(unwrapResp.MissingDeps))
	for i, dep := range unwrapResp.MissingDeps {
		missingDeps[i] = MessageID(dep.MessageID)
	}

	return &UnwrappedMessage{
		Message:     &msg,
		MissingDeps: &missingDeps,
		ChannelId:   &channelId,
	}, nil
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
	req := sdsMarkDependenciesReq{Req: sdsMarkDependenciesRequest{
		MessageIDs: ids,
		ChannelID:  channelId,
	}}

	var ret int
	var data []byte
	err := withReqCbor(req, func(ptr unsafe.Pointer, length C.size_t) {
		ret, data = sdsCall(func(resp unsafe.Pointer) {
			C.cGoSdsMarkDependenciesMet(rm.rmCtx, ptr, length, resp)
		})
	})
	if err != nil {
		return err
	}
	if ret != C.RET_OK {
		return respError("error MarkDependenciesMet", ret, data)
	}

	rm.logger.Debug("successfully marked dependencies as met")
	return nil
}

func (rm *ReliabilityManager) StartPeriodicTasks() error {
	if rm == nil {
		return errEmptyReliabilityManager
	}

	rm.logger.Debug("starting periodic tasks")

	var ret int
	var data []byte
	err := withReqCbor(sdsEmptyReq{}, func(ptr unsafe.Pointer, length C.size_t) {
		ret, data = sdsCall(func(resp unsafe.Pointer) {
			C.cGoSdsStartPeriodicTasks(rm.rmCtx, ptr, length, resp)
		})
	})
	if err != nil {
		return err
	}
	if ret != C.RET_OK {
		return respError("error StartPeriodicTasks", ret, data)
	}

	rm.logger.Debug("successfully started periodic tasks")
	return nil
}
