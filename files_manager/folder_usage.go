// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"sort"

	pb "github.com/alonsovidales/otc/proto/generated"
)

// FolderUsage answers GetFolderUsage (issue #195, Files' Storage view):
// every direct child of folder that holds a file or an older version, with
// what it holds at any depth (dao.FolderUsage), largest first, and the
// folder's own totals. The folder is taken as ListFiles takes it; one
// given without its trailing slash is that folder, and "" is the root. A
// folder that holds nothing, or doesn't exist, answers with no entries.
func (mg *Manager) FolderUsage(folder string) (*pb.FolderUsage, error) {
	folder = folderPath(folder)
	entries, err := mg.dao.FolderUsage(folder)
	if err != nil {
		return nil, err
	}
	sortFolderUsage(entries)

	usage := &pb.FolderUsage{Path: folder, Entries: entries}
	for _, e := range entries {
		usage.FilesSize += e.FilesSize
		usage.FileCount += e.FileCount
		usage.VersionsSize += e.VersionsSize
	}

	return usage, nil
}

// sortFolderUsage puts the largest entries first (files and older
// versions together), then by path byte by byte - the database's order for
// the binary path columns - and a folder before a file of the same name.
func sortFolderUsage(entries []*pb.FolderUsageEntry) {
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if ta, tb := a.FilesSize+a.VersionsSize, b.FilesSize+b.VersionsSize; ta != tb {
			return ta > tb
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.IsDir && !b.IsDir
	})
}
