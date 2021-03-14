package auth

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/troydota/modlogs/configure"
	"github.com/troydota/modlogs/redis"

	jsoniter "github.com/json-iterator/go"
	"github.com/pasztorpisti/qs"
	log "github.com/sirupsen/logrus"
)

var json = jsoniter.ConfigCompatibleWithStandardLibrary

var mutex = &sync.Mutex{}

var auth string

type AuthResp struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

var InvalidRespTwitch = fmt.Errorf("invalid resp from twitch")

func GetAuth() (string, error) {
	mutex.Lock()
	defer mutex.Unlock()
	if auth != "" {
		return auth, nil
	}

	val, err := redis.Client.Get(redis.Ctx, "twitch:auth").Result()
	if err != nil && err != redis.ErrNil {
		return "", err
	}
	if val != "" {
		return val, nil
	}
	params, _ := qs.Marshal(map[string]string{
		"client_id":     configure.Config.GetString("twitch_client_id"),
		"client_secret": configure.Config.GetString("twitch_client_secret"),
		"grant_type":    "client_credentials",
	})
	u, _ := url.Parse(fmt.Sprintf("https://id.twitch.tv/oauth2/token?%s", params))
	resp, err := http.DefaultClient.Do(&http.Request{
		Method: "POST",
		URL:    u,
	})
	if err != nil {
		return "", err
	}

	defer resp.Body.Close()

	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode > 200 {
		log.Errorf("invalid, resp from twitch, resp=%s", string(data))
		return "", InvalidRespTwitch
	}

	resData := AuthResp{}
	if err := json.Unmarshal(data, &resData); err != nil {
		return "", err
	}

	auth = resData.AccessToken

	expiry := time.Second * time.Duration(int64(float64(resData.ExpiresIn)*0.75))

	if err := redis.Client.SetNX(redis.Ctx, "twitch:vods:auth", auth, expiry).Err(); err != nil {
		log.Errorf("redis, err=%e", err)
	}

	go func() {
		if int64(float64(resData.ExpiresIn)*0.75) < 3600 {
			time.Sleep(time.Second * time.Duration(int64(float64(resData.ExpiresIn)*0.75)))
		} else {
			time.Sleep(time.Second * time.Duration(3600))
		}
		mutex.Lock()
		defer mutex.Unlock()
		auth = ""
	}()
	return auth, nil
}
