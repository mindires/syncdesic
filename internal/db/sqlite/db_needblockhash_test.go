package sqlite

import (
	"slices"
	"testing"

	"github.com/syncthing/syncthing/internal/db"
	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/protocol"
)

func TestNeedBlockHashes(t *testing.T) {
	t.Parallel()

	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
	})

	// Some local files
	var v protocol.Vector
	files := []protocol.FileInfo{
		genFile("test1", 1, 0),
		genFile("test2", 2, 0),
		genFile("test3", 3, 0), // will share BlocksHash with test2 after we clone blocks
	}
	// Make test3 share test2's BlocksHash
	files[2].Blocks = files[1].Blocks

	files[1].Version = v.Update(1)
	files[2].Version = v.Update(1)
	files[0].Version = v.Update(2)

	err = database.Update(folderID, protocol.LocalDeviceID, files)
	if err != nil {
		t.Fatal(err)
	}

	// Remote files that are newer — these are our "needed" files
	newerV := v.Update(42)
	remote := []protocol.FileInfo{
		genFile("test2", 2, 100), // needed, distinct BlocksHash
		genFile("test3", 2, 101), // needed, same BlocksHash as test2
		genFile("test4", 1, 102), // needed, yet another BlocksHash
	}
	// test2 and test3 share the same BlocksHash
	remote[1].Blocks = remote[0].Blocks
	remote[0].Version = newerV
	remote[1].Version = newerV
	remote[2].Version = newerV
	err = database.Update(folderID, protocol.DeviceID{42}, remote)
	if err != nil {
		t.Fatal(err)
	}

	// Call AllNeededBlockHashes
	hashes, errFn := database.AllNeededBlockHashes(folderID, protocol.LocalDeviceID, config.PullOrderAlphabetic, 0, 0)
	got := mustCollect[db.NeededBlockHash](t)(hashes, errFn)

	// We expect 2 distinct blocklist hashes:
	//   test2 & test3 share the same BlocksHash
	//   test4 has a different BlocksHash
	if len(got) != 2 {
		t.Fatalf("expected 2 distinct blocklist hashes, got %d: %v", len(got), got)
	}

	// Verify they all have non-nil hash and at least one name
	for _, h := range got {
		if len(h.Hash) == 0 {
			t.Errorf("hash entry with empty Hash: %+v", h)
		}
		if len(h.Names) == 0 {
			t.Errorf("hash entry with empty Names: %+v", h)
		}
	}

	// Sort by first name for deterministic check
	slices.SortFunc(got, func(a, b db.NeededBlockHash) int {
		if a.Names[0] < b.Names[0] {
			return -1
		}
		if a.Names[0] > b.Names[0] {
			return 1
		}
		return 0
	})

	// First entry: test2+test3 (same BlocksHash)
	if got[0].Names[0] != "test2" || got[0].Names[1] != "test3" {
		t.Errorf("expected [test2, test3], got %v", got[0].Names)
	}

	// Second entry: test4
	if got[1].Names[0] != "test4" {
		t.Errorf("expected [test4], got %v", got[1].Names)
	}
}

func TestNeedBlockHashesNoFolders(t *testing.T) {
	t.Parallel()

	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
	})

	hashes, errFn := database.AllNeededBlockHashes("nonexistent", protocol.LocalDeviceID, config.PullOrderAlphabetic, 0, 0)
	got := mustCollect[db.NeededBlockHash](t)(hashes, errFn)
	if len(got) != 0 {
		t.Errorf("expected no hashes for nonexistent folder, got %d", len(got))
	}
}
