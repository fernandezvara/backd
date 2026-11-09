package admin

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"time"

	"github.com/fernandezvara/backd/internal/adminclient"
	"github.com/fernandezvara/backd/internal/cli/clicore"
)

// fetchPages reads a skip/limit list of the admin API until limit items are collected or the
// server has no more, asking for at most 100 at a time. list is the call of one page.
func fetchPages[T any](ctx context.Context, q url.Values, limit int, list func(context.Context, url.Values, any, ...adminclient.Opt) error) ([]T, error) {
	var items []T
	for skip := 0; len(items) < limit; {
		var page struct {
			Items   []T
			HasMore bool `json:"has_more"`
		}
		q.Set("limit", strconv.Itoa(min(100, limit-len(items))))
		q.Set("skip", strconv.Itoa(skip))
		if err := list(ctx, q, &page); err != nil {
			return nil, err
		}
		items = append(items, page.Items...)
		if !page.HasMore {
			break
		}
		skip += len(page.Items)
	}
	return items, nil
}

// followJob asks for a job every interval until it is done, and returns it. Each time the line
// progress makes of the job differs from the last one, it goes to w ("" prints nothing).
func followJob(t *clicore.Target, id string, interval time.Duration, w io.Writer, progress func(jobRecord) string) (jobRecord, error) {
	last := ""
	for {
		var job jobRecord
		if err := t.Adm.Jobs.Get(t.Ctx, id, &job); err != nil {
			return job, err
		}
		if line := progress(job); line != "" && line != last {
			fmt.Fprintln(w, line)
			last = line
		}
		if job.Status == "done" {
			return job, nil
		}
		time.Sleep(interval)
	}
}

// stepProgress is the usual progress line: the step and its percentage, with its message.
func stepProgress(job jobRecord) string {
	p := job.Progress
	if p == nil {
		return ""
	}
	line := fmt.Sprintf("%s %.0f%%", p.Name, p.Current)
	if p.Message != nil {
		line += " (" + *p.Message + ")"
	}
	return line
}

// resultStatus is a finished job's result status, "no result" without one.
func resultStatus(job jobRecord) string {
	if job.Result == nil {
		return "no result"
	}
	return job.Result.Status
}
