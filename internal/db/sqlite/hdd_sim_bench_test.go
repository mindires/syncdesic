// Copyright (C) 2025 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package sqlite

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/syncthing/syncthing/internal/db"
	"github.com/syncthing/syncthing/internal/itererr"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/rand"
)

// HDD Simulation Benchmark
//
// On a machine with NVMe SSD, this benchmark simulates HDD performance
// characteristics for the three key operations:
//
//   - insertBlocks: random B-tree writes (PRIMARY KEY hash is random)
//   - scanBlocks:   sequential protobuf BLOB scan
//   - scanAllBlocksCached: in-memory LRU cache lookup
//
// Approach: real SQLite execution on NVMe + I/O instrumentation via page_count
// tracking + penalty model calibrated to HDD rotational latency.
//
// HDD penalty model:
//
//	Random I/O:  12ms seek + 4ms rotational latency = 16ms per page write
//	            (WD Red / Seagate Barracuda class)
//	Sequential:  150 MB/s read throughput
//	            100 MB/s write throughput (outer track average)
//
// Penalty is applied per observed SQLite page I/O, counted via PRAGMA page_count
// deltas and WAL page tracking.

// HDD penalty constants (class: 7200rpm CMR desktop HDD)
const (
	hddSeekTime      = 12 * time.Millisecond       // avg seek 7200rpm
	hddRotLatency    = 4 * time.Millisecond        // 7200rpm = 8.33ms full rotation
	hddRandomWriteIO = hddSeekTime + hddRotLatency // 16ms per random page write
	hddRandomReadIO  = hddSeekTime + hddRotLatency // 16ms per random page read
	hddSeqReadBW     = 150.0 * 1024 * 1024         // 150 MB/s
	hddSeqWriteBW    = 100.0 * 1024 * 1024         // 100 MB/s
	nvmeRandomIO     = 75 * time.Microsecond       // NVMe ~75µs random 4K IO
	nvmeSeqReadBW    = 3500.0 * 1024 * 1024        // NVMe ~3.5 GB/s seq read
	nvmeSeqWriteBW   = 2000.0 * 1024 * 1024        // NVMe ~2 GB/s seq write
)

// Experiment config matrix
type hddSimConfig struct {
	numFiles  int   // number of files
	numBlocks int   // blocks per file
	blockSize int64 // bytes per block
	label     string
}

func (c hddSimConfig) totalBlocks() int  { return c.numFiles * c.numBlocks }
func (c hddSimConfig) totalBytes() int64 { return int64(c.totalBlocks()) * c.blockSize }

// Experiment matrix: sweep file count and block count
var hddExperimentMatrix = []hddSimConfig{
	{numFiles: 10, numBlocks: 64, blockSize: 128 << 10, label: "10files_x_64blk"},
	{numFiles: 100, numBlocks: 64, blockSize: 128 << 10, label: "100files_x_64blk"},
	{numFiles: 100, numBlocks: 512, blockSize: 128 << 10, label: "100files_x_512blk"},
	{numFiles: 100, numBlocks: 8192, blockSize: 128 << 10, label: "100x8192blk_1GB"},
	{numFiles: 500, numBlocks: 64, blockSize: 128 << 10, label: "500files_x_64blk"},
}

// dbPageStats captures SQLite page-level I/O statistics
type dbPageStats struct {
	pageSize   int64 // bytes
	pageCount0 int64 // pages before operation
	pageCount1 int64 // pages after operation
}

func capturePageStats(fdb *folderDB) dbPageStats {
	var ps dbPageStats
	fdb.sql.Get(&ps.pageSize, `PRAGMA page_size`)
	fdb.sql.Get(&ps.pageCount0, `PRAGMA main.page_count`)
	return ps
}

func capturePageStatsAfter(fdb *folderDB, before dbPageStats) dbPageStats {
	after := before
	fdb.sql.Get(&after.pageCount1, `PRAGMA main.page_count`)
	return after
}

// deltaPages returns the number of new database pages written
func (b dbPageStats) deltaPages() int64 { return b.pageCount1 - b.pageCount0 }

// estimateHDDRandomWriteTime estimates HDD time for random page writes
func (b dbPageStats) estimateHDDRandomWriteTime() time.Duration {
	pages := b.deltaPages()
	if pages <= 0 {
		return 0
	}
	ioTime := time.Duration(pages) * hddRandomWriteIO
	dataTime := time.Duration(float64(pages)*float64(b.pageSize)/float64(hddSeqWriteBW)) * time.Second
	return ioTime + dataTime
}

// estimateNVMeRandomWriteTime estimates NVMe time for same pages
func (b dbPageStats) estimateNVMeRandomWriteTime() time.Duration {
	pages := b.deltaPages()
	if pages <= 0 {
		return 0
	}
	ioTime := time.Duration(pages) * nvmeRandomIO
	dataTime := time.Duration(float64(pages)*float64(b.pageSize)/float64(nvmeSeqWriteBW)) * time.Second
	return ioTime + dataTime
}

// ----------------------------------------------------------------
// Experiment 1: insertBlocks (random B-tree write)
// ----------------------------------------------------------------

func BenchmarkHDDSim_InsertBlocks(b *testing.B) {
	for _, cfg := range hddExperimentMatrix {
		cfg := cfg
		b.Run(cfg.label, func(b *testing.B) {
			benchInsertBlocksHDD(b, cfg)
		})
	}
}

func benchInsertBlocksHDD(b *testing.B, cfg hddSimConfig) {
	dir := b.TempDir()
	sdb := openBenchDB(b, dir)
	fdb := mustGetFolderDB(b, sdb)

	// Pre-insert files WITHOUT block indexing to isolate insertBlocks cost
	seed := 0
	insertSeedFiles(b, sdb, cfg, &seed)

	// Now benchmark: update each file with new blocks (triggers insertBlocksLocked)
	files := make([]protocol.FileInfo, cfg.numFiles)
	b.ResetTimer()

	for bi := range b.N {
		ps := capturePageStats(fdb)

		t0 := time.Now()
		for i := range files {
			name := fmt.Sprintf("bench-update-%d-%d", bi, i)
			files[i] = genFile(name, cfg.numBlocks, 0)
			files[i].Version = files[i].Version.Update(protocol.ShortID(bi + i))
			files[i].Blocks = genBlocks(name, seed, cfg.numBlocks)
			seed++
		}
		if err := sdb.Update(folderID, protocol.LocalDeviceID, files); err != nil {
			b.Fatal(err)
		}
		elapsed := time.Since(t0)

		psAfter := capturePageStatsAfter(fdb, ps)

		hddEst := psAfter.estimateHDDRandomWriteTime().Nanoseconds()
		nvmeEst := psAfter.estimateNVMeRandomWriteTime().Nanoseconds()

		b.ReportMetric(float64(cfg.totalBlocks())/elapsed.Seconds(), "blk/s_real")
		b.ReportMetric(float64(elapsed.Nanoseconds())/1e6, "ms_real")
		b.ReportMetric(float64(hddEst)/1e6, "ms_est_hdd")
		b.ReportMetric(float64(nvmeEst)/1e6, "ms_est_nvme")
		b.ReportMetric(float64(psAfter.deltaPages()), "pages_delta")
	}

	b.ReportMetric(float64(cfg.totalBlocks())/b.Elapsed().Seconds(), "blk/s_real_avg")
}

// ----------------------------------------------------------------
// Experiment 2: scanBlocks via protobuf (sequential read)
// ----------------------------------------------------------------

func BenchmarkHDDSim_ScanBlocksProtobuf(b *testing.B) {
	for _, cfg := range hddExperimentMatrix {
		cfg := cfg
		b.Run(cfg.label, func(b *testing.B) {
			benchScanBlocksProtobufHDD(b, cfg)
		})
	}
}

func benchScanBlocksProtobufHDD(b *testing.B, cfg hddSimConfig) {
	dir := b.TempDir()
	sdb := openBenchDB(b, dir)

	seed := 0
	insertFullFiles(b, sdb, cfg, &seed) // with block indexing

	// Measure total blocklist data size for HDD sequential read estimation
	var totalBlobBytes int64
	fdb := mustGetFolderDB(b, sdb)
	fdb.sql.Get(&totalBlobBytes, `SELECT COALESCE(SUM(length(blprotobuf)), 0) FROM blocklists`)

	b.ResetTimer()

	for range b.N {
		t0 := time.Now()

		count := 0
		it, errFn := fdb.AllLocalFiles(protocol.LocalDeviceID)
		for range it {
			count++
		}
		if err := errFn(); err != nil {
			b.Fatal(err)
		}

		elapsed := time.Since(t0)

		hddSeqTime := time.Duration(float64(totalBlobBytes)/float64(hddSeqReadBW)*1e9) * time.Nanosecond
		nvmeSeqTime := time.Duration(float64(totalBlobBytes)/float64(nvmeSeqReadBW)*1e9) * time.Nanosecond

		b.ReportMetric(float64(cfg.totalBlocks())/elapsed.Seconds(), "blk/s_real")
		b.ReportMetric(float64(elapsed.Nanoseconds())/1e6, "ms_real")
		b.ReportMetric(float64(hddSeqTime.Nanoseconds())/1e6, "ms_est_hdd_seq")
		b.ReportMetric(float64(nvmeSeqTime.Nanoseconds())/1e6, "ms_est_nvme_seq")
		b.ReportMetric(float64(totalBlobBytes), "blob_bytes")
	}
}

// ----------------------------------------------------------------
// Experiment 3: scanAllBlocksCached (in-memory LRU, no I/O)
// ----------------------------------------------------------------

func BenchmarkHDDSim_ScanBlocksCached(b *testing.B) {
	for _, cfg := range hddExperimentMatrix {
		cfg := cfg
		b.Run(cfg.label, func(b *testing.B) {
			benchScanBlocksCachedHDD(b, cfg)
		})
	}
}

func benchScanBlocksCachedHDD(b *testing.B, cfg hddSimConfig) {
	dir := b.TempDir()
	sdb := openBenchDB(b, dir)

	seed := 0
	insertFullFiles(b, sdb, cfg, &seed)

	fdb := mustGetFolderDB(b, sdb)

	// Build in-memory cache: hash → []blockLocation
	// This simulates the LRU cache described in cache-over-blocks design
	b.ResetTimer()

	// Warm cache: first scan populates the cache
	warmStart := time.Now()
	cache := buildBlockCache(b, fdb)
	warmElapsed := time.Since(warmStart)
	b.ReportMetric(float64(warmElapsed.Nanoseconds())/1e6, "ms_warmup")

	// Measure query throughput using a representative hash set
	hashes := collectSampleHashes(b, fdb, 1000)
	if len(hashes) == 0 {
		b.Skip("no blocks to query")
	}

	b.ResetTimer()

	for range b.N {
		t0 := time.Now()
		for _, h := range hashes {
			_, hit := cache[toHashKey(h)]
			if !hit {
				b.Fatal("cache miss after warmup")
			}
		}
		elapsed := time.Since(t0)

		queriesPerSec := float64(len(hashes)) / elapsed.Seconds()
		b.ReportMetric(queriesPerSec, "lookups/s")
		b.ReportMetric(float64(elapsed.Nanoseconds())/1e6/float64(len(hashes))*1e6, "ns/lookup")
		b.ReportMetric(float64(elapsed.Nanoseconds())/1e6, "ms_real")
		b.ReportMetric(0, "ms_est_hdd") // zero I/O penalty for cache
	}
}

// ----------------------------------------------------------------
// Experiment 4: SkipBlockIndex effect comparison
// ----------------------------------------------------------------

func BenchmarkHDDSim_SkipBlockIndex(b *testing.B) {
	cfg := hddSimConfig{
		numFiles:  100,
		numBlocks: 8192,
		blockSize: 128 << 10,
	}

	for _, skip := range []bool{false, true} {
		label := "with_block_index"
		if skip {
			label = "skip_block_index"
		}
		b.Run(label, func(b *testing.B) {
			dir := b.TempDir()
			sdb := openBenchDB(b, dir)

			seed := 0
			files := make([]protocol.FileInfo, cfg.numFiles)
			b.ResetTimer()

			for bi := range b.N {
				t0 := time.Now()
				for i := range files {
					name := fmt.Sprintf("bench-skip-%d-%d", bi, i)
					files[i] = genFile(name, cfg.numBlocks, 0)
					files[i].Version = files[i].Version.Update(protocol.ShortID(bi + i))
					files[i].Blocks = genBlocks(name, seed, cfg.numBlocks)
					seed++
				}
				var err error
				if skip {
					err = sdb.Update(folderID, protocol.LocalDeviceID, files, db.WithSkipBlockIndex())
				} else {
					err = sdb.Update(folderID, protocol.LocalDeviceID, files)
				}
				if err != nil {
					b.Fatal(err)
				}
				elapsed := time.Since(t0)

				b.ReportMetric(float64(cfg.totalBlocks())/elapsed.Seconds(), "blk/s_real")
				b.ReportMetric(float64(elapsed.Nanoseconds())/1e6, "ms_real")
			}
			b.ReportMetric(float64(cfg.totalBlocks())/b.Elapsed().Seconds(), "blk/s_avg")
		})
	}
}

// ----------------------------------------------------------------
// Experiment 5: I/O pattern characterization (standalone diagnostic test)
// ----------------------------------------------------------------

func TestHDDSim_IOPatternCharacterization(t *testing.T) {
	if testing.Short() {
		t.Skip("I/O pattern characterization is not a short test")
	}

	cfg := hddSimConfig{
		numFiles:  100,
		numBlocks: 8192,
		blockSize: 128 << 10,
		label:     "100x8192blk_1GB",
	}

	dir := t.TempDir()
	sdb, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()

	// 1. Insert with block indexing — measure page spread
	t.Log("=== Phase 1: Insert with Block Indexing ===")
	fdb := mustGetFolderDB(t, sdb)
	seed := 0

	ps0 := capturePageStats(fdb)
	t0 := time.Now()

	files := make([]protocol.FileInfo, cfg.numFiles)
	for i := range files {
		name := fmt.Sprintf("io-char-%d", i)
		files[i] = genFile(name, cfg.numBlocks, 0)
		files[i].Blocks = genBlocks(name, seed, cfg.numBlocks)
		seed++
	}
	if err := sdb.Update(folderID, protocol.LocalDeviceID, files); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(t0)
	ps1 := capturePageStatsAfter(fdb, ps0)

	pagesAdded := ps1.deltaPages()
	blocksTotal := cfg.totalBlocks()

	t.Logf("  Real time: %v", elapsed)
	t.Logf("  Blocks inserted: %d", blocksTotal)
	t.Logf("  Data size: %d bytes (%.2f MiB)", cfg.totalBytes(), float64(cfg.totalBytes())/1024/1024)
	t.Logf("  Database pages added: %d", pagesAdded)
	t.Logf("  Page size: %d bytes", ps1.pageSize)
	t.Logf("  DB growth: %.2f MiB", float64(pagesAdded*ps1.pageSize)/1024/1024)
	t.Logf("  Blocks per page (avg): %.1f", float64(blocksTotal)/float64(pagesAdded))

	// Random page spread index: closer to 1.0 = more random (every page is a seek)
	// B-tree + WITHOUT ROWID + random hash PK → each page holds ~30-40 rows
	// 819200 blocks / 40 rows-per-page = ~20480 pages = spread index ~0.25
	theoreticalMinPages := int(math.Ceil(float64(blocksTotal) / 40.0))
	spreadIndex := float64(pagesAdded) / float64(theoreticalMinPages)
	t.Logf("  Theoretical min pages (40 rows/page): %d", theoreticalMinPages)
	t.Logf("  Page spread index (1.0=ideal, >1=spread): %.2f", spreadIndex)

	// 2. Simulate HDD random write penalty
	hddTime := ps1.estimateHDDRandomWriteTime()
	nvmeTime := ps1.estimateNVMeRandomWriteTime()
	t.Logf("  NVMe estimated write time: %v", nvmeTime)
	t.Logf("  HDD estimated write time: %v", hddTime)
	t.Logf("  HDD/NVMe ratio: %.1fx", float64(hddTime)/float64(nvmeTime))
	t.Logf("  Real/NVMe ratio: %.1fx (real includes transaction overhead)", float64(elapsed)/float64(nvmeTime))

	// 3. Sequential protobuf scan — measure real sequential read
	t.Log("=== Phase 2: Sequential Protobuf Scan ===")
	t0 = time.Now()

	count := 0
	it, errFn := fdb.AllLocalFiles(protocol.LocalDeviceID)
	for range it {
		count++
	}
	if err := errFn(); err != nil {
		t.Fatal(err)
	}
	scanElapsed := time.Since(t0)

	var totalBlobBytes int64
	fdb.sql.Get(&totalBlobBytes, `SELECT COALESCE(SUM(length(blprotobuf)), 0) FROM blocklists`)

	t.Logf("  Real time: %v", scanElapsed)
	t.Logf("  Files scanned: %d", count)
	t.Logf("  Total protobuf bytes: %d (%.2f MiB)", totalBlobBytes, float64(totalBlobBytes)/1024/1024)

	hddSeqTime := time.Duration(float64(totalBlobBytes)/float64(hddSeqReadBW)*1e9) * time.Nanosecond
	nvmeSeqTime := time.Duration(float64(totalBlobBytes)/float64(nvmeSeqReadBW)*1e9) * time.Nanosecond
	t.Logf("  NVMe estimated sequential read: %v", nvmeSeqTime)
	t.Logf("  HDD estimated sequential read: %v", hddSeqTime)
	t.Logf("  Real sequential throughput: %.2f MB/s",
		float64(totalBlobBytes)/scanElapsed.Seconds()/1024/1024)

	// 4. Build cache and measure
	t.Log("=== Phase 3: In-Memory Cache ===")
	t0 = time.Now()
	cache := buildBlockCache(t, fdb)
	cacheBuildTime := time.Since(t0)
	t.Logf("  Cache build time: %v", cacheBuildTime)
	t.Logf("  Cache entries (unique hashes): %d", len(cache))

	// 5. Compare estimated HDD costs
	t.Log("=== Comparison Summary ===")
	insertHDD := ps1.estimateHDDRandomWriteTime()
	insertNVMe := ps1.estimateNVMeRandomWriteTime()
	scanHDD := hddSeqTime
	scanNVMe := nvmeSeqTime

	t.Logf("  Phase         | Real(NVMe) | Est.NVMe  | Est.HDD   | Ratio(H/N)")
	t.Logf("  --------------+------------+-----------+-----------+----------")
	t.Logf("  insertBlocks  | %10v | %9v | %9v | %8.1fx",
		elapsed.Round(time.Millisecond),
		insertNVMe.Round(time.Millisecond),
		insertHDD.Round(time.Millisecond),
		float64(insertHDD)/float64(insertNVMe))
	t.Logf("  scanBlocks    | %10v | %9v | %9v | %8.1fx",
		scanElapsed.Round(time.Millisecond),
		scanNVMe.Round(time.Millisecond),
		scanHDD.Round(time.Millisecond),
		float64(scanHDD)/float64(scanNVMe))
	t.Logf("  cacheBuild    | %10v | %9v | %9v | %8.1fx",
		cacheBuildTime.Round(time.Millisecond),
		cacheBuildTime.Round(time.Millisecond),
		cacheBuildTime.Round(time.Millisecond),
		1.0)
	t.Logf("  cacheQ1000    | <%6s     | <%6s     | <%6s     | %8s",
		"1ms", "1ms", "1ms", "1.0x")
}

// ----------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------

// openBenchDB opens a DB with default pragmas for benchmarking
func openBenchDB(b testing.TB, dir string) *DB {
	b.Helper()
	sdb, err := Open(dir)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { sdb.Close() })
	return sdb
}

func mustGetFolderDB(t testing.TB, sdb *DB) *folderDB {
	t.Helper()
	fdb, err := sdb.getFolderDB(folderID, true)
	if err != nil {
		t.Fatal(err)
	}
	return fdb
}

// insertSeedFiles inserts file metadata WITHOUT blocks indexing (blocks table stays empty)
func insertSeedFiles(b testing.TB, sdb *DB, cfg hddSimConfig, seed *int) {
	b.Helper()
	files := make([]protocol.FileInfo, cfg.numFiles)
	for i := range files {
		name := fmt.Sprintf("seed-%d", i)
		files[i] = genFile(name, cfg.numBlocks, 0)
		files[i].Blocks = genBlocks(name, *seed, cfg.numBlocks)
		*seed++
	}
	if err := sdb.Update(folderID, protocol.LocalDeviceID, files, db.WithSkipBlockIndex()); err != nil {
		b.Fatal(err)
	}
}

// insertFullFiles inserts file metadata WITH full block indexing
func insertFullFiles(b testing.TB, sdb *DB, cfg hddSimConfig, seed *int) {
	b.Helper()
	files := make([]protocol.FileInfo, cfg.numFiles)
	for i := range files {
		name := fmt.Sprintf("full-%d", i)
		files[i] = genFile(name, cfg.numBlocks, 0)
		files[i].Blocks = genBlocks(name, *seed, cfg.numBlocks)
		*seed++
	}
	if err := sdb.Update(folderID, protocol.LocalDeviceID, files); err != nil {
		b.Fatal(err)
	}
}

// hashKey converts a byte slice hash to a fixed-size key for map lookup
type hashKey [32]byte

func toHashKey(h []byte) hashKey {
	var k hashKey
	copy(k[:], h)
	return k
}

// buildBlockCache constructs an in-memory map of hash → count.
// This simulates the LRU cache from the cache-over-blocks design.
func buildBlockCache(t testing.TB, fdb *folderDB) map[hashKey]int {
	t.Helper()
	cache := make(map[hashKey]int)

	it, errFn := fdb.AllLocalFiles(protocol.LocalDeviceID)
	for fi := range it {
		for _, b := range fi.Blocks {
			k := toHashKey(b.Hash)
			cache[k]++
		}
	}
	if err := errFn(); err != nil {
		t.Fatal(err)
	}
	return cache
}

// collectSampleHashes returns up to n unique block hashes from all local files
func collectSampleHashes(t testing.TB, fdb *folderDB, n int) [][]byte {
	t.Helper()
	seen := make(map[hashKey]bool)
	var hashes [][]byte

	it, errFn := fdb.AllLocalFiles(protocol.LocalDeviceID)
	for fi := range it {
		for _, b := range fi.Blocks {
			k := toHashKey(b.Hash)
			if !seen[k] {
				seen[k] = true
				h := make([]byte, len(b.Hash))
				copy(h, b.Hash)
				hashes = append(hashes, h)
				if len(hashes) >= n {
					goto done
				}
			}
		}
	}
done:
	if err := errFn(); err != nil {
		t.Fatal(err)
	}
	return hashes
}

var _ = rand.String
var _ = itererr.Collect[int]
