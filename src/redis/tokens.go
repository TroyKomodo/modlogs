package redis

import (
	"context"

	log "github.com/sirupsen/logrus"
)

var tokenConsumerLuaScript = `
local token = ARGV[1]

local userID = tostring(redis.call("GET", "temp:codes:" .. token))

if not userID then
	return nil
end

redis.call("DEL", "temp:codes:" .. token)

return userID
`

var (
	tokenConsumerLuaScriptSHA1 string
)

func AuthTokenValues(ctx context.Context, token string) (string, error) {
	res, err := Client.EvalSha(
		ctx,
		tokenConsumerLuaScriptSHA1, // scriptSHA1
		[]string{},                 // KEYS
		token,                      // ARGV[1]
	).Result()
	if err != nil {
		return "", err
	}
	resp, ok := res.(string)
	if !ok {
		log.WithError(err).Error("redis")
		return "", InvalidRespRedis
	}
	return resp, nil
}
