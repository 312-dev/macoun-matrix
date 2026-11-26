// macoun-matrix - A Matrix-iMessage puppeting bridge (fork of corten-matrix).
// SPDX-License-Identifier: MPL-2.0

package rustpushgo

// Hand-written binding for provider_outage_mask (src/lib.rs), in the shape
// uniffi-bindgen-go produces and kept out of rustpushgo.go for the same reason
// as macoun_recoverable.go: delete this file once the bindings are regenerated
// from a library that exports the function. The prototype of the C symbol is
// in rustpushgo.h. There is no API checksum check for this function.

// #include <rustpushgo.h>
import "C"

const (
	// ProviderValidationData is the ProviderOutageMask bit for validation-data
	// (NAC) calls.
	ProviderValidationData uint64 = 1
	// ProviderAnisette is the ProviderOutageMask bit for anisette calls.
	ProviderAnisette uint64 = 2
)

// ProviderOutageMask reports which calls to the validation-data and anisette
// provider (the Mac relay or a compatible service) failed at their latest try,
// after retries. 0 means none.
func ProviderOutageMask() uint64 {
	return FfiConverterUint64INSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint64_t {
		return C.uniffi_rustpushgo_fn_func_provider_outage_mask(_uniffiStatus)
	}))
}
