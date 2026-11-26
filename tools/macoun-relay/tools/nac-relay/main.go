// nac-relay: Runs on a Mac and serves NAC validation data over HTTPS with
// bearer-token auth. The Linux bridge calls this instead of running the
// x86_64 NAC emulator locally.
//
// On first run, a self-signed TLS certificate and random bearer token are
// generated and stored in ~/Library/Application Support/nac-relay/.
// extract-key reads relay-info.json from the same directory and embeds
// the token + cert fingerprint into the hardware key.
//
// Usage:
//   go run tools/nac-relay/main.go [-port 5001] [-addr 0.0.0.0]
//
// Endpoints (all require Authorization: Bearer <token> except /health):
//   POST /validation-data → base64-encoded validation data
//   GET  /anisette        → JSON of Apple anisette header name → value (macoun-relay)
//   GET  /health          → JSON status, no secrets (no auth required; macoun-relay)
//
// Modified 2026-11-25 for macoun-relay (changes marked "macoun-relay:"):
// added /anisette and a JSON /health (macoun.go, macoun_ext.m), -log/-log-max-mb/
// -stderr-log for a size-capped log file, the listener is bound before the NAC
// self-test, a failed self-test is reported by /health instead of exiting, and the
// bearer token is no longer logged (auth.go). Original: corten-matrix e2adc79d92,
// AGPL-3.0 (see LICENSE).
package main

/*
#cgo CFLAGS: -x objective-c -DNAC_NO_MAIN -fobjc-arc
#cgo LDFLAGS: -framework Foundation

// Inline the validation_data.m source
#include "../../nac-validation/src/validation_data.m"
*/
import "C"

import (
	"encoding/base64"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"sync"
	"time"
	"unsafe"
)

var nacMu sync.Mutex // serialize NAC calls (framework may not be thread-safe)

// macoun-relay: every real NAC run is recorded for /health.
func generateValidationData() ([]byte, error) {
	data, err := generateValidationDataRaw()
	recordNAC(err)
	return data, err
}

func generateValidationDataRaw() ([]byte, error) {
	nacMu.Lock()
	defer nacMu.Unlock()

	var buf *C.uint8_t
	var bufLen C.size_t
	var errBuf *C.char

	result := C.nac_generate_validation_data(&buf, &bufLen, &errBuf)
	if result != 0 {
		errMsg := "unknown error"
		if errBuf != nil {
			errMsg = C.GoString(errBuf)
			C.free(unsafe.Pointer(errBuf))
		}
		return nil, fmt.Errorf("NAC error %d: %s", result, errMsg)
	}

	data := C.GoBytes(unsafe.Pointer(buf), C.int(bufLen))
	C.free(unsafe.Pointer(buf))
	return data, nil
}

func main() {
	if runtime.GOOS != "darwin" {
		fmt.Fprintln(os.Stderr, "nac-relay must run on macOS")
		os.Exit(1)
	}

	addr := flag.String("addr", "0.0.0.0", "Address to bind to")
	port := flag.Int("port", 5001, "Port to listen on")
	setup := flag.Bool("setup", false, "Install .app bundle and LaunchAgent, then start service")
	// macoun-relay: log file with a size cap (launchd does not rotate its own log paths).
	logPath := flag.String("log", "", "Write the log here, rotated at -log-max-mb (default: stderr)")
	logMaxMB := flag.Int("log-max-mb", 5, "Rotate -log past this size; keeps 3 old files")
	stderrLog := flag.String("stderr-log", "", "launchd StandardErrorPath to trim at startup if over 1 MB")
	flag.Parse()

	if *setup {
		runSetup()
		return
	}

	if err := setupLogging(*logPath, *logMaxMB, *stderrLog); err != nil {
		log.Fatalf("Failed to open log: %v", err)
	}

	http.HandleFunc("/validation-data", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		start := time.Now()
		data, err := generateValidationData()
		if err != nil {
			log.Printf("ERROR: NAC generation failed: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		b64 := base64.StdEncoding.EncodeToString(data)
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(b64))
		log.Printf("Served %d bytes of validation data in %v (from %s)",
			len(data), time.Since(start), r.RemoteAddr)
	})

	// macoun-relay: JSON /health and the new /anisette (macoun.go).
	http.HandleFunc("/health", handleHealth)
	http.HandleFunc("/anisette", handleAnisette)

	// Set up TLS + bearer token auth
	tlsConfig, token, err := ensureRelayAuth()
	if err != nil {
		log.Fatalf("Failed to initialize TLS/auth: %v", err)
	}

	// macoun-relay: bind first, so a restart loop while the Tailscale address is
	// not up yet (early boot) exits here without sending NAC traffic to Apple.
	listenAddr := fmt.Sprintf("%s:%d", *addr, *port)
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", listenAddr, err)
	}

	// Test that NAC works on startup. macoun-relay: a failure is recorded for
	// /health and the relay keeps serving, instead of exiting into a launchd loop.
	log.Println("Testing NAC validation data generation...")
	start := time.Now()
	vd, err := generateValidationData()
	if err != nil {
		log.Printf("ERROR: NAC test failed: %v", err)
	} else {
		log.Printf("NAC test OK: %d bytes in %v", len(vd), time.Since(start))
	}
	if h, err := anisetteHeaders(); err != nil {
		log.Printf("ERROR: anisette test failed: %v", err)
	} else {
		log.Printf("Anisette test OK: %d headers", len(h))
	}

	log.Printf("NAC relay listening on %s (HTTPS)", listenAddr)

	// Print helpful info
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
				log.Printf("  → Bridge relay URL: https://%s:%d/validation-data", ipnet.IP, *port)
			}
		}
	}
	log.Println("Use -relay <url> when running extract-key to embed this URL in the hardware key.")

	server := &http.Server{
		Addr:      listenAddr,
		Handler:   authMiddleware(token, http.DefaultServeMux),
		TLSConfig: tlsConfig,
	}
	log.Fatal(server.ServeTLS(ln, "", ""))
}
