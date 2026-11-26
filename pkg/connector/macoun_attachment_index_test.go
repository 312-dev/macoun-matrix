package connector

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/lrhodin/corten-matrix/pkg/rustpushgo"
)

// fakeAttachmentZone serves an attachment zone as a change feed of 200 records
// per request and records what a sync pass asks for and registers.
type fakeAttachmentZone struct {
	records    []rustpushgo.WrappedCloudAttachmentInfo
	requests   int
	failing    bool
	registered map[string]bool
}

func newFakeAttachmentZone(n int) *fakeAttachmentZone {
	z := &fakeAttachmentZone{}
	z.add(n)
	return z
}

func (z *fakeAttachmentZone) add(n int) {
	for i := 0; i < n; i++ {
		id := len(z.records)
		key := []byte(fmt.Sprintf("ford-key-%027d", id))
		z.records = append(z.records, rustpushgo.WrappedCloudAttachmentInfo{
			Guid:       fmt.Sprintf("at_%d", id),
			RecordName: fmt.Sprintf("rec-%d", id),
			FileSize:   int64(id),
			FordKey:    &key,
		})
	}
}

func (z *fakeAttachmentZone) source() attachmentZoneSource {
	z.registered = make(map[string]bool)
	return attachmentZoneSource{
		fetchPage: func(token *string) (rustpushgo.WrappedCloudSyncAttachmentsPage, error) {
			z.requests++
			if z.failing {
				return rustpushgo.WrappedCloudSyncAttachmentsPage{}, errors.New("fake CloudKit failure")
			}
			start := 0
			if token != nil {
				start, _ = strconv.Atoi(*token)
			}
			end := start + 200
			if end > len(z.records) {
				end = len(z.records)
			}
			next := strconv.Itoa(end)
			return rustpushgo.WrappedCloudSyncAttachmentsPage{
				ContinuationToken: &next,
				Done:              end == len(z.records),
				Attachments:       z.records[start:end],
			}, nil
		},
		fetchFallback: func([]string) (rustpushgo.WrappedCloudSyncAttachmentsPage, error) {
			return rustpushgo.WrappedCloudSyncAttachmentsPage{}, nil
		},
		registerFordKey: func(key []byte) { z.registered[string(key)] = true },
		retryDelay:      func(error, int) time.Duration { return 0 },
	}
}

func attachmentIndexFixture(t *testing.T) (context.Context, *attachmentIndex) {
	t.Helper()
	index, err := newAttachmentIndex(newTestSQLiteDB(t), string(testSQLLoginID), bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return context.Background(), index
}

// TestAttachmentIndexLaterPassesReadOnlyChanges shows the redundant work and
// its cure. Without an index (how every pass ran before the index existed)
// each pass reads the whole zone; with it, a later pass (a new process: fresh
// map, empty Ford key cache) costs one request and still ends up with the full
// map and every Ford key registered.
func TestAttachmentIndexLaterPassesReadOnlyChanges(t *testing.T) {
	const total = 4000 // 20 pages
	zone := newFakeAttachmentZone(total)

	for pass := 1; pass <= 2; pass++ {
		zone.requests = 0
		attMap, _ := syncAttachmentZone(context.Background(), nil, zone.source(), zerolog.Nop())
		if zone.requests != total/200 || len(attMap) != total {
			t.Fatalf("pass %d without index: requests=%d attachments=%d, want %d and %d",
				pass, zone.requests, len(attMap), total/200, total)
		}
	}

	ctx, index := attachmentIndexFixture(t)
	zone.requests = 0
	attMap, _ := syncAttachmentZone(ctx, index, zone.source(), zerolog.Nop())
	if zone.requests != total/200 || len(attMap) != total {
		t.Fatalf("first pass with index: requests=%d attachments=%d", zone.requests, len(attMap))
	}

	zone.requests = 0
	attMap, _ = syncAttachmentZone(ctx, index, zone.source(), zerolog.Nop())
	if zone.requests != 1 {
		t.Fatalf("second pass with index: %d requests, want 1", zone.requests)
	}
	if len(attMap) != total || len(zone.registered) != total {
		t.Fatalf("second pass with index: attachments=%d ford_keys=%d, want %d of each",
			len(attMap), len(zone.registered), total)
	}
	if row := attMap["at_1234"]; row.RecordName != "rec-1234" || row.FileSize != 1234 {
		t.Fatalf("row did not survive the index round trip: %+v", row)
	}

	// New records arrive: the next pass fetches only them.
	zone.add(3)
	zone.requests = 0
	attMap, _ = syncAttachmentZone(ctx, index, zone.source(), zerolog.Nop())
	if zone.requests != 1 || len(attMap) != total+3 || len(zone.registered) != total+3 {
		t.Fatalf("pass after new records: requests=%d attachments=%d ford_keys=%d",
			zone.requests, len(attMap), len(zone.registered))
	}
}

// TestAttachmentIndexResumesInterruptedPass: a pass that dies half way leaves
// its pages and position behind, and the next pass continues instead of
// producing a map that lacks the records before the interruption.
func TestAttachmentIndexResumesInterruptedPass(t *testing.T) {
	ctx, index := attachmentIndexFixture(t)
	zone := newFakeAttachmentZone(1000)
	src := zone.source()
	if err := index.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	// Two pages stored, then the process dies.
	var token *string
	for i := 0; i < 2; i++ {
		page, _ := src.fetchPage(token)
		entries := make([]attachmentIndexEntry, 0, len(page.Attachments))
		for _, att := range page.Attachments {
			entries = append(entries, attachmentIndexEntryFromInfo(att))
		}
		if err := index.applyPage(ctx, entries, page.ContinuationToken); err != nil {
			t.Fatal(err)
		}
		token = page.ContinuationToken
	}

	zone.requests = 0
	attMap, _ := syncAttachmentZone(ctx, index, zone.source(), zerolog.Nop())
	if zone.requests != 3 || len(attMap) != 1000 || len(zone.registered) != 1000 {
		t.Fatalf("resumed pass: requests=%d attachments=%d ford_keys=%d, want 3, 1000, 1000",
			zone.requests, len(attMap), len(zone.registered))
	}
}

// TestAttachmentIndexIsEncryptedAndFailsSafe: nothing readable is stored, and
// an index that does not decrypt is dropped in favour of a full read.
func TestAttachmentIndexIsEncryptedAndFailsSafe(t *testing.T) {
	ctx, index := attachmentIndexFixture(t)
	zone := newFakeAttachmentZone(300)
	syncAttachmentZone(ctx, index, zone.source(), zerolog.Nop())

	rows, err := index.db.Query(ctx, `SELECT sealed FROM macoun_attachment_index`)
	if err != nil {
		t.Fatal(err)
	}
	stored := 0
	for rows.Next() {
		var sealed []byte
		if err = rows.Scan(&sealed); err != nil {
			t.Fatal(err)
		}
		stored++
		if bytes.Contains(sealed, []byte("ford-key-")) || bytes.Contains(sealed, []byte("at_")) {
			t.Fatal("index row contains plaintext")
		}
	}
	rows.Close()
	if stored != 300 {
		t.Fatalf("stored %d rows, want 300", stored)
	}

	// Same database, different key: as after a lost key file.
	other, err := newAttachmentIndex(index.db, index.loginID, bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	zone.requests = 0
	attMap, _ := syncAttachmentZone(ctx, other, zone.source(), zerolog.Nop())
	if zone.requests != 2 || len(attMap) != 300 || len(zone.registered) != 300 {
		t.Fatalf("pass with unreadable index: requests=%d attachments=%d ford_keys=%d, want a full read (2, 300, 300)",
			zone.requests, len(attMap), len(zone.registered))
	}
	zone.requests = 0
	if syncAttachmentZone(ctx, other, zone.source(), zerolog.Nop()); zone.requests != 1 {
		t.Fatalf("index was not rebuilt under the new key: %d requests", zone.requests)
	}
}

// TestAttachmentIndexRepeatedFailureForcesFullRead covers the stand-in for a
// rejected continuation token.
func TestAttachmentIndexRepeatedFailureForcesFullRead(t *testing.T) {
	ctx, index := attachmentIndexFixture(t)
	zone := newFakeAttachmentZone(300)
	syncAttachmentZone(ctx, index, zone.source(), zerolog.Nop())

	zone.failing = true
	for pass := 1; pass <= attachmentIndexFailureLimit; pass++ {
		attMap, _ := syncAttachmentZone(ctx, index, zone.source(), zerolog.Nop())
		if len(attMap) != 300 || len(zone.registered) != 300 {
			t.Fatalf("failing pass %d must still serve the stored index: attachments=%d ford_keys=%d",
				pass, len(attMap), len(zone.registered))
		}
		_, token, err := index.load(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if wantKept := pass < attachmentIndexFailureLimit; (token != nil) != wantKept {
			t.Fatalf("after %d failing passes: index kept=%v, want %v", pass, token != nil, wantKept)
		}
	}

	zone.failing = false
	zone.requests = 0
	if attMap, _ := syncAttachmentZone(ctx, index, zone.source(), zerolog.Nop()); zone.requests != 2 || len(attMap) != 300 {
		t.Fatalf("pass after reset: requests=%d attachments=%d, want a full read", zone.requests, len(attMap))
	}
}
