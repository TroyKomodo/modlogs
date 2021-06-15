package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	log "github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/bson"

	"github.com/troydota/modlogs/src/api"
	"github.com/troydota/modlogs/src/auth"
	"github.com/troydota/modlogs/src/bot"
	"github.com/troydota/modlogs/src/configure"
	"github.com/troydota/modlogs/src/mongo"
	_ "github.com/troydota/modlogs/src/redis"
	"github.com/troydota/modlogs/src/server"
)

func main() {
	log.Infoln("Application Starting...")

	configCode := configure.Config.GetInt("exit_code")
	if configCode > 125 || configCode < 0 {
		log.Warnf("Invalid exit code specified in config (%v), using 0 as new exit code.", configCode)
		configCode = 0
	}

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM, syscall.SIGINT)

	s := server.New()

	b := bot.New()

	go func() {
		sig := <-c
		log.Infof("sig=%v, gracefully shutting down...", sig)
		start := time.Now().UnixNano()

		wg := sync.WaitGroup{}

		wg.Wait()

		wg.Add(2)

		go func() {
			defer wg.Done()
			if err := s.Shutdown(); err != nil {
				log.WithError(err).Error("failed to shutdown server")
			}
		}()

		go func() {
			defer wg.Done()
			if err := b.Shutdown(); err != nil {
				log.WithError(err).Error("failed to shutdown bot")
			}
		}()

		wg.Wait()

		log.Infof("Shutdown took, %.2fms", float64(time.Now().UnixNano()-start)/10e5)
		os.Exit(configCode)
	}()

	fixHooks()

	log.Infoln("Application Started.")

	select {}
}

type hookGet struct {
	Total int32         `json:"total"`
	Data  []hookGetData `json:"data"`
}

type hookGetData struct {
	ID        string                 `json:"id"`
	Status    string                 `json:"status"`
	Type      string                 `json:"type"`
	Version   string                 `json:"version"`
	Condition map[string]interface{} `json:"condition"`
	CreatedAt time.Time              `json:"created_at"`
	Transport map[string]interface{} `json:"transport"`
	Cost      int32                  `json:"cost"`
}

func fixHooks() {
	req, err := http.NewRequest("GET", "https://api.twitch.tv/helix/eventsub/subscriptions", nil)
	if err != nil {
		log.WithError(err).Fatal("fix tokens")
	}
	req.Header.Add("Client-Id", configure.Config.GetString("twitch_client_id"))
	token, err := auth.GetAuth(context.Background())
	if err != nil {
		log.WithError(err).Fatal("fix tokens")
	}
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", token))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.WithError(err).Fatal("fix tokens")
	}
	defer resp.Body.Close()
	rawData, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.WithError(err).Fatal("fix tokens")
	}
	data := hookGet{}
	err = json.Unmarshal(rawData, &data)
	if err != nil {
		log.WithError(err).Fatal("fix tokens")
	}
	hooks := []mongo.Hook{}
	cur, err := mongo.Database.Collection("hooks").Find(context.Background(), bson.M{})
	if err == nil {
		err = cur.All(context.Background(), &hooks)
	}
	if err != nil {
		log.WithError(err).Fatal("fix tokens")
	}
	streamers := map[string]bool{}
	streamerHooks := map[string]map[string]bool{}
	for _, h := range hooks {
		streamers[h.StreamerID] = true
		if _, ok := streamerHooks[h.StreamerID]; !ok {
			streamerHooks[h.StreamerID] = map[string]bool{}
		}
	}
	deleteIDs := []string{}
	for _, h := range data.Data {
		streamerID, ok := h.Condition["broadcaster_user_id"].(string)
		if !ok {
			continue
		}
		if _, ok := streamerHooks[streamerID]; !ok || h.Status != "enabled" {
			deleteIDs = append(deleteIDs, h.ID)
			continue
		}
		streamerHooks[streamerID][h.Type] = true
	}
	for _, id := range deleteIDs {
		req, err := http.NewRequest("DELETE", fmt.Sprintf("https://api.twitch.tv/helix/eventsub/subscriptions?id=%s", id), nil)
		if err != nil {
			log.WithError(err).Fatal("fix tokens")
		}
		req.Header.Add("Client-Id", configure.Config.GetString("twitch_client_id"))
		req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", token))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.WithError(err).Fatal("fix tokens")
		}
		if resp.StatusCode > 300 {
			data, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				log.WithError(err).Fatal("fix tokens")
			}
			log.WithField("data", string(data)).Fatal("fix tokens")
		}
		resp.Body.Close()
	}
	validHooks := []api.Hook{
		{Name: "channel.ban", Version: "1"},
		{Name: "channel.unban", Version: "1"},
		{Name: "channel.moderator.add", Version: "1"},
		{Name: "channel.moderator.remove", Version: "1"},
	}
	for streamerID, activeHooks := range streamerHooks {
		hooksToAdd := []api.Hook{}
		for _, v := range validHooks {
			if !activeHooks[v.Name] {
				hooksToAdd = append(hooksToAdd, v)
			}
		}
		if len(hooksToAdd) != 0 {
			err = api.CreateWebhooks(context.Background(), streamerID, hooksToAdd...)
			if err != nil {
				log.WithError(err).Error("fix tokens")
				_, err = mongo.Database.Collection("hooks").DeleteMany(context.Background(), bson.M{"streamer_id": streamerID})
				if err != nil {
					log.WithError(err).Fatal("fix tokens")
				}
			}
		}
	}
}
