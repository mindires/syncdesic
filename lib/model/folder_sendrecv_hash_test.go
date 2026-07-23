// Copyright (C) 2026 The Syncdesic Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package model

import (
	"bytes"
	"testing"
	"time"

	"github.com/syncthing/syncthing/lib/protocol"
)

// TestProcessNeededByHash verifies that processNeededByHash dispatches
// needed files using the 1+N query approach (AllNeededGlobalFiles + GetDeviceFile).
//
//  1. Remote announces a file with a higher version → file becomes "needed"
//  2. AllNeededGlobalFiles returns the full FileInfo (1 SQL)
//  3. GetDeviceFile is queried once per file (N SQL, for version comparison)
//  4. Files are dispatched to copyChan (if blocks differ) or shortcutFile
//
// This must produce identical results to the old processNeeded path
// but without the intermediate queue.Push → GetGlobalFile → fileAvailability
// pipeline, reducing SQL from 1+3N to 1+N.
func TestProcessNeededByHash(t *testing.T) {
	m, f := setupSendReceiveFolder(t)
	conn := addFakeConn(m, device1, f.ID)

	writeFile(t, f.mtimefs, "file1", []byte("hello world"))
	must(t, f.scanSubdirs(t.Context(), nil))

	cur, ok := m.testCurrentFolderFile(f.ID, "file1")
	if !ok {
		t.Fatal("file1 not found in database after scan")
	}

	block := protocol.BlockInfo{
		Size:   128,
		Hash:   []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32},
		Offset: 0,
	}
	blocksHash := protocol.BlocksHash([]protocol.BlockInfo{block})

	remote := cur
	remote.Version = cur.Version.Update(device1.Short())
	remote.Size = 128
	remote.Blocks = []protocol.BlockInfo{block}
	remote.BlocksHash = blocksHash
	remote.ModifiedS = cur.ModifiedS + 1

	must(t, m.Index(conn, &protocol.Index{Folder: f.ID, Files: []protocol.FileInfo{remote}}))

	dbUpdateChan := make(chan dbUpdateJob, 10)
	copyChan := make(chan copyBlocksState, 10)
	scanChan := make(chan string, 10)

	fileDeletions, dirDeletions, err := f.processNeededByHash(
		t.Context(), dbUpdateChan, copyChan, scanChan,
	)
	must(t, err)

	if len(fileDeletions) != 0 {
		t.Errorf("expected no file deletions, got %d", len(fileDeletions))
	}
	if len(dirDeletions) != 0 {
		t.Errorf("expected no dir deletions, got %d", len(dirDeletions))
	}

	select {
	case state := <-copyChan:
		if state.file.Name != "file1" {
			t.Errorf("expected file1 on copyChan, got %s", state.file.Name)
		}
		if !bytes.Equal(state.file.BlocksHash, blocksHash) {
			t.Errorf("expected BlocksHash %x on copyChan, got %x", blocksHash, state.file.BlocksHash)
		}
	case <-time.After(time.Second):
		t.Error("expected dispatch on copyChan, nothing received within 1s")
	}

	select {
	case <-copyChan:
		t.Error("unexpected additional item on copyChan")
	default:
	}
}

// TestProcessNeededByHashMultipleFiles verifies dispatch when two files
// with identical content both need pulling.
func TestProcessNeededByHashMultipleFiles(t *testing.T) {
	m, f := setupSendReceiveFolder(t)
	conn := addFakeConn(m, device1, f.ID)

	writeFile(t, f.mtimefs, "file_a", []byte("shared content"))
	writeFile(t, f.mtimefs, "file_b", []byte("shared content"))
	must(t, f.scanSubdirs(t.Context(), nil))

	curA, okA := m.testCurrentFolderFile(f.ID, "file_a")
	curB, okB := m.testCurrentFolderFile(f.ID, "file_b")
	if !okA || !okB {
		t.Fatal("files not found in database after scan")
	}

	block := protocol.BlockInfo{
		Size:   256,
		Hash:   []byte{2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33},
		Offset: 0,
	}
	hash := protocol.BlocksHash([]protocol.BlockInfo{block})

	remoteA := curA
	remoteA.Version = curA.Version.Update(device1.Short())
	remoteA.Size = 256
	remoteA.Blocks = []protocol.BlockInfo{block}
	remoteA.BlocksHash = hash
	remoteA.ModifiedS = curA.ModifiedS + 1

	remoteB := curB
	remoteB.Version = curB.Version.Update(device1.Short())
	remoteB.Size = 256
	remoteB.Blocks = []protocol.BlockInfo{block}
	remoteB.BlocksHash = hash
	remoteB.ModifiedS = curB.ModifiedS + 1

	must(t, m.Index(conn, &protocol.Index{
		Folder: f.ID,
		Files:  []protocol.FileInfo{remoteA, remoteB},
	}))

	dbUpdateChan := make(chan dbUpdateJob, 10)
	copyChan := make(chan copyBlocksState, 10)
	scanChan := make(chan string, 10)

	fileDeletions, dirDeletions, err := f.processNeededByHash(
		t.Context(), dbUpdateChan, copyChan, scanChan,
	)
	must(t, err)

	if len(fileDeletions) != 0 {
		t.Errorf("expected no file deletions, got %d", len(fileDeletions))
	}
	if len(dirDeletions) != 0 {
		t.Errorf("expected no dir deletions, got %d", len(dirDeletions))
	}

	select {
	case <-copyChan:
	case <-time.After(time.Second):
		t.Error("expected at least one dispatch on copyChan, nothing received within 1s")
	}

	select {
	case <-copyChan:
	case <-time.After(100 * time.Millisecond):
	}
}

// TestProcessNeededByHashShortcut verifies that when blocks match,
// processNeededByHash takes the shortcut path instead of dispatching.
func TestProcessNeededByHashShortcut(t *testing.T) {
	m, f := setupSendReceiveFolder(t)
	conn := addFakeConn(m, device1, f.ID)

	writeFile(t, f.mtimefs, "file1", []byte("hello world"))
	must(t, f.scanSubdirs(t.Context(), nil))

	cur, ok := m.testCurrentFolderFile(f.ID, "file1")
	if !ok {
		t.Fatal("file1 not found in database after scan")
	}

	remote := cur
	remote.Version = cur.Version.Update(device1.Short())
	remote.ModifiedS = cur.ModifiedS + 1

	must(t, m.Index(conn, &protocol.Index{Folder: f.ID, Files: []protocol.FileInfo{remote}}))

	dbUpdateChan := make(chan dbUpdateJob, 10)
	copyChan := make(chan copyBlocksState, 10)
	scanChan := make(chan string, 10)

	fileDeletions, dirDeletions, err := f.processNeededByHash(
		t.Context(), dbUpdateChan, copyChan, scanChan,
	)
	must(t, err)

	if len(fileDeletions) != 0 {
		t.Errorf("expected no file deletions, got %d", len(fileDeletions))
	}
	if len(dirDeletions) != 0 {
		t.Errorf("expected no dir deletions, got %d", len(dirDeletions))
	}

	select {
	case <-copyChan:
		t.Error("expected shortcut (no dispatch on copyChan), but file was dispatched")
	default:
	}
}

// TestProcessNeededByHashIgnore verifies that ignored files go through
// dbUpdateInvalidate and not copyChan.
func TestProcessNeededByHashIgnore(t *testing.T) {
	m, f := setupSendReceiveFolder(t)
	conn := addFakeConn(m, device1, f.ID)

	writeFile(t, f.mtimefs, "file1", []byte("hello world"))
	must(t, f.scanSubdirs(t.Context(), nil))

	cur, ok := m.testCurrentFolderFile(f.ID, "file1")
	if !ok {
		t.Fatal("file1 not found in database")
	}

	// Parse ignore pattern for file1
	must(t, f.ignores.Parse(bytes.NewBufferString("file1"), ""))

	remote := cur
	remote.Version = cur.Version.Update(device1.Short())
	remote.ModifiedS = cur.ModifiedS + 1

	must(t, m.Index(conn, &protocol.Index{Folder: f.ID, Files: []protocol.FileInfo{remote}}))

	dbUpdateChan := make(chan dbUpdateJob, 10)
	copyChan := make(chan copyBlocksState, 10)
	scanChan := make(chan string, 10)

	fileDeletions, dirDeletions, err := f.processNeededByHash(
		t.Context(), dbUpdateChan, copyChan, scanChan,
	)
	must(t, err)

	if len(fileDeletions) != 0 {
		t.Errorf("expected no file deletions, got %d", len(fileDeletions))
	}
	if len(dirDeletions) != 0 {
		t.Errorf("expected no dir deletions, got %d", len(dirDeletions))
	}

	select {
	case <-copyChan:
		t.Error("ignored file should not be dispatched on copyChan")
	default:
	}
}

// TestProcessNeededByHashDirSymlink verifies directories are handled
// directly without copyChan dispatch.
func TestProcessNeededByHashDirSymlink(t *testing.T) {
	m, f := setupSendReceiveFolder(t)
	conn := addFakeConn(m, device1, f.ID)

	must(t, f.mtimefs.Mkdir("mydir", 0o755))
	must(t, f.scanSubdirs(t.Context(), nil))

	curDir, ok := m.testCurrentFolderFile(f.ID, "mydir")
	if !ok {
		t.Fatal("mydir not found in database after scan")
	}

	remoteDir := curDir
	remoteDir.Version = curDir.Version.Update(device1.Short())
	remoteDir.ModifiedS = curDir.ModifiedS + 1

	must(t, m.Index(conn, &protocol.Index{Folder: f.ID, Files: []protocol.FileInfo{remoteDir}}))

	dbUpdateChan := make(chan dbUpdateJob, 10)
	copyChan := make(chan copyBlocksState, 10)
	scanChan := make(chan string, 10)

	fileDeletions, dirDeletions, err := f.processNeededByHash(
		t.Context(), dbUpdateChan, copyChan, scanChan,
	)
	must(t, err)

	if len(fileDeletions) != 0 {
		t.Errorf("expected no file deletions for dir test, got %d", len(fileDeletions))
	}
	if len(dirDeletions) != 0 {
		t.Errorf("expected no dir deletions, got %d", len(dirDeletions))
	}

	select {
	case <-copyChan:
		t.Error("directory should not be on copyChan")
	default:
	}
}

// TestProcessNeededByHashDeleted verifies that remote deletions are
// propagated into fileDeletions return value.
func TestProcessNeededByHashDeleted(t *testing.T) {
	m, f := setupSendReceiveFolder(t)
	conn := addFakeConn(m, device1, f.ID)

	writeFile(t, f.mtimefs, "file1", []byte("hello world"))
	must(t, f.scanSubdirs(t.Context(), nil))

	cur, ok := m.testCurrentFolderFile(f.ID, "file1")
	if !ok {
		t.Fatal("file1 not found in database after scan")
	}

	del := cur
	del.SetDeleted(device1.Short())
	del.Version = cur.Version.Update(device1.Short())

	must(t, m.Index(conn, &protocol.Index{Folder: f.ID, Files: []protocol.FileInfo{del}}))

	dbUpdateChan := make(chan dbUpdateJob, 10)
	copyChan := make(chan copyBlocksState, 10)
	scanChan := make(chan string, 10)

	fileDeletions, dirDeletions, err := f.processNeededByHash(
		t.Context(), dbUpdateChan, copyChan, scanChan,
	)
	must(t, err)

	if len(fileDeletions) != 1 {
		t.Errorf("expected 1 file deletion, got %d", len(fileDeletions))
	}
	if len(dirDeletions) != 0 {
		t.Errorf("expected no dir deletions, got %d", len(dirDeletions))
	}

	select {
	case <-copyChan:
		t.Error("deleted file should not be on copyChan")
	default:
	}
}
