// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"
)

// cDownloadsWriteTimeout bounds a snapshot's write: a poll that can't
// write goes on at the next one, six hours later. ctx ends it sooner (the
// poller stopping): REPLACE writes the same totals again next time, so a
// write cut short loses nothing.
const cDownloadsWriteTimeout = 30 * time.Second

// SaveDownloadCounts keeps one poll of the project's GitHub release
// downloads (package downloads): each file's total as day's snapshot (the
// UTC date of day). REPLACE, in one statement: a later poll the same day,
// or the other node's, writes its totals over the earlier ones, and a
// failed write leaves the day as it was.
func (dao *Dao) SaveDownloadCounts(ctx context.Context, day time.Time, counts map[string]int64) error {
	if len(counts) == 0 {
		return nil
	}
	d := day.UTC().Format(time.DateOnly)
	assets := slices.Sorted(maps.Keys(counts))
	args := make([]any, 0, 3*len(assets))
	for _, a := range assets {
		args = append(args, d, a, counts[a])
	}
	ctx, cancel := context.WithTimeout(ctx, cDownloadsWriteTimeout)
	defer cancel()
	_, err := dao.db.ExecContext(ctx,
		"replace into `download_counts` (`day`, `asset`, `downloads`) values "+
			strings.TrimSuffix(strings.Repeat("(?, ?, ?), ", len(assets)), ", "),
		args...)
	return err
}
