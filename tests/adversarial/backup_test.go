package adversarial

// Backup/restore verification (docs/OPERATIONS.md): a VACUUM INTO backup of
// an OPEN store (WAL mode) must contain every durable operation and restore
// as the same replica identity with identical state.

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/PrathamKapoor/ConvergeDB/internal/clock"
	"github.com/PrathamKapoor/ConvergeDB/internal/core"
	"github.com/PrathamKapoor/ConvergeDB/internal/store"
)

func TestBackupRestoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "live.db")
	backupPath := filepath.Join(dir, "backup.db")

	// Build a live store with mixed ops.
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMeta("replica_id", "backup-test"); err != nil {
		t.Fatal(err)
	}
	clk := clock.New("writer", func() int64 { return 9000 })
	for i := 0; i < 150; i++ {
		op := &core.Operation{
			DocID: "board", Kind: core.KindVoteDelta, Field: core.FieldVotes,
			Actor: "writer", Payload: core.Payload{Delta: 1},
		}
		if err := st.AppendLocal(op, clk.Now()); err != nil {
			t.Fatal(err)
		}
	}
	// Backup while OPEN (WAL active) — the operator's real scenario.
	if err := st.Backup(backupPath); err != nil {
		t.Fatalf("backup: %v", err)
	}
	wantCount, wantDigest, err := st.OpDigest()
	if err != nil {
		t.Fatal(err)
	}
	// Keep writing after the backup: the backup must NOT include these.
	for i := 0; i < 10; i++ {
		op := &core.Operation{
			DocID: "board", Kind: core.KindVoteDelta, Field: core.FieldVotes,
			Actor: "writer", Payload: core.Payload{Delta: 2},
		}
		if err := st.AppendLocal(op, clk.Now()); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()

	// Restore: the backup file IS the database.
	st2, err := store.Open(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	id, ok, err := st2.GetMeta("replica_id")
	if err != nil || !ok || id != "backup-test" {
		t.Fatalf("restored identity wrong: %q %v %v", id, ok, err)
	}
	gotCount, gotDigest, err := st2.OpDigest()
	if err != nil {
		t.Fatal(err)
	}
	if gotCount != wantCount || gotDigest != wantDigest {
		t.Fatalf("restored log mismatch: %d/%s vs %d/%s", gotCount, gotDigest, wantCount, wantDigest)
	}
	// The post-backup writes must NOT be in the backup (point-in-time semantics).
	ops, err := st2.AllOps()
	if err != nil {
		t.Fatal(err)
	}
	var votes int64
	for _, op := range ops {
		votes += op.Payload.Delta
	}
	if votes != 150 {
		t.Fatalf("restored votes = %d, want 150 (point-in-time backup)", votes)
	}
	// The restored replica continues working with correct sequence state.
	next := &core.Operation{
		DocID: "board", Kind: core.KindVoteDelta, Field: core.FieldVotes,
		Actor: "writer", Payload: core.Payload{Delta: 1},
	}
	if err := st2.AppendLocal(next, clock.HLC{Wall: 9999, Counter: 0, Actor: "writer"}); err != nil {
		t.Fatal(err)
	}
	if next.ActorSeq != 151 {
		t.Fatalf("post-restore seq = %d, want 151", next.ActorSeq)
	}
	fmt.Println("backup/restore round trip verified")
}
