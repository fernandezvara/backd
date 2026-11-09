package mongodb

import (
	"slices"
	"testing"

	"github.com/fernandezvara/backd/internal/dbnames"
)

// The CLI lists the system databases and collections without the driver: its names must be
// the ones provisioning creates.
func TestDBNamesMatchTheSystemCollections(t *testing.T) {
	if got := dbnames.SystemCollections(); !slices.Equal(got, SystemCollectionNames()) {
		t.Errorf("dbnames.SystemCollections() = %v, want %v", got, SystemCollectionNames())
	}
	if dbnames.SystemDatabase("acme") != SystemDatabaseName("acme") || dbnames.DeploymentDatabase != DeploymentDatabase || dbnames.DeploymentCollection != DeploymentCollection {
		t.Error("the database names differ")
	}
}
