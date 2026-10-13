package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"text/tabwriter"

	"github.com/fernandezvara/kli"
)

// pushDevices lists a user's registered devices: never their tokens.
func pushDevices(c *userCtx) error {
	u, err := c.find()
	if err != nil {
		return err
	}
	var out struct {
		Items []struct {
			ID       string  `json:"id"`
			Platform string  `json:"platform"`
			Name     *string `json:"name"`
			LastSeen string  `json:"last_seen"`
		} `json:"items"`
	}
	if err := c.t.call("GET", "_admin/users/"+url.PathEscape(u.ID)+"/devices", nil, nil, &out); err != nil {
		return err
	}
	if len(out.Items) == 0 {
		fmt.Fprintf(c.uio.stdout, "%s has no registered devices\n", u.Email)
		return nil
	}
	w := tabwriter.NewWriter(c.uio.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tPLATFORM\tNAME\tLAST SEEN")
	for _, d := range out.Items {
		name := "-"
		if d.Name != nil {
			name = *d.Name
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", d.ID, d.Platform, name, d.LastSeen)
	}
	return w.Flush()
}

// pushRemoveDevice removes one device of a user; the removal is audited.
func pushRemoveDevice(c *userCtx) error {
	u, err := c.find()
	if err != nil {
		return err
	}
	id := str(c.cmd, "id")
	if err := c.t.call("DELETE", "_admin/users/"+url.PathEscape(u.ID)+"/devices/"+url.PathEscape(id), nil, nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(c.uio.stdout, "device %s of %s removed\n", id, u.Email)
	return nil
}

// pushSendRow is a send as the admin API lists it.
type pushSendRow struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind"`
	Sender    string  `json:"sender"`
	CreatedAt string  `json:"created_at"`
	Users     *int    `json:"users"`
	Queued    int     `json:"queued"`
	Limited   int     `json:"limited"`
	NoDevices int     `json:"no_devices"`
	Sent      int     `json:"sent"`
	Invalid   int     `json:"invalid"`
	Failed    int     `json:"failed"`
	Completed bool    `json:"completed"`
	Batches   int     `json:"batches"`
	Units     int     `json:"units"`
	UnitsDone int     `json:"units_done"`
	Planned   bool    `json:"planned"`
	DoneAt    *string `json:"completed_at"`
}

// pushSends lists the realm's sends, or shows one with --id.
func pushSends(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	out := ioOf(c)
	if id := str(c, "id"); id != "" {
		var row pushSendRow
		if err := t.call("GET", "_admin/push/sends/"+url.PathEscape(id), nil, nil, &row); err != nil {
			return err
		}
		state := "in progress"
		if row.Completed {
			state = "completed " + *row.DoneAt
		}
		fmt.Fprintf(out.stdout, "send      %s\nkind      %s\nsender    %s\ncreated   %s\nstate     %s\n", row.ID, row.Kind, row.Sender, row.CreatedAt, state)
		fmt.Fprintf(out.stdout, "users     queued %d, limited %d, no devices %d\ndevices   sent %d, invalid %d, failed %d\nwork      %d batches, %d of %d units done\n",
			row.Queued, row.Limited, row.NoDevices, row.Sent, row.Invalid, row.Failed, row.Batches, row.UnitsDone, row.Units)
		return nil
	}
	query := url.Values{}
	for _, k := range []string{"kind", "function", "limit"} {
		if v := str(c, k); v != "" {
			query.Set(k, v)
		}
	}
	var page struct {
		Items []pushSendRow `json:"items"`
	}
	if err := t.call("GET", "_admin/push/sends", query, nil, &page); err != nil {
		return err
	}
	if len(page.Items) == 0 {
		fmt.Fprintln(out.stdout, "no sends")
		return nil
	}
	w := tabwriter.NewWriter(out.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tKIND\tSENDER\tQUEUED\tLIMITED\tNO DEVICES\tSENT\tINVALID\tFAILED\tDONE")
	for _, r := range page.Items {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%t\n", r.ID, r.Kind, r.Sender, r.Queued, r.Limited, r.NoDevices, r.Sent, r.Invalid, r.Failed, r.Completed)
	}
	return w.Flush()
}

// pushTest sends one user a test push, audited and counted toward their limits.
func pushTest(c *userCtx) error {
	body := map[string]any{"email": c.email}
	for flag, key := range map[string]string{"kind": "kind", "title": "title", "body": "body"} {
		if v := str(c.cmd, flag); v != "" {
			body[key] = v
		}
	}
	if raw := str(c.cmd, "data"); raw != "" {
		var data map[string]any
		if err := json.Unmarshal([]byte(raw), &data); err != nil {
			return fmt.Errorf("--data must be a JSON object of strings: %w", err)
		}
		body["data"] = data
	}
	var out struct {
		SendID    string `json:"send_id"`
		Queued    int    `json:"queued"`
		Limited   int    `json:"limited"`
		NoDevices int    `json:"no_devices"`
	}
	if err := c.t.call("POST", "_admin/push/test", nil, body, &out); err != nil {
		return err
	}
	fmt.Fprintf(c.uio.stdout, "send %s: %s\n", out.SendID, map[bool]string{
		true:  "queued for delivery; see it with `backd push sends --id " + out.SendID + "`",
		false: fmt.Sprintf("nothing queued (limited %d, no devices %d)", out.Limited, out.NoDevices),
	}[out.Queued > 0])
	return nil
}
