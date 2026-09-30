package mongodb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Connect opens a client and checks the server is reachable and part of a
// replica set (or a sharded cluster).
func Connect(ctx context.Context, uri string) (*mongo.Client, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("mongodb: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx, nil); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("mongodb: ping: %w", err)
	}
	if err := checkTopology(pingCtx, client); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, err
	}
	return client, nil
}

// ErrStandalone means MongoDB isn't a replica set member (or a sharded
// cluster's router), so transactions aren't available.
var ErrStandalone = errors.New("mongodb: the server is a standalone instance; backd needs a replica set (a single node is fine): start mongod with --replSet and run rs.initiate(), then add ?replicaSet=<name> to MONGO_URI")

// checkTopology refuses standalone servers: backd's atomic batch writes and
// later features rely on transactions, which need a replica set.
func checkTopology(ctx context.Context, client *mongo.Client) error {
	var hello struct {
		SetName string `bson:"setName"`
		Msg     string `bson:"msg"`
	}
	if err := client.Database("admin").RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&hello); err != nil {
		return fmt.Errorf("mongodb: hello: %w", err)
	}
	if hello.SetName == "" && hello.Msg != "isdbgrid" {
		return ErrStandalone
	}
	return nil
}
