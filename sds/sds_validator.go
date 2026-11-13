//go:build !lint

package sds

/*
#include <stdint.h>
#include <stddef.h>
#define SDS_VALIDATION_ACCEPT 0
#define SDS_VALIDATION_REJECT 1
#define SDS_VALIDATION_ERROR 2
*/
import "C"

import (
	"math"
	"unsafe"

	"go.uber.org/zap"
)

//export sdsGlobalValidatorCallback
func sdsGlobalValidatorCallback(
	channelID *C.uint8_t, channelIDLen C.size_t,
	content *C.uint8_t, contentLen C.size_t,
	senderID *C.uint8_t, senderIDLen C.size_t,
	validationContext *C.uint8_t, validationContextLen C.size_t,
	ctx C.uintptr_t,
) C.int {
	rm, ok := lookupReliabilityManager(uintptr(ctx))
	if !ok {
		return C.SDS_VALIDATION_ERROR
	}
	rm.validatorMu.RLock()
	validator := rm.validator
	rm.validatorMu.RUnlock()
	if validator == nil {
		return C.SDS_VALIDATION_ACCEPT
	}
	if max(channelIDLen, contentLen, senderIDLen, validationContextLen) > C.size_t(math.MaxInt32) {
		rm.logger.Error("SDS validator input exceeds supported buffer length")
		return C.SDS_VALIDATION_ERROR
	}
	message := ValidationMessage{
		ChannelID:         C.GoStringN((*C.char)(unsafe.Pointer(channelID)), C.int(channelIDLen)),
		Content:           C.GoBytes(unsafe.Pointer(content), C.int(contentLen)),
		SenderID:          C.GoStringN((*C.char)(unsafe.Pointer(senderID)), C.int(senderIDLen)),
		ValidationContext: C.GoBytes(unsafe.Pointer(validationContext), C.int(validationContextLen)),
	}
	accepted, err := validator.Validate(message)
	if err != nil {
		rm.logger.Error("SDS message validation failed",
			zap.String("channelID", message.ChannelID), zap.Error(err))
		return C.SDS_VALIDATION_ERROR
	}
	if !accepted {
		return C.SDS_VALIDATION_REJECT
	}
	return C.SDS_VALIDATION_ACCEPT
}
