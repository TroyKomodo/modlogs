package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/troydota/modlogs/src/bot"
	"github.com/troydota/modlogs/src/mongo"
	"github.com/troydota/modlogs/src/redis"
	"github.com/troydota/modlogs/src/utils"

	"github.com/troydota/modlogs/src/api"

	"github.com/gofiber/fiber/v2"
	"github.com/troydota/modlogs/src/configure"

	"github.com/pasztorpisti/qs"

	jsoniter "github.com/json-iterator/go"
	log "github.com/sirupsen/logrus"
)

var json = jsoniter.ConfigCompatibleWithStandardLibrary

type TwitchTokenResp struct {
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
	ExpiresIn    int      `json:"expires_in"`
	Scope        []string `json:"scope"`
	TokenType    string   `json:"token_type"`
}

type TwitchCallback struct {
	Challenge    string                     `json:"challenge"`
	Subscription TwitchCallbackSubscription `json:"subscription"`
	Event        map[string]interface{}     `json:"event"`
}

type TwitchCallbackSubscription struct {
	ID        string                  `json:"id"`
	Status    string                  `json:"status"`
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

func Twitch(app fiber.Router) fiber.Router {
	app.Get("/login", func(c *fiber.Ctx) error {
		csrfToken, err := utils.GenerateRandomString(64)
		if err != nil {
			log.WithError(err).Error("secure bytes")
			return c.Status(500).JSON(&fiber.Map{
				"message": "Internal server error.",
				"status":  500,
			})
		}

		scopes := []string{}

		scopes = append(scopes, "channel:moderate", "moderation:read")

		c.Cookie(&fiber.Cookie{Name: "crsf_token", Value: csrfToken, Domain: configure.Config.GetString("cookie_domain"), Expires: time.Now().Add(time.Second * 300)})

		params, _ := qs.Marshal(map[string]string{
			"client_id":     configure.Config.GetString("twitch_client_id"),
			"redirect_uri":  configure.Config.GetString("twitch_redirect_uri"),
			"response_type": "code",
			"scope":         strings.Join(scopes, " "),
			"state":         csrfToken,
		})

		u := fmt.Sprintf("https://id.twitch.tv/oauth2/authorize?%s", params)

		return c.Redirect(u)
	})

	app.Get("/login/callback", func(c *fiber.Ctx) error {
		twitchToken := c.Query("state")

		if twitchToken == "" {
			return c.Status(400).JSON(&fiber.Map{
				"status":  400,
				"message": "Invalid response from twitch, missing state paramater.",
			})
		}

		sessionToken := c.Cookies("crsf_token")

		if sessionToken == "" {
			return c.Status(400).JSON(&fiber.Map{
				"status":  400,
				"message": "Invalid response from sessiom store.",
			})
		}

		if twitchToken != sessionToken {
			return c.Status(400).JSON(&fiber.Map{
				"status":  400,
				"message": "Invalid response from twitch, csrf_token token missmatch.",
			})
		}

		code := c.Query("code")

		params, _ := qs.Marshal(map[string]string{
			"client_id":     configure.Config.GetString("twitch_client_id"),
			"client_secret": configure.Config.GetString("twitch_client_secret"),
			"redirect_uri":  configure.Config.GetString("twitch_redirect_uri"),
			"code":          code,
			"grant_type":    "authorization_code",
		})

		u, _ := url.Parse(fmt.Sprintf("https://id.twitch.tv/oauth2/token?%s", params))

		resp, err := http.DefaultClient.Do(&http.Request{
			Method: "POST",
			URL:    u,
		})

		if err != nil {
			log.WithError(err).Error("twitch")
			return c.Status(400).JSON(&fiber.Map{
				"status":  400,
				"message": "Invalid response from twitch, failed to convert code to access token.",
			})
		}

		defer resp.Body.Close()

		data, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			log.WithError(err).Error("ioutil")
			return c.Status(400).JSON(&fiber.Map{
				"status":  400,
				"message": "Invalid response from twitch, failed to convert code to access token.",
			})
		}

		tokenResp := TwitchTokenResp{}

		if err := json.Unmarshal(data, &tokenResp); err != nil {
			log.WithError(err).WithField("data", data).WithField("url", u).Error("twitch")
			return c.Status(400).JSON(&fiber.Map{
				"status":  400,
				"message": "Invalid response from twitch, failed to convert code to access token.",
			})
		}

		data, _ = json.Marshal(tokenResp)

		users, err := api.GetUsers(c.Context(), tokenResp.AccessToken, nil, nil)
		if err != nil || len(users) != 1 {
			log.WithError(err).WithField("resp", users).WithField("token", tokenResp).Error("twitch")
			return c.Status(400).JSON(&fiber.Map{
				"status":  400,
				"message": "Invalid response from twitch, failed to convert access token to user account.",
			})
		}

		user := users[0]

		exp := int64(math.Floor(float64(tokenResp.ExpiresIn) * 0.7))

		opts := options.Update().SetUpsert(true)
		mUser := &mongo.User{
			ID:    user.ID,
			Name:  user.DisplayName,
			Login: user.Login,
		}
		if _, err := mongo.Database.Collection("users").UpdateOne(c.Context(), bson.M{"$or": bson.A{
			bson.M{"id": user.ID},
			bson.M{"login": user.Login},
		}}, bson.M{"$set": mUser}, opts); err != nil {
			log.WithError(err).Error("mongo")
			return c.Status(500).JSON(&fiber.Map{
				"status":  500,
				"message": "Failed to save user data.",
			})
		}

		if err := redis.Client.HSet(c.Context(), "oauth:streamer", user.ID, fmt.Sprintf("%v %s", time.Now().Unix()+exp, string(data))).Err(); err != nil {
			log.WithError(err).Error("redis")
			return c.Status(500).JSON(&fiber.Map{
				"status":  500,
				"message": "Failed to save OAuth token.",
			})
		}

		authCode, _ := uuid.NewRandom()

		if err := redis.Client.SetNX(c.Context(), fmt.Sprintf("temp:codes:%s", authCode), user.ID, time.Second*300).Err(); err != nil {
			log.WithError(err).Error("redis")
			return c.Status(500).JSON(&fiber.Map{
				"status":  500,
				"message": "Failed to save temp secret.",
			})
		}

		c.Set("Content-Type", "text/html")

		jsonData, _ := json.MarshalIndent(fiber.Map{
			"`status*":  `status_code`,
			"`message*": "message_content",
			"`command*": "command_content",
			"`link*":    "link_url",
		}, "", "  ")

		status_code := `<span class="json-value">200</span>`

		message := `<span class="json-string">Everything went as planned, to add the bot to your discord you can use the invite link, and then type the command in the channel you want the logs to appear in, the command will expire in 300 seconds.</span>`

		command := fmt.Sprintf(`<span class="json-string">/add token: %s</span>`, authCode.String())

		css := `
.json-key {
	color: brown;
}
.json-value {
	color: green;
}
.json-string {
	color: teal;
}`

		jsonStr := strings.Replace(strings.Replace(strings.Replace(strings.Replace(strings.ReplaceAll(strings.ReplaceAll(string(jsonData), "\"`", `<span class="json-key">"`), "*\"", "\"</span>"), "\"status_code\"", status_code, 1), "message_content", message, 1), "command_content", command, 1), "link_url", fmt.Sprintf(`<a href="%s">%s</a>`, configure.Config.GetString("website_url"), configure.Config.GetString("website_url")), 1)

		return c.Status(200).Send([]byte(fmt.Sprintf(`<style>%s</style><pre><code>%s</code></pre>`, css, jsonStr)))
	})

	app.Post("/webhook/:type/:id", func(c *fiber.Ctx) error {
		key := fmt.Sprintf("webhook:twitch:%s:%s", c.Params("type"), c.Params("id"))

		res, err := redis.Client.HGet(c.Context(), key, "secret").Result()
		if err != nil {
			if err != redis.ErrNil {
				log.WithError(err).Error("redis")
				return c.SendStatus(500)
			}
			return c.SendStatus(404)
		}

		if res == "" {
			return c.SendStatus(404)
		}

		t, err := time.Parse(time.RFC3339, c.Get("Twitch-Eventsub-Message-Timestamp"))
		if err != nil || t.Before(time.Now().Add(-10*time.Minute)) {
			return c.SendStatus(400)
		}

		msgID := c.Get("Twitch-Eventsub-Message-Id")

		if msgID == "" {
			return c.SendStatus(400)
		}

		body := c.Body()

		hmacMessage := fmt.Sprintf("%s%s%s", msgID, c.Get("Twitch-Eventsub-Message-Timestamp"), body)

		h := hmac.New(sha256.New, []byte(res))

		// Write Data to it
		h.Write([]byte(hmacMessage))

		// Get result and encode as hexadecimal string
		sha := hex.EncodeToString(h.Sum(nil))

		if c.Get("Twitch-Eventsub-Message-Signature") != fmt.Sprintf("sha256=%s", sha) {
			return c.SendStatus(403)
		}

		newKey := fmt.Sprintf("twitch:events:%s:%s:%s", c.Params("type"), c.Params("id"), msgID)
		err = redis.Client.Do(c.Context(), "SET", newKey, "1", "NX", "EX", 30*60).Err()
		if err != nil {
			if err != redis.ErrNil {
				log.WithError(err).Error("redis")
				return c.SendStatus(500)
			}
			log.Warnf("Duplicated key=%s", newKey)
			return c.SendStatus(200)
		}

		cleanUp := func(statusCode int, resp string) error {
			if statusCode != 200 {
				if err := redis.Client.Del(c.Context(), newKey).Err(); err != nil {
					log.WithError(err).Error("redis")
				}
			}
			if resp == "" {
				return c.SendStatus(statusCode)
			}
			return c.Status(statusCode).SendString(resp)
		}

		callback := &TwitchCallback{}
		if err := json.Unmarshal(body, callback); err != nil {
			return cleanUp(400, "")
		}

		if callback.Subscription.Status == "authorization_revoked" {
			pipe := redis.Client.Pipeline()
			pipe.Del(c.Context(), key)
			if _, err := pipe.Exec(c.Context()); err != nil {
				log.WithError(err).Error("redis")
			}
			return cleanUp(200, "")
		}

		if callback.Challenge != "" {
			if err := redis.Client.HSet(c.Context(), key, "id", callback.Subscription.ID).Err(); err != nil {
				log.WithError(err).Error("redis")
				return cleanUp(500, "")
			}
			return cleanUp(200, callback.Challenge)
		}

		req := bot.WebhookRequest{
			CreatedAt:     t,
			BroadcasterID: c.Params("id"),
			Action:        callback.Subscription.Type,
		}

		if callback.Subscription.Type == "channel.ban" {
			var ok bool
			req.BroadcasterUserName, ok = callback.Event["broadcaster_user_name"].(string)
			if !ok {
				return cleanUp(400, "")
			}
			req.UserName, ok = callback.Event["user_name"].(string)
			if !ok {
				return cleanUp(400, "")
			}
			req.Reason, ok = callback.Event["reason"].(string)
			if !ok {
				return cleanUp(400, "")
			}
			req.ModeratorUserName, ok = callback.Event["moderator_user_name"].(string)
			if !ok {
				return cleanUp(400, "")
			}
			req.ModeratorID, ok = callback.Event["moderator_user_id"].(string)
			if !ok {
				return cleanUp(400, "")
			}
			if v, ok := callback.Event["is_permanent"].(bool); ok && !v {
				exp, ok := callback.Event["ends_at"].(string)
				if !ok {
					return cleanUp(400, "")
				}
				t, err := time.Parse(time.RFC3339, exp)
				if err != nil {
					return cleanUp(400, "")
				}
				req.Expires = &t
			}
		} else if callback.Subscription.Type == "channel.unban" {
			var ok bool
			req.BroadcasterUserName, ok = callback.Event["broadcaster_user_name"].(string)
			if !ok {
				return cleanUp(400, "")
			}
			req.UserName, ok = callback.Event["user_name"].(string)
			if !ok {
				return cleanUp(400, "")
			}
			req.ModeratorUserName, ok = callback.Event["moderator_user_name"].(string)
			if !ok {
				return cleanUp(400, "")
			}
			req.ModeratorID, ok = callback.Event["moderator_user_id"].(string)
			if !ok {
				return cleanUp(400, "")
			}
		} else if callback.Subscription.Type == "channel.moderator.add" {
			var ok bool
			req.BroadcasterUserName, ok = callback.Event["broadcaster_user_name"].(string)
			if !ok {
				return cleanUp(400, "")
			}
			req.UserName, ok = callback.Event["user_name"].(string)
			if !ok {
				return cleanUp(400, "")
			}
		} else if callback.Subscription.Type == "channel.moderator.remove" {
			var ok bool
			req.BroadcasterUserName, ok = callback.Event["broadcaster_user_name"].(string)
			if !ok {
				return cleanUp(400, "")
			}
			req.UserName, ok = callback.Event["user_name"].(string)
			if !ok {
				return cleanUp(400, "")
			}
		}

		bot.Callback <- req

		return cleanUp(200, "")
	})

	return app
}
