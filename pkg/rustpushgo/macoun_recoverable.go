package rustpushgo

// Hand-written binding for Client::sync_recoverable_zone (src/lib.rs), in the
// shape uniffi-bindgen-go produces, kept out of rustpushgo.go so regenerating
// the bindings does not clash with it. Once the bindings are regenerated from a
// library that exports the method, the generator emits these declarations
// itself and this file has to be deleted. The prototype of the C symbol is in
// rustpushgo.h. There is no API checksum check for this method.

// #include <rustpushgo.h>
import "C"

import (
	"io"
	"unsafe"
)

// WrappedRecoverableZonePage is one page of the change feed of a recycle-bin
// zone. MessageRecordNames[i] is the CloudKit record name of MessageEntries[i].
type WrappedRecoverableZonePage struct {
	ContinuationToken  *string
	Status             int32
	Done               bool
	Chats              []WrappedCloudSyncChat
	MessageEntries     []string
	MessageRecordNames []string
	RemovedRecordNames []string
}

func (r *WrappedRecoverableZonePage) Destroy() {
	FfiDestroyerOptionalString{}.Destroy(r.ContinuationToken)
	FfiDestroyerInt32{}.Destroy(r.Status)
	FfiDestroyerBool{}.Destroy(r.Done)
	FfiDestroyerSequenceTypeWrappedCloudSyncChat{}.Destroy(r.Chats)
	FfiDestroyerSequenceString{}.Destroy(r.MessageEntries)
	FfiDestroyerSequenceString{}.Destroy(r.MessageRecordNames)
	FfiDestroyerSequenceString{}.Destroy(r.RemovedRecordNames)
}

type FfiConverterTypeWrappedRecoverableZonePage struct{}

var FfiConverterTypeWrappedRecoverableZonePageINSTANCE = FfiConverterTypeWrappedRecoverableZonePage{}

func (c FfiConverterTypeWrappedRecoverableZonePage) Lift(rb RustBufferI) WrappedRecoverableZonePage {
	return LiftFromRustBuffer[WrappedRecoverableZonePage](c, rb)
}

// Read decodes the fields in the order the Rust record declares them.
func (c FfiConverterTypeWrappedRecoverableZonePage) Read(reader io.Reader) WrappedRecoverableZonePage {
	return WrappedRecoverableZonePage{
		FfiConverterOptionalStringINSTANCE.Read(reader),
		FfiConverterInt32INSTANCE.Read(reader),
		FfiConverterBoolINSTANCE.Read(reader),
		FfiConverterSequenceTypeWrappedCloudSyncChatINSTANCE.Read(reader),
		FfiConverterSequenceStringINSTANCE.Read(reader),
		FfiConverterSequenceStringINSTANCE.Read(reader),
		FfiConverterSequenceStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterTypeWrappedRecoverableZonePage) Lower(value WrappedRecoverableZonePage) RustBuffer {
	return LowerIntoRustBuffer[WrappedRecoverableZonePage](c, value)
}

func (c FfiConverterTypeWrappedRecoverableZonePage) Write(writer io.Writer, value WrappedRecoverableZonePage) {
	FfiConverterOptionalStringINSTANCE.Write(writer, value.ContinuationToken)
	FfiConverterInt32INSTANCE.Write(writer, value.Status)
	FfiConverterBoolINSTANCE.Write(writer, value.Done)
	FfiConverterSequenceTypeWrappedCloudSyncChatINSTANCE.Write(writer, value.Chats)
	FfiConverterSequenceStringINSTANCE.Write(writer, value.MessageEntries)
	FfiConverterSequenceStringINSTANCE.Write(writer, value.MessageRecordNames)
	FfiConverterSequenceStringINSTANCE.Write(writer, value.RemovedRecordNames)
}

// SyncRecoverableZone fetches one page of a recycle-bin zone's change feed,
// starting at continuationToken (nil for the start of the feed).
func (_self *Client) SyncRecoverableZone(zoneName string, continuationToken *string) (WrappedRecoverableZonePage, error) {
	_pointer := _self.ffiObject.incrementPointer("*Client")
	defer _self.ffiObject.decrementPointer()
	return uniffiRustCallAsyncWithErrorAndResult(
		FfiConverterTypeWrappedError{}, func(status *C.RustCallStatus) *C.void {
			// rustFutureFunc
			return (*C.void)(C.uniffi_rustpushgo_fn_method_client_sync_recoverable_zone(
				_pointer, rustBufferToC(FfiConverterStringINSTANCE.Lower(zoneName)), rustBufferToC(FfiConverterOptionalStringINSTANCE.Lower(continuationToken)),
				status,
			))
		},
		func(handle *C.void, ptr unsafe.Pointer, status *C.RustCallStatus) {
			// pollFunc
			C.ffi_rustpushgo_rust_future_poll_rust_buffer(unsafe.Pointer(handle), ptr, status)
		},
		func(handle *C.void, status *C.RustCallStatus) RustBufferI {
			// completeFunc
			return rustBufferFromC(C.ffi_rustpushgo_rust_future_complete_rust_buffer(unsafe.Pointer(handle), status))
		},
		FfiConverterTypeWrappedRecoverableZonePageINSTANCE.Lift, func(rustFuture *C.void, status *C.RustCallStatus) {
			// freeFunc
			C.ffi_rustpushgo_rust_future_free_rust_buffer(unsafe.Pointer(rustFuture), status)
		})
}
