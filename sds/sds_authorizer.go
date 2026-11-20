//go:build !lint

package sds

/*
#include <stdint.h>
#include <stddef.h>
#define SDS_AUTHORIZATION_ACCEPT 0
#define SDS_AUTHORIZATION_REJECT 1
#define SDS_AUTHORIZATION_ERROR 2
*/
import "C"

import (
	"math"
	"unsafe"

	"go.uber.org/zap"
)

//export sdsGlobalAuthorizerCallback
func sdsGlobalAuthorizerCallback(
	channelID *C.uint8_t, channelIDLen C.size_t,
	content *C.uint8_t, contentLen C.size_t,
	senderID *C.uint8_t, senderIDLen C.size_t,
	authorizationContext *C.uint8_t, authorizationContextLen C.size_t,
	ctx C.uintptr_t,
) C.int {
	rm, ok := lookupReliabilityManager(uintptr(ctx))
	if !ok {
		return C.SDS_AUTHORIZATION_ERROR
	}
	rm.authorizerMu.RLock()
	authorizer := rm.authorizer
	rm.authorizerMu.RUnlock()
	if authorizer == nil {
		return C.SDS_AUTHORIZATION_ACCEPT
	}
	if max(channelIDLen, contentLen, senderIDLen, authorizationContextLen) > C.size_t(math.MaxInt32) {
		rm.logger.Error("SDS authorizer input exceeds supported buffer length")
		return C.SDS_AUTHORIZATION_ERROR
	}
	message := AuthorizationMessage{
		ChannelID:            C.GoStringN((*C.char)(unsafe.Pointer(channelID)), C.int(channelIDLen)),
		Content:              C.GoBytes(unsafe.Pointer(content), C.int(contentLen)),
		SenderID:             C.GoStringN((*C.char)(unsafe.Pointer(senderID)), C.int(senderIDLen)),
		AuthorizationContext: C.GoBytes(unsafe.Pointer(authorizationContext), C.int(authorizationContextLen)),
	}
	accepted, err := authorizer.Authorize(message)
	if err != nil {
		rm.logger.Error("SDS message authorization failed",
			zap.String("channelID", message.ChannelID), zap.Error(err))
		return C.SDS_AUTHORIZATION_ERROR
	}
	if !accepted {
		return C.SDS_AUTHORIZATION_REJECT
	}
	return C.SDS_AUTHORIZATION_ACCEPT
}
