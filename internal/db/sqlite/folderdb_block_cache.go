package sqlite

import (
	"container/list"
	"sync"

	"github.com/syncthing/syncthing/internal/db"
	"github.com/syncthing/syncthing/lib/protocol"
)

type cacheEntry struct {
	hash      [32]byte
	locations []db.BlockMapEntry
}

type blockCache struct {
	mu     sync.RWMutex
	byHash map[[32]byte]*list.Element
	byFile map[string]map[[32]byte]struct{}
	lru    *list.List
	max    int
}

func newBlockCache(maxSize int) *blockCache {
	if maxSize < 0 {
		maxSize = 0
	}
	return &blockCache{
		byHash: make(map[[32]byte]*list.Element),
		byFile: make(map[string]map[[32]byte]struct{}),
		lru:    list.New(),
		max:    maxSize,
	}
}

func (c *blockCache) get(h [32]byte) ([]db.BlockMapEntry, bool) {
	if c.max == 0 {
		return nil, false
	}
	c.mu.RLock()
	elem, ok := c.byHash[h]
	if !ok {
		c.mu.RUnlock()
		return nil, false
	}
	c.mu.RUnlock()

	c.mu.Lock()
	c.lru.MoveToFront(elem)
	entry := elem.Value.(*cacheEntry)
	c.mu.Unlock()

	return entry.locations, true
}

func (c *blockCache) set(h [32]byte, locations []db.BlockMapEntry) {
	if c.max == 0 || len(locations) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, ok := c.byHash[h]; ok {
		c.lru.MoveToFront(elem)
		entry := elem.Value.(*cacheEntry)
		entry.locations = locations
		return
	}

	for c.lru.Len() >= c.max {
		c.evictBack()
	}

	entry := &cacheEntry{hash: h, locations: locations}
	elem := c.lru.PushFront(entry)
	c.byHash[h] = elem
}

func (c *blockCache) notify(name string, blocklistHash []byte, blocks []protocol.BlockInfo) {
	if c.max == 0 || len(blocks) == 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.invalidateFileLocked(name)

	fileHashes := make(map[[32]byte]struct{}, len(blocks))

	for i, b := range blocks {
		var h [32]byte
		copy(h[:], b.Hash)

		loc := db.BlockMapEntry{
			BlocklistHash: blocklistHash,
			Offset:        b.Offset,
			BlockIndex:    i,
			Size:          int(b.Size),
			FileName:      name,
		}

		if elem, ok := c.byHash[h]; ok {
			entry := elem.Value.(*cacheEntry)
			entry.locations = append(entry.locations, loc)
			c.lru.MoveToFront(elem)
		} else {
			if c.lru.Len() >= c.max {
				c.evictBack()
			}
			entry := &cacheEntry{hash: h, locations: []db.BlockMapEntry{loc}}
			elem := c.lru.PushFront(entry)
			c.byHash[h] = elem
		}
		fileHashes[h] = struct{}{}
	}

	c.byFile[name] = fileHashes
}

func (c *blockCache) invalidateFile(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.invalidateFileLocked(name)
}

func (c *blockCache) invalidateFileLocked(name string) {
	hashes, ok := c.byFile[name]
	if !ok {
		return
	}
	for h := range hashes {
		if elem, exists := c.byHash[h]; exists {
			entry := elem.Value.(*cacheEntry)

			filtered := entry.locations[:0]
			for _, loc := range entry.locations {
				if loc.FileName != name {
					filtered = append(filtered, loc)
				}
			}

			if len(filtered) == 0 {
				c.lru.Remove(elem)
				delete(c.byHash, h)
			} else {
				entry.locations = filtered
			}
		}
	}
	delete(c.byFile, name)
}

func (c *blockCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byHash = make(map[[32]byte]*list.Element)
	c.byFile = make(map[string]map[[32]byte]struct{})
	c.lru.Init()
}

func (c *blockCache) evictBack() {
	elem := c.lru.Back()
	if elem == nil {
		return
	}
	entry := elem.Value.(*cacheEntry)
	delete(c.byHash, entry.hash)

	for _, loc := range entry.locations {
		if fileHashes, ok := c.byFile[loc.FileName]; ok {
			delete(fileHashes, entry.hash)
			if len(fileHashes) == 0 {
				delete(c.byFile, loc.FileName)
			}
		}
	}

	c.lru.Remove(elem)
}
