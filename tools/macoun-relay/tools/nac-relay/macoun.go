// macoun.go - GET /anisette, GET /health and a size-capped log file for nac-relay.
//
// Added 2026-11-25 (macoun-relay). Not part of upstream corten-matrix.
// Licensed under the GNU AGPL v3 or later, like the rest of this tree (see LICENSE).
//
// /anisette (bearer token, like /validation-data) returns this Mac's Apple anisette
// headers as a JSON object of header name to value, from AOSKit (macoun_ext.m).
//
// /health (no token, as upstream) returns JSON with no secrets:
//   - nac.framework_ok: an offline check that AAAbsintheContext still has the
//     selector shape validation_data.m needs (catches a macOS update breaking it).
//   - nac.last_success / last_error: the most recent real NAC run. NAC talks to
//     Apple, so /health does not run it on every probe; it reruns it only when the
//     last run failed and the previous retry is at least nacRetryAfter old.
//   - anisette.ok / header_count: a live AOSKit call (local, no Apple traffic),
//     reused for anisetteHealthTTL so a busy prober cannot hammer akd.
// The status code is 200 when everything is ok, 503 otherwise.
package main

/*
#include <stdlib.h>
#include "macoun_ext.h"
*/
import "C"

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	nacRetryAfter     = 10 * time.Minute
	anisetteHealthTTL = 30 * time.Second
)

var anisetteMu sync.Mutex // serialize AOSKit calls, like nacMu for NAC

func anisetteHeaders() (map[string]string, error) {
	anisetteMu.Lock()
	defer anisetteMu.Unlock()

	var out, errBuf *C.char
	if rc := C.macoun_anisette_json(&out, &errBuf); rc != 0 {
		msg := "unknown error"
		if errBuf != nil {
			msg = C.GoString(errBuf)
			C.free(unsafe.Pointer(errBuf))
		}
		return nil, fmt.Errorf("anisette error %d: %s", int(rc), msg)
	}
	raw := C.GoString(out)
	C.free(unsafe.Pointer(out))

	headers := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &headers); err != nil {
		return nil, fmt.Errorf("anisette JSON: %w", err)
	}
	return headers, nil
}

func nacProbe() error {
	var errBuf *C.char
	if rc := C.macoun_nac_probe(&errBuf); rc != 0 {
		msg := "unknown error"
		if errBuf != nil {
			msg = C.GoString(errBuf)
			C.free(unsafe.Pointer(errBuf))
		}
		return fmt.Errorf("NAC probe %d: %s", int(rc), msg)
	}
	return nil
}

// nacState records the outcome of real NAC runs (startup self-test, /validation-data,
// and /health retries) for /health to report.
var nacState struct {
	sync.Mutex
	lastSuccess time.Time
	lastAttempt time.Time
	lastErrAt   time.Time
	lastErr     string
}

func recordNAC(err error) {
	nacState.Lock()
	defer nacState.Unlock()
	now := time.Now()
	nacState.lastAttempt = now
	if err == nil {
		nacState.lastSuccess = now
		nacState.lastErr = ""
		return
	}
	nacState.lastErrAt = now
	nacState.lastErr = err.Error()
}

var anisetteCache struct {
	sync.Mutex
	checkedAt time.Time
	count     int
	err       error
}

func anisetteHealth() (int, time.Time, error) {
	anisetteCache.Lock()
	defer anisetteCache.Unlock()
	if time.Since(anisetteCache.checkedAt) >= anisetteHealthTTL {
		h, err := anisetteHeaders()
		anisetteCache.checkedAt, anisetteCache.count, anisetteCache.err = time.Now(), len(h), err
		if err != nil {
			log.Printf("ERROR: anisette health check failed: %v", err)
		}
	}
	return anisetteCache.count, anisetteCache.checkedAt, anisetteCache.err
}

func timeOrNil(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

func handleAnisette(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	start := time.Now()
	headers, err := anisetteHeaders()
	if err != nil {
		log.Printf("ERROR: anisette failed: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(headers)
	log.Printf("Served %d anisette headers in %v (from %s)", len(headers), time.Since(start), r.RemoteAddr)
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	probeErr := nacProbe()

	nacState.Lock()
	retry := nacState.lastErr != "" && time.Since(nacState.lastAttempt) >= nacRetryAfter
	nacState.Unlock()
	if retry && probeErr == nil {
		_, err := generateValidationData()
		if err != nil {
			log.Printf("ERROR: NAC retry from /health failed: %v", err)
		} else {
			log.Printf("NAC retry from /health OK")
		}
	}

	nacState.Lock()
	nacOK := probeErr == nil && !nacState.lastSuccess.IsZero() && nacState.lastErr == ""
	nac := map[string]any{
		"ok":           nacOK,
		"framework_ok": probeErr == nil,
		"last_success": timeOrNil(nacState.lastSuccess),
		"last_attempt": timeOrNil(nacState.lastAttempt),
	}
	if probeErr != nil {
		nac["framework_error"] = probeErr.Error()
	}
	if nacState.lastErr != "" {
		nac["last_error"] = nacState.lastErr
		nac["last_error_at"] = timeOrNil(nacState.lastErrAt)
	}
	nacState.Unlock()

	count, checkedAt, aniErr := anisetteHealth()
	ani := map[string]any{"ok": aniErr == nil, "header_count": count, "checked_at": timeOrNil(checkedAt)}
	if aniErr != nil {
		ani["error"] = aniErr.Error()
	}

	osBuild, _ := syscall.Sysctl("kern.osversion")
	body := map[string]any{
		"ok":       nacOK && aniErr == nil,
		"nac":      nac,
		"anisette": ani,
		"os_build": osBuild,
		"time":     time.Now().UTC().Format(time.RFC3339),
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if !(nacOK && aniErr == nil) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	json.NewEncoder(w).Encode(body)
}

// cappedLog is an io.Writer for a log file that rotates to path.1 .. path.<keep>
// once the file passes max bytes. launchd does not rotate StandardOutPath.
type cappedLog struct {
	mu   sync.Mutex
	path string
	max  int64
	keep int
	f    *os.File
	size int64
}

func openCappedLog(path string, max int64, keep int) (*cappedLog, error) {
	l := &cappedLog{path: path, max: max, keep: keep}
	return l, l.open()
}

func (l *cappedLog) open() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.f, l.size = f, st.Size()
	return nil
}

func (l *cappedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.size+int64(len(p)) > l.max {
		l.f.Close()
		rotateFile(l.path, l.keep)
		if err := l.open(); err != nil {
			return 0, err
		}
	}
	n, err := l.f.Write(p)
	l.size += int64(n)
	return n, err
}

func rotateFile(path string, keep int) {
	for i := keep - 1; i >= 1; i-- {
		os.Rename(fmt.Sprintf("%s.%d", path, i), fmt.Sprintf("%s.%d", path, i+1))
	}
	os.Rename(path, path+".1")
}

// trimAtStart rotates a file once at startup if it is over max bytes. It is for
// launchd's StandardErrorPath, which only receives Go runtime crashes and framework
// noise once the log package writes to cappedLog.
func trimAtStart(path string, max int64, keep int) {
	if st, err := os.Stat(path); err == nil && st.Size() > max {
		rotateFile(path, keep)
	}
}

// setupLogging points the log package at a capped file when path is non-empty.
func setupLogging(path string, maxMB int, stderrPath string) error {
	if stderrPath != "" {
		trimAtStart(stderrPath, 1<<20, 2)
	}
	if path == "" {
		return nil
	}
	if maxMB < 1 {
		return errors.New("-log-max-mb must be at least 1")
	}
	l, err := openCappedLog(path, int64(maxMB)<<20, 3)
	if err != nil {
		return err
	}
	log.SetOutput(io.Writer(l))
	return nil
}
