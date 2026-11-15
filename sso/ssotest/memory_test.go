package ssotest_test

import (
	"testing"
	"time"

	"github.com/fernandezvara/backd/sso"
	"github.com/fernandezvara/backd/sso/ssotest"
)

func TestMemStoreConforms(t *testing.T) {
	ssotest.TestStore(t, func(now func() time.Time) sso.Store { return &ssotest.MemStore{Now: now} })
}

func TestMemDirectoryConforms(t *testing.T) {
	ssotest.TestDirectory(t, func(t *testing.T) ssotest.DirectoryHarness {
		d := &ssotest.Directory{}
		return ssotest.DirectoryHarness{Directory: d, Seed: d.AddUser}
	})
}
