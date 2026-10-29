// Command shelf is the Go client against the Shelf tutorial stack (docs: Tutorial): it signs in,
// lists the published assets, saves a note and follows the assets live for a little while.
//
//	SHELF_EMAIL=ada@example.com SHELF_PASSWORD='dev-p4ssw0rd!' go run ./clients/go-examples/shelf
//
// BACKD_URL is the stack's address (http://localhost:8080 by default). The session is kept in
// ~/.backd-shelf.session, so only the first run signs in. backd/types.go is generated:
//
//	CONFIG_DIR=examples/config backd types --realm shelf --lang go --package backd --out clients/go-examples/shelf/backd
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fernandezvara/backd/clients/go-examples/shelf/backd"
	"github.com/fernandezvara/backd/pkg/client"
)

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	url := os.Getenv("BACKD_URL")
	if url == "" {
		url = "http://localhost:8080"
	}
	c, err := client.New(client.Options{
		URL:     url,
		Realm:   "shelf",
		Storage: client.NewFileStorage(filepath.Join(home, ".backd-shelf.session")),
	})
	if err != nil {
		return err
	}
	defer c.Close()

	// The token lives in the file above: only the first run signs in.
	if ok, err := c.Auth().HasSession(ctx); err != nil {
		return err
	} else if !ok {
		if _, err := c.Auth().Login(ctx, client.SignInInput{Email: os.Getenv("SHELF_EMAIL"), Password: os.Getenv("SHELF_PASSWORD")}); err != nil {
			var second *client.SecondStepRequiredError
			if errors.As(err, &second) {
				return fmt.Errorf("this account has a second step (%s): sign in once with `backd login`", second.Code)
			}
			return err
		}
	}

	shelf := backd.NewShelfRealm(c)
	assets := shelf.Main().Assets()

	// The newest published assets: a typed query, with the field names the config declares.
	page, err := assets.List(ctx, client.Query{
		Where:   client.Where{backd.MainAssetsFieldPublishedAt: client.NotNull()},
		OrderBy: []string{backd.MainAssetsFieldPublishedAtDesc},
		Limit:   20,
	})
	if err != nil {
		return err
	}
	for _, a := range page.Items {
		fmt.Printf("%-8s %-40s %s\n", a.Kind, a.Title, strings.Join(a.Tags, ", "))
	}

	// A draft note of the signed-in user.
	body := "Saved with the Go client."
	note, err := assets.Create(ctx, backd.MainAssetsInput{Title: "Hello from Go", Kind: backd.MainAssetsKindNote, Body: &body})
	if err != nil {
		return err
	}
	fmt.Println("saved", note.ID)

	// Follow the assets the user may read, live, for a few seconds.
	view, err := assets.Collection().Subscribe(client.SubscriptionSpec{Load: client.LoadSpec{Sort: []string{"-_meta.created_at"}, Limit: 20}}, client.SubscribeOptions{})
	if err != nil {
		return err
	}
	defer view.Close()
	view.OnChange(func(s client.LiveState) { fmt.Printf("live: %d assets (%s)\n", len(s.Items), s.Status) })
	select {
	case <-time.After(10 * time.Second):
	case <-ctx.Done():
	}
	return view.Err()
}
