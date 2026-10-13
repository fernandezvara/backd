package main

import (
	"fmt"
	"net/url"
	"text/tabwriter"
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
