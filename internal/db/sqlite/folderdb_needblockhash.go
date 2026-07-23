// Copyright (C) 2026 The Syncdesic Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package sqlite

import (
	"bytes"
	"fmt"
	"iter"

	"github.com/syncthing/syncthing/internal/db"
	"github.com/syncthing/syncthing/internal/itererr"
	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/protocol"
)

// neededBlockHashRow is the raw SQL row for AllNeededBlockHashes.
type neededBlockHashRow struct {
	BlocklistHash []byte `db:"blocklisthash"`
	Name          string `db:"name"`
}

func (s *folderDB) AllNeededBlockHashes(device protocol.DeviceID, order config.PullOrder, limit, offset int) (iter.Seq[db.NeededBlockHash], func() error) {
	var selectOpts string
	switch order {
	case config.PullOrderRandom:
		selectOpts = "ORDER BY g.blocklist_hash, n.name"
	case config.PullOrderAlphabetic:
		selectOpts = "ORDER BY g.blocklist_hash, n.name"
	case config.PullOrderSmallestFirst:
		selectOpts = "ORDER BY g.size ASC, g.blocklist_hash, n.name"
	case config.PullOrderLargestFirst:
		selectOpts = "ORDER BY g.size DESC, g.blocklist_hash, n.name"
	case config.PullOrderOldestFirst:
		selectOpts = "ORDER BY g.modified ASC, g.blocklist_hash, n.name"
	case config.PullOrderNewestFirst:
		selectOpts = "ORDER BY g.modified DESC, g.blocklist_hash, n.name"
	default:
		selectOpts = "ORDER BY g.blocklist_hash, n.name"
	}

	if limit > 0 {
		selectOpts += fmt.Sprintf(" LIMIT %d", limit)
	}
	if offset > 0 {
		selectOpts += fmt.Sprintf(" OFFSET %d", offset)
	}

	if device == protocol.LocalDeviceID {
		return s.neededBlockHashLocal(selectOpts)
	}
	return s.neededBlockHashRemote(device, selectOpts)
}

func (s *folderDB) neededBlockHashLocal(selectOpts string) (iter.Seq[db.NeededBlockHash], func() error) {
	rows, errFn := iterStructs[neededBlockHashRow](s.stmt(`
		SELECT g.blocklist_hash as blocklisthash, n.name FROM files g
		INNER JOIN file_names n ON g.name_idx = n.idx
		WHERE g.local_flags & {{.FlagLocalIgnored}} = 0
		  AND g.local_flags & {{.FlagLocalNeeded}} != 0
		  AND g.blocklist_hash IS NOT NULL
	` + selectOpts).Queryx())
	return groupBlockHashRows(rows, errFn)
}

func (s *folderDB) neededBlockHashRemote(device protocol.DeviceID, selectOpts string) (iter.Seq[db.NeededBlockHash], func() error) {
	rows, errFn := iterStructs[neededBlockHashRow](s.stmt(`
		SELECT g.blocklist_hash as blocklisthash, n.name FROM files g
		INNER JOIN file_names n ON g.name_idx = n.idx
		WHERE g.local_flags & {{.FlagLocalGlobal}} != 0
		  AND NOT g.deleted
		  AND g.local_flags & {{.LocalInvalidFlags}} = 0
		  AND g.blocklist_hash IS NOT NULL
		  AND NOT EXISTS (
			SELECT 1 FROM files f
			INNER JOIN devices d ON d.idx = f.device_idx
			WHERE f.name_idx = g.name_idx
			  AND f.version_idx = g.version_idx
			  AND d.device_id = ?
		)
	` + selectOpts).Queryx(device.String()))
	return groupBlockHashRows(rows, errFn)
}

// groupBlockHashRows reads all rows from the iterator, groups consecutive rows
// with the same blocklist_hash, and returns a new iterator of NeededBlockHash.
// The input must be ordered by blocklist_hash.
func groupBlockHashRows(it iter.Seq[neededBlockHashRow], errFn func() error) (iter.Seq[db.NeededBlockHash], func() error) {
	rows, err := itererr.Collect(it, errFn)
	if err != nil {
		return func(yield func(db.NeededBlockHash) bool) {}, func() error { return err }
	}

	if len(rows) == 0 {
		return func(yield func(db.NeededBlockHash) bool) {}, func() error { return nil }
	}

	var result []db.NeededBlockHash
	var current *db.NeededBlockHash
	for _, r := range rows {
		if current == nil || !bytes.Equal(current.Hash, r.BlocklistHash) {
			if current != nil {
				result = append(result, *current)
			}
			current = &db.NeededBlockHash{
				Hash:  r.BlocklistHash,
				Names: []string{r.Name},
			}
		} else {
			current.Names = append(current.Names, r.Name)
		}
	}
	if current != nil {
		result = append(result, *current)
	}

	return func(yield func(db.NeededBlockHash) bool) {
		for _, h := range result {
			if !yield(h) {
				return
			}
		}
	}, func() error { return nil }
}
