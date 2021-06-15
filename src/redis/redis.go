package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/troydota/modlogs/src/configure"
)

var (
	InvalidRespRedis = fmt.Errorf("invalid resp from redis")
)

func init() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()
	options, err := redis.ParseURL(configure.Config.GetString("redis_uri"))
	if err != nil {
		panic(err)
	}

	Client = redis.NewClient(options)

	v, err := Client.ScriptLoad(ctx, tokenConsumerLuaScript).Result()
	if err != nil {
		panic(err)
	}
	tokenConsumerLuaScriptSHA1 = v
}

var Client *redis.Client

type Message = redis.Message

type StringCmd = redis.StringCmd

type StringStringMapCmd = redis.StringStringMapCmd

const ErrNil = redis.Nil
