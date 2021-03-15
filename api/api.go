package api

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/hashicorp/go-multierror"
	"github.com/pasztorpisti/qs"
	"github.com/troydota/modlogs/auth"
	"github.com/troydota/modlogs/configure"
	"github.com/troydota/modlogs/redis"
	"github.com/troydota/modlogs/utils"

	jsoniter "github.com/json-iterator/go"
	log "github.com/sirupsen/logrus"
)

var json = jsoniter.ConfigCompatibleWithStandardLibrary

type TwitchUserResp struct {
	Data []TwitchUser `json:"data"`
}

type TwitchUser struct {
	ID              string    `json:"id"`
	Login           string    `json:"login"`
	DisplayName     string    `json:"display_name"`
	BroadcasterType string    `json:"broadcaster_type"`
	Description     string    `json:"description"`
	ProfileImageURL string    `json:"profile_image_url"`
	OfflineImageURL string    `json:"offline_image_url"`
	ViewCount       int       `json:"view_count"`
	CreatedAt       time.Time `json:"created_at"`
}

func GetUsers(oauth string, ids []string, logins []string) ([]TwitchUser, error) {
	returnv := []TwitchUser{}
	for len(ids) != 0 || len(logins) != 0 {
		var temp []string
		var temp2 []string
		if len(ids) > 100 {
			temp = ids[:100]
			ids = ids[100:]
		} else {
			temp = ids
			ids = []string{}
			if len(logins)+len(temp) > 100 {
				temp2 = logins[:100-len(temp)]
				logins = logins[100-len(temp):]
			} else {
				temp2 = logins
				logins = []string{}
			}
		}

		params, _ := qs.Marshal(map[string][]string{
			"id":    temp,
			"login": temp2,
		})

		u, _ := url.Parse(fmt.Sprintf("https://api.twitch.tv/helix/users?%s", params))

		var token string
		var err error

		if oauth == "" {
			token, err = auth.GetAuth()
			if err != nil {
				return nil, err
			}
		} else {
			token = oauth
		}

		resp, err := http.DefaultClient.Do(&http.Request{
			Method: "GET",
			URL:    u,
			Header: http.Header{
				"Client-Id":     []string{configure.Config.GetString("twitch_client_id")},
				"Authorization": []string{fmt.Sprintf("Bearer %s", token)},
			},
		})
		if err != nil {
			return nil, err
		}

		defer resp.Body.Close()

		data, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}

		respData := TwitchUserResp{}

		if err := json.Unmarshal(data, &respData); err != nil {
			return nil, err
		}
		returnv = append(returnv, respData.Data...)
	}

	if oauth != "" && len(ids) == 0 && len(logins) == 0 {
		u, _ := url.Parse("https://api.twitch.tv/helix/users")

		var err error

		resp, err := http.DefaultClient.Do(&http.Request{
			Method: "GET",
			URL:    u,
			Header: http.Header{
				"Client-Id":     []string{configure.Config.GetString("twitch_client_id")},
				"Authorization": []string{fmt.Sprintf("Bearer %s", oauth)},
			},
		})
		if err != nil {
			return nil, err
		}

		defer resp.Body.Close()

		data, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}

		respData := TwitchUserResp{}

		if err := json.Unmarshal(data, &respData); err != nil {
			return nil, err
		}
		return respData.Data, nil
	}

	return returnv, nil
}

// {
//     "type": "channel.ban",
//     "version": "1",
//     "condition": {
//         "broadcaster_user_id": "121903137"
//     },
//     "transport": {
//         "method": "webhook",
//         "callback": "https://modlogs.komodohype.dev/webhook/channel.ban/121903137",
//         "secret": "5353208469b4788087d51f2a167fdf7b338f40af20cc05f8f65dacbdf792ee92"
//     }
// }

type TwitchWebhookRequest struct {
	Type      string                  `json:"type"`
	Version   string                  `json:"version"`
	Condition map[string]interface{}  `json:"condition"`
	Transport TwitchCallbackTransport `json:"transport"`
}

type TwitchCallbackTransport struct {
	Method   string `json:"method"`
	Callback string `json:"callback"`
	Secret   string `json:"secret"`
}

func CreateWebhooks(streamerID string) error {
	secret, err := utils.GenerateRandomString(64)
	token, err := auth.GetAuth()
	if err != nil {
		return err
	}

	cb := func(t string, v string) error {
		data, err := json.Marshal(TwitchWebhookRequest{
			Type:    t,
			Version: v,
			Condition: map[string]interface{}{
				"broadcaster_user_id": streamerID,
			},
			Transport: TwitchCallbackTransport{
				Method:   "webhook",
				Callback: fmt.Sprintf("%s/webhook/%s/%s", configure.Config.GetString("website_url"), t, streamerID),
				Secret:   secret,
			},
		})
		if err != nil {
			return err
		}
		req, err := http.NewRequest("POST", "https://api.twitch.tv/helix/eventsub/subscriptions", bytes.NewBuffer(data))
		if err != nil {
			log.Errorf("req, err=%e", err)
			return err
		}

		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
		req.Header.Set("Client-ID", configure.Config.GetString("twitch_client_id"))
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Errorf("resp, err=%e", err)
			return err
		}

		if resp.StatusCode > 300 {
			data, err := ioutil.ReadAll(resp.Body)
			log.Errorf("twitch, body=%s, err=%e", data, err)
			return fmt.Errorf("invalid resp from twitch")
		}

		return nil
	}

	wg := &sync.WaitGroup{}

	hooks := []struct {
		t string
		v string
	}{
		{"channel.ban", "1"},
		{"channel.unban", "1"},
		{"channel.moderator.add", "beta"},
		{"channel.moderator.remove", "beta"},
	}

	redisCb := make(chan struct{})
	errored := false

	pipe := redis.Client.Pipeline()

	wg.Add(len(hooks))
	mtx := sync.Mutex{}
	for _, h := range hooks {
		key := fmt.Sprintf("webhook:twitch:%s:%s", h.t, streamerID)
		cmd := pipe.HSet(redis.Ctx, key, "secret", secret)
		go func(t, v string) {
			<-redisCb
			if err := cmd.Err(); err != nil {
				log.Errorf("redis, err=%e, key=%s", err, key)
			}
			defer wg.Done()
			if errored {
				return
			}
			e := cb(t, v)
			if e != nil {
				mtx.Lock()
				err = multierror.Append(err, e)
				mtx.Unlock()
			}
		}(h.t, h.v)
	}

	_, err = pipe.Exec(redis.Ctx)
	if err != nil {
		log.Errorf("redis, err=%e", err)
		errored = true
	}

	close(redisCb)

	wg.Wait()

	return err
}

func RevokeWebhook(streamerID string) error {
	token, err := auth.GetAuth()
	if err != nil {
		return err
	}

	cb := func(id string) error {
		req, err := http.NewRequest("DELETE", fmt.Sprintf("https://api.twitch.tv/helix/eventsub/subscriptions?id=%s", id), nil)
		if err != nil {
			log.Errorf("req, err=%e", err)
			return err
		}

		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
		req.Header.Set("Client-ID", configure.Config.GetString("twitch_client_id"))

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Errorf("resp, err=%e", err)
			return err
		}

		if resp.StatusCode > 300 {
			data, err := ioutil.ReadAll(resp.Body)
			log.Errorf("twitch, body=%s, err=%e", data, err)
			return fmt.Errorf("invalid resp from twitch")
		}

		return nil
	}

	wg := &sync.WaitGroup{}

	hooks := []string{
		"channel.ban",
		"channel.unban",
		"channel.moderator.add",
		"channel.moderator.remove",
	}

	redisCb := make(chan struct{})
	errored := false

	pipe := redis.Client.Pipeline()

	wg.Add(len(hooks))
	mtx := sync.Mutex{}
	for _, h := range hooks {
		cmd := pipe.HGet(redis.Ctx, fmt.Sprintf("webhook:twitch:%s:%s", h, streamerID), "id")
		go func(cmd *redis.StringCmd) {
			<-redisCb
			defer wg.Done()
			if errored {
				return
			}
			e := cb(cmd.Val())
			if e != nil {
				mtx.Lock()
				err = multierror.Append(err, e)
				mtx.Unlock()
			}
		}(cmd)
	}

	_, err = pipe.Exec(redis.Ctx)
	if err != nil {
		log.Errorf("redis, err=%e", err)
		errored = true
	}

	close(redisCb)

	wg.Wait()

	return err
}
