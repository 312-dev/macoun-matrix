package connector

import (
	"context"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/util/dbutil"
)

// TestReadOnlyPoolRouting proves the SQLite read-only pool that
// ensureSQLiteReadPool configures actually opens and routes reads to it. If it
// silently failed to open, readOnlyTxn would be a no-op (no concurrency win);
// if read-only connections errored at BeginTx, every scrub enumeration would
// fail and the privacy scrubber would stop. Both are caught here.
//
// It opens a real SQLite database the exact way the bridge does
// (dbutil.NewFromConfig with sqlite3-fk-wal and a ro_pool) and asserts:
//   - the read-only pool opened (ReadOnlyDB != nil),
//   - a read-only transaction SELECT succeeds (the _query_only ConnectHook does
//     not reject reads), and
//   - a write inside a read-only transaction fails (so reads really do route to
//     the _query_only pool, not back to the write pool).
func TestReadOnlyPoolRouting(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ro_pool_test.db")
	cfg := dbutil.Config{
		PoolConfig: dbutil.PoolConfig{
			Type:         "sqlite3-fk-wal",
			URI:          "file:" + dbPath + "?_txlock=immediate",
			MaxOpenConns: 1,
			MaxIdleConns: 1,
		},
		ReadOnlyPool: dbutil.PoolConfig{
			Type:         "sqlite3-fk-wal",
			MaxOpenConns: 4,
			MaxIdleConns: 2,
		},
	}
	db, err := dbutil.NewFromConfig("corten-test", cfg, nil)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if db.ReadOnlyDB == nil {
		t.Fatal("ReadOnlyDB is nil: ro_pool did not open, so readOnlyTxn routing is a silent no-op")
	}

	ctx := context.Background()
	if _, err := db.Exec(ctx, `CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO t (id, v) VALUES ($1, $2)`, 1, "hello"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// A read-only transaction SELECT must succeed.
	var got string
	err = db.DoTxn(ctx, &dbutil.TxnOptions{ReadOnly: true}, func(ctx context.Context) error {
		return db.QueryRow(ctx, `SELECT v FROM t WHERE id=$1`, 1).Scan(&got)
	})
	if err != nil {
		t.Fatalf("read-only SELECT failed (ro pool rejects reads?): %v", err)
	}
	if got != "hello" {
		t.Fatalf("read-only SELECT returned %q, want %q", got, "hello")
	}

	// A write inside a read-only transaction must fail — that is how we know the
	// query routed to the _query_only read pool rather than the write pool.
	err = db.DoTxn(ctx, &dbutil.TxnOptions{ReadOnly: true}, func(ctx context.Context) error {
		_, werr := db.Exec(ctx, `INSERT INTO t (id, v) VALUES ($1, $2)`, 2, "world")
		return werr
	})
	if err == nil {
		t.Fatal("write inside a read-only txn SUCCEEDED: reads are not routing to the _query_only pool")
	}
}
