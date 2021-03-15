package mongo

import (
	log "github.com/sirupsen/logrus"

	"context"

	"github.com/troydota/modlogs/configure"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var Database *mongo.Database
var Ctx = context.TODO()

var ErrNoDocuments = mongo.ErrNoDocuments

func init() {
	clientOptions := options.Client().ApplyURI(configure.Config.GetString("mongo_uri"))
	client, err := mongo.Connect(Ctx, clientOptions)
	if err != nil {
		log.Errorf("mongodb connect, err=%e", err)
		return
	}

	err = client.Ping(Ctx, nil)
	if err != nil {
		log.Errorf("mongodb ping, err=%e", err)
		return
	}

	Database = client.Database(configure.Config.GetString("mongo_db"))

	Database.CreateCollection(Ctx, "hooks")
	Database.CreateCollection(Ctx, "users")

	_, err = Database.Collection("hooks").Indexes().CreateMany(Ctx, []mongo.IndexModel{
		{Keys: bson.D{{"channel_id", 1}, {"guild_id", 1}, {"streamer_id", 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.M{"channel_id": 1}},
		{Keys: bson.M{"guild_id": 1}},
		{Keys: bson.M{"streamer_id": 1}},
	})
	if err != nil {
		log.Errorf("mongodb, err=%e", err)
		return
	}

	_, err = Database.Collection("users").Indexes().CreateMany(Ctx, []mongo.IndexModel{
		{Keys: bson.M{"id": 1}, Options: options.Index().SetUnique(true)},
		{Keys: bson.M{"login": 1}, Options: options.Index().SetUnique(true)},
	})

	if err != nil {
		log.Errorf("mongodb, err=%e", err)
		return
	}
}
