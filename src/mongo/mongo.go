package mongo

import (
	"context"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/troydota/modlogs/src/configure"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var Database *mongo.Database

var ErrNoDocuments = mongo.ErrNoDocuments

func init() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()
	clientOptions := options.Client().ApplyURI(configure.Config.GetString("mongo_uri"))
	client, err := mongo.Connect(ctx, clientOptions)
	if err != nil {
		log.WithError(err).Fatal("mongo")
	}

	err = client.Ping(ctx, nil)
	if err != nil {
		log.WithError(err).Fatal("mongo")
	}

	Database = client.Database(configure.Config.GetString("mongo_db"))

	_, err = Database.Collection("hooks").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "channel_id", Value: 1}, {Key: "guild_id", Value: 1}, {Key: "streamer_id", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.M{"channel_id": 1}},
		{Keys: bson.M{"guild_id": 1}},
		{Keys: bson.M{"streamer_id": 1}},
	})
	if err != nil {
		log.WithError(err).Fatal("mongo")
	}

	_, err = Database.Collection("users").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.M{"id": 1}, Options: options.Index().SetUnique(true)},
		{Keys: bson.M{"login": 1}, Options: options.Index().SetUnique(true)},
	})

	if err != nil {
		log.WithError(err).Fatal("mongo")
	}
}
