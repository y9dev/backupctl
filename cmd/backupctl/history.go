package main

import (
	"fmt"

	"backupctl/internal/database"
)

// normalizeHistoryPage clamps user pagination input to safe values.
func normalizeHistoryPage(page, perPage int) (int, int) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 20
	}
	if perPage > 100 {
		perPage = 100
	}
	return page, perPage
}

// historyFooter renders "Page X/Y — showing A-B of TOTAL".
func historyFooter(page, perPage, total int) string {
	if total == 0 {
		return "No entries."
	}
	pages := (total + perPage - 1) / perPage
	if page > pages {
		page = pages
	}
	start := (page-1)*perPage + 1
	end := page * perPage
	if end > total {
		end = total
	}
	return fmt.Sprintf("Page %d/%d — showing %d-%d of %d (per-page %d)", page, pages, start, end, total, perPage)
}

// resolveHistoryFilter maps --server/--job names to IDs (0 = no filter).
func resolveHistoryFilter(db *database.DB, serverName, jobName string) (jobID, serverID int64, err error) {
	if serverName != "" {
		srv, err := db.GetServerByName(serverName)
		if err != nil {
			return 0, 0, fmt.Errorf("server %q not found", serverName)
		}
		serverID = srv.ID
	}
	if jobName != "" {
		if serverName != "" {
			jb, _, err := db.GetJob(serverName, jobName)
			if err != nil {
				return 0, 0, fmt.Errorf("job %q not found on server %q", jobName, serverName)
			}
			jobID = jb.ID
			serverID = jb.ServerID
			return jobID, serverID, nil
		}
		jobs, err := db.ListJobs(0)
		if err != nil {
			return 0, 0, err
		}
		var match *struct {
			id, srv int64
		}
		count := 0
		for _, j := range jobs {
			if j.Name == jobName {
				count++
				id, srv := j.ID, j.ServerID
				match = &struct {
					id, srv int64
				}{id, srv}
			}
		}
		if count == 0 {
			return 0, 0, fmt.Errorf("job %q not found", jobName)
		}
		if count > 1 {
			return 0, 0, fmt.Errorf("job %q exists on %d servers, specify --server", jobName, count)
		}
		jobID = match.id
		serverID = match.srv
	}
	return jobID, serverID, nil
}
