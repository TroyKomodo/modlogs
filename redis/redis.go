package redis

import (
	"context"
	"fmt"

	"github.com/go-redis/redis/v8"
	"github.com/troydota/modlogs/configure"
)

var Ctx = context.Background()

var (
	InvalidRespRedis = fmt.Errorf("invalid resp from redis")
)

func init() {
	options, err := redis.ParseURL(configure.Config.GetString("redis_uri"))
	if err != nil {
		panic(err)
	}

	Client = redis.NewClient(options)

	v, err := Client.ScriptLoad(Ctx, tokenConsumerLuaScript).Result()
	if err != nil {
		panic(err)
	}
	tokenConsumerLuaScriptSHA1 = v

	v, err = Client.ScriptLoad(Ctx, createCallbackLuaScript).Result()
	if err != nil {
		panic(err)
	}
	createCallbackLuaScriptSHA1 = v

	v, err = Client.ScriptLoad(Ctx, deleteCallbackLuaScript).Result()
	if err != nil {
		panic(err)
	}
	deleteCallbackLuaScriptSHA1 = v

	v, err = Client.ScriptLoad(Ctx, getCallbacksLuaScript).Result()
	if err != nil {
		panic(err)
	}
	getCallbacksLuaScriptSHA1 = v

	v, err = Client.ScriptLoad(Ctx, getGuildCallbacksLuaScript).Result()
	if err != nil {
		panic(err)
	}
	getGuildCallbacksLuaScriptSHA1 = v
}

var Client *redis.Client

type Message = redis.Message

type StringCmd = redis.StringCmd

type StringStringMapCmd = redis.StringStringMapCmd

const ErrNil = redis.Nil
