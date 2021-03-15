package redis

import (
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

func AuthTokenValues(token string) (string, error) {
	res, err := Client.EvalSha(
		Ctx,
		tokenConsumerLuaScriptSHA1, // scriptSHA1
		[]string{},                 // KEYS
		token,                      // ARGV[1]
	).Result()
	if err != nil {
		return "", err
	}
	resp, ok := res.(string)
	if !ok {
		log.Errorf("redis resp, resp=%v", res)
		return "", InvalidRespRedis
	}
	return resp, nil
}
