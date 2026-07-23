// Copyright (C) 2026 The Syncdesic Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package model

import (
	"fmt"
	"testing"

	"github.com/syncthing/syncthing/lib/protocol"
)

// setupBenchModel creates a testModel + sendReceiveFolder ready for benchmarking
// processNeeded / processNeededByHash with n files in the database.
func setupBenchModel(t testing.TB, n int) (*testModel, *sendReceiveFolder, *fakeConnection) {
	m, f := setupSendReceiveFolder(t)
	conn := addFakeConn(m, device1, f.ID)

	// Use the folder's filesystem (f.mtimefs) to create files — not os.WriteFile
	for i := range n {
		name := fmt.Sprintf("benchfile_%d", i)
		writeFile(t, f.mtimefs, name, []byte(name))
	}
	must(t, f.scanSubdirs(t.Context(), nil))

	for i := range n {
		name := fmt.Sprintf("benchfile_%d", i)
		_, ok := m.testCurrentFolderFile(f.ID, name)
		if !ok {
			t.Fatalf("file %s not in database after scan", name)
		}
	}

	return m, f, conn
}

// genRemoteIndexStr generates n remote FileInfo entries for files benchfile_0..benchfile_n-1.
// Each entry has a bumped version and new blocks.
func genRemoteIndexStr(t testing.TB, m *testModel, f *sendReceiveFolder, n int) []protocol.FileInfo {
	t.Helper()

	block := protocol.BlockInfo{
		Offset: 0,
		Size:   128,
		Hash:   []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32},
	}
	blocks := []protocol.BlockInfo{block}
	blocksHash := protocol.BlocksHash(blocks)

	files := make([]protocol.FileInfo, n)
	for i := range n {
		name := fmt.Sprintf("benchfile_%d", i)
		cur, ok := m.testCurrentFolderFile(f.ID, name)
		if !ok {
			t.Fatalf("file %s not in database", name)
		}
		remote := cur
		remote.Version = cur.Version.Update(device1.Short())
		remote.Size = 128
		remote.Blocks = blocks
		remote.BlocksHash = blocksHash
		remote.ModifiedS = cur.ModifiedS + 1
		files[i] = remote
	}
	return files
}

// BenchmarkProcessNeededX vs BenchmarkProcessNeededByHashX compare the two implementations
// under identical conditions. Each iteration injects a fresh remote index so that
// the files become "needed" before processNeeded is called.

func BenchmarkProcessNeeded10(b *testing.B)  { benchmarkProcessNeeded(b, 10) }
func BenchmarkProcessNeeded100(b *testing.B) { benchmarkProcessNeeded(b, 100) }

func BenchmarkProcessNeededByHash10(b *testing.B)  { benchmarkProcessNeededByHash(b, 10) }
func BenchmarkProcessNeededByHash100(b *testing.B) { benchmarkProcessNeededByHash(b, 100) }

func benchmarkProcessNeeded(b *testing.B, n int) {
	m, f, conn := setupBenchModel(b, n)

	remoteFiles := genRemoteIndexStr(b, m, f, n)

	dbUpdateChan := make(chan dbUpdateJob, n*10)
	copyChan := make(chan copyBlocksState, n*10)
	scanChan := make(chan string, n)

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		must(b, m.Index(conn, &protocol.Index{Folder: f.ID, Files: remoteFiles}))

		_, _, err := f.processNeeded(b.Context(), dbUpdateChan, copyChan, scanChan)
		if err != nil {
			b.Fatal(err)
		}

		for len(copyChan) > 0 {
			<-copyChan
		}
		for len(dbUpdateChan) > 0 {
			<-dbUpdateChan
		}
	}

	b.ReportAllocs()
}

func benchmarkProcessNeededByHash(b *testing.B, n int) {
	m, f, conn := setupBenchModel(b, n)

	remoteFiles := genRemoteIndexStr(b, m, f, n)

	dbUpdateChan := make(chan dbUpdateJob, n*10)
	copyChan := make(chan copyBlocksState, n*10)
	scanChan := make(chan string, n)

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		must(b, m.Index(conn, &protocol.Index{Folder: f.ID, Files: remoteFiles}))

		_, _, err := f.processNeededByHash(b.Context(), dbUpdateChan, copyChan, scanChan)
		if err != nil {
			b.Fatal(err)
		}

		for len(copyChan) > 0 {
			<-copyChan
		}
		for len(dbUpdateChan) > 0 {
			<-dbUpdateChan
		}
	}

	b.ReportAllocs()
}
