package redis

import (
	log "github.com/sirupsen/logrus"
)

var createCallbackLuaScript = `
local streamerID = ARGV[1]
local guildID = ARGV[2]
local channelID = ARGV[3]
local mode = ARGV[4]

local current = tonumber(redis.call("HLEN", "callbacks:" .. streamerID .. ":guilds:" .. guildID))

local isSet = tonumber(redis.call("HGET", "callbacks:" .. streamerID .. ":guilds:" .. guildID, channelID)) or 0

if isSet > 0 then 
	if isSet ~= mode then 
		redis.call("HSET", "callbacks:" .. streamerID .. ":guilds:" .. guildID, channelID, mode)
	end
	return tonumber(redis.call("GET", "callbacks:" .. streamerID .. ":count"))
end

local val = tonumber(redis.call("INCR", "callbacks:" .. streamerID .. ":count"))

redis.call("HSET", "callbacks:" .. streamerID .. ":guilds:list", guildID, 1)

redis.call("HSET", "callbacks:" .. guildID, streamerID, 1)

redis.call("HSET", "callbacks:" .. streamerID .. ":guilds:" .. guildID, channelID, mode)

return val - 1
`

var (
	createCallbackLuaScriptSHA1 string
)

func CreateCallback(streamerID string, guildID string, channelID string, minimal bool) (int64, error) {
	newMin := 1
	if minimal {
		newMin = 2
	}
	res, err := Client.EvalSha(
		Ctx,
		createCallbackLuaScriptSHA1, // scriptSHA1
		[]string{},                  // KEYS
		streamerID,                  // ARGV[1]
		guildID,                     // ARGV[2]
		channelID,                   // ARGV[3]
		newMin,                      // ARGV[4]
	).Result()
	if err != nil {
		return 0, err
	}
	resp, ok := res.(int64)
	if !ok {
		log.Errorf("redis resp, resp=%v", res)
		return 0, InvalidRespRedis
	}
	return resp, nil
}

var deleteCallbackLuaScript = `
local streamerID = ARGV[1]
local guildID = ARGV[2]
local channelID = ARGV[3]

local function values(t)
  local i = 0
  return function() i = i + 1; return t[i] end
end

if guildID == '' then 
	local guilds = redis.call("HKEYS", "callbacks:" .. streamerID .. ":guilds:list")
	if table.getn(guilds) == 0 then
		return -1
	end
	for v in values(guilds) do
		redis.call("DEL", "callbacks:" .. streamerID .. ":guilds:" .. v)
		redis.call("HDEL", "callbacks:" .. guildID, streamerID)
	end

	redis.call("DEL", "callbacks:" .. streamerID .. ":count")

	return 0
end

if channelID == '' then 
	local len = tonumber(redis.call("HLEN", "callbacks:" .. streamerID .. ":guilds:" .. guildID))
	if len == 0 then 
		return -1
	end
	local guilds = redis.call("DEL", "callbacks:" .. streamerID .. ":guilds:" .. guildID)

	if tonumber(redis.call("HLEN", "callbacks:" .. streamerID .. ":guilds:" .. guildID)) == 0 then 
		redis.call("HDEL", "callbacks:" .. streamerID .. ":guilds:list", guildID)
		redis.call("HDEL", "callbacks:" .. guildID, streamerID)
	end

	local val = tonumber(redis.call("DECRBY", "callbacks:" .. streamerID .. ":count", len))
	if val == 0 then
		redis.call("DEL", "callbacks:" .. streamerID .. ":count")
	end

	return val
end

if redis.call("HEXISTS", "callbacks:" .. streamerID .. ":guilds:" .. guildID, channelID) == 0 then
	return -1
end

local val = tonumber(redis.call("DECR", "callbacks:" .. streamerID .. ":count"))

if val == 0 then
	redis.call("DEL", "callbacks:" .. streamerID .. ":count")
end

redis.call("HDEL", "callbacks:" .. streamerID .. ":guilds:" .. guildID, channelID)

if tonumber(redis.call("HLEN", "callbacks:" .. streamerID .. ":guilds:" .. guildID)) == 0 then 
	redis.call("HDEL", "callbacks:" .. streamerID .. ":guilds:list", guildID)
	redis.call("HDEL", "callbacks:" .. guildID, streamerID)
end

return val
`

var (
	deleteCallbackLuaScriptSHA1 string
)

func DeleteCallback(streamerID string, guildID string, channelID string) (int64, error) {
	res, err := Client.EvalSha(
		Ctx,
		deleteCallbackLuaScriptSHA1, // scriptSHA1
		[]string{},                  // KEYS
		streamerID,                  // ARGV[1]
		guildID,                     // ARGV[2]
		channelID,                   // ARGV[3]
	).Result()
	if err != nil {
		return 0, err
	}
	resp, ok := res.(int64)
	if !ok {
		log.Errorf("redis resp, resp=%v", res)
		return 0, InvalidRespRedis
	}
	return resp, nil
}

var getCallbacksLuaScript = `
local streamerID = ARGV[1]
local guildID 

local function values(t)
  local i = 0
  return function() i = i + 1; return t[i] end
end

local tb = {}

local guilds = redis.call("HKEYS", "callbacks:" .. streamerID .. ":guilds:list")
for v in values(guilds) do
	local channels = redis.call("HGETALL", "callbacks:" .. streamerID .. ":guilds:" .. v)
	local len = table.getn(channels)
	local stb = {}
	for i=1,len,2 do
		table.insert(stb, {tostring(channels[i]), tonumber(channels[i+1])})
	end
	table.insert(tb, {v, stb})
end

return tb
`

var (
	getCallbacksLuaScriptSHA1 string
)

func GetCallbacks(streamerID string) ([]interface{}, error) {
	res, err := Client.EvalSha(
		Ctx,
		getCallbacksLuaScriptSHA1, // scriptSHA1
		[]string{},                // KEYS
		streamerID,                // ARGV[1]
	).Result()
	if err != nil {
		return nil, err
	}
	resp, ok := res.([]interface{})
	if !ok {
		log.Errorf("redis resp, resp=%v", res)
		return nil, InvalidRespRedis
	}
	return resp, nil
}

var getGuildCallbacksLuaScript = `
local guildID = ARGV[1]
local channelID = ARGV[2] 

local function values(t)
  local i = 0
  return function() i = i + 1; return t[i] end
end

local streamers = redis.call("HKEYS", "callbacks:" .. guildID)

local tb = {}

for v in values(streamers) do
	local channels = redis.call("HGETALL", "callbacks:" .. v .. ":guilds:" .. guildID)
	local len = table.getn(channels)
	local stb = {}
	for i=1,len,2 do
		if channelID == "" or channelID == tostring(channels[i]) then   
			table.insert(stb, {tostring(channels[i]), tonumber(channels[i+1])})
		end
	end
  if table.getn(stb) > 0 then
		table.insert(tb, {v, stb})
  end
end

return tb
`

var (
	getGuildCallbacksLuaScriptSHA1 string
)

func GetGuildCallbacks(guildID string, channelID string) ([]interface{}, error) {
	res, err := Client.EvalSha(
		Ctx,
		getGuildCallbacksLuaScriptSHA1, // scriptSHA1
		[]string{},                     // KEYS
		guildID,                        // ARGV[1]
		channelID,                      // ARGV[2]
	).Result()
	if err != nil {
		return nil, err
	}
	resp, ok := res.([]interface{})
	if !ok {
		log.Errorf("redis resp, resp=%v", res)
		return nil, InvalidRespRedis
	}
	return resp, nil
}
