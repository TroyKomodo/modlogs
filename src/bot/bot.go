package bot

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	log "github.com/sirupsen/logrus"
	"github.com/troydota/modlogs/src/api"
	"github.com/troydota/modlogs/src/configure"
	"github.com/troydota/modlogs/src/mongo"
	"github.com/troydota/modlogs/src/redis"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type Command func(b *Bot, m *discordgo.Message) error

type WebhookRequest struct {
	BroadcasterID       string
	BroadcasterUserName string
	ModeratorUserName   string
	ModeratorID         string
	UserName            string
	Reason              string
	Action              string
	Expires             *time.Time
	CreatedAt           time.Time
}

var Callback = make(chan WebhookRequest)

type cmdWrapper struct {
	cmd     *discordgo.ApplicationCommand
	guildID string
	conn    *discordgo.Session
}

func (c *cmdWrapper) Delete() error {
	return c.conn.ApplicationCommandDelete(c.cmd.ApplicationID, c.guildID, c.cmd.ID)
}

type Bot struct {
	conn *discordgo.Session

	commands []*cmdWrapper
	stopped  chan struct{}
	limiter  *rateLimiter
}

var validationWrapper = func(next func(s *discordgo.Session, i *discordgo.InteractionCreate, g *discordgo.Guild)) func(s *discordgo.Session, i *discordgo.InteractionCreate) {
	return func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		var valid bool
		var guild *discordgo.Guild

		for _, g := range s.State.Guilds {
			if g.ID == i.GuildID {
				guild = g
				break
			}
		}

		if guild == nil {
			err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionApplicationCommandResponseData{
					Content: "Internal Server Error. Please try again later...",
					// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
					Flags: 64,
				},
			})
			if err != nil {
				log.WithError(err).Error("discord")
			}
			return
		} else {
			valid = i.Member.User.ID == guild.OwnerID
			if !valid {
			admins:
				for _, u := range configure.Config.GetStringSlice("admins") {
					if u == i.Member.User.ID {
						valid = true
						break admins
					}
				}
				if !valid {
				guild:
					for _, r := range guild.Roles {
						found := false
					member:
						for _, m := range i.Member.Roles {
							if m == r.ID {
								found = true
								break member
							}
						}
						if !found {
							continue guild
						}
						if r.Permissions&discordgo.PermissionAdministrator != 0 {
							valid = true
							break guild
						}
					}
				}
			}
		}
		if valid {
			next(s, i, guild)
		} else {
			err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionApplicationCommandResponseData{
					Content: "You do not have permission to execute that command.",
					// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
					Flags: 64,
				},
			})
			if err != nil {
				log.WithError(err).Error("discord")
			}
		}
	}
}

var (
	commands = []*discordgo.ApplicationCommand{
		{
			Name:        "add",
			Description: fmt.Sprintf("Adds a new twitch moderation hook to log. You can have a maximum of %v", configure.Config.GetInt("max_hooks_per_guild")),
			Options: []*discordgo.ApplicationCommandOption{

				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "token",
					Description: "Token from the login.",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionBoolean,
					Name:        "minimal",
					Description: "Minimal mode.",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionChannel,
					Name:        "channel",
					Description: "Text channel for logging.",
					Required:    false,
				},
			},
		},
		{
			Name:        "list",
			Description: "Shows a list of current hooks in this discord.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionChannel,
					Name:        "channel",
					Description: "Show hooks for this channel.",
					Required:    false,
				},
			},
		},
		{
			Name:        "delete",
			Description: "Shows a list of current hooks in this discord.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "broadcaster",
					Description: "The ID or name of the twitch streamer.",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionChannel,
					Name:        "channel",
					Description: "Text channel where the hook is active.",
					Required:    false,
				},
			},
		},
		{
			Name:        "ignore",
			Description: "Ignore a user, such as a bot.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "user",
					Description: "The id or username of the twitch account.",
					Required:    true,
				},
			},
		},
		{
			Name:        "unignore",
			Description: "Unignore a user that was previously ignored",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "user",
					Description: "The id or username of the twitch account.",
					Required:    true,
				},
			},
		},
		{
			Name:        "ignored",
			Description: "Shows a list of ignored ids.",
		},
		{
			Name:        "link",
			Description: "Responds with the invite link and the login link.",
		},
	}
	commandHandlers = map[string]func(s *discordgo.Session, i *discordgo.InteractionCreate){
		"add": validationWrapper(func(s *discordgo.Session, i *discordgo.InteractionCreate, g *discordgo.Guild) {
			token := i.Data.Options[0].StringValue()
			var channel *discordgo.Channel
			mode := mongo.ModeMinimal

			for _, o := range i.Data.Options {
				if o.Name == "minimal" {
					if !o.BoolValue() {
						mode = mongo.ModeEmbed
					}
				} else if o.Name == "channel" {
					channel = o.ChannelValue(s)
					if channel != nil && channel.Type != discordgo.ChannelTypeGuildText {
						err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
							Type: discordgo.InteractionResponseChannelMessageWithSource,
							Data: &discordgo.InteractionApplicationCommandResponseData{
								Content: "Logs can only be outputted into a text channel.",
								// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
								Flags: 64,
							},
						})
						if err != nil {
							log.WithError(err).Error("discord")
						}
						return
					}
				}
			}

			if channel == nil {
				for _, c := range g.Channels {
					if c.ID == i.ChannelID {
						channel = c
						break
					}
				}
			}

			if channel == nil {
				err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: "Internal server error occured.",
						// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
						Flags: 64,
					},
				})
				if err != nil {
					log.WithError(err).Error("discord")
				}
			}

			userID, err := redis.AuthTokenValues(context.Background(), token)
			if err != nil {
				msg := "Internal server error. Please try again later."
				if err == redis.ErrNil {
					msg = "The token you provided is expired or invalid. Please login again to make a new one."
				} else {
					log.WithError(err).Error("redis")
				}
				err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: msg,
						// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
						Flags: 64,
					},
				})
				if err != nil {
					log.WithError(err).Error("discord")
				}
				return
			}

			user := &mongo.User{}

			filterOr := bson.M{
				"id": userID,
			}

			res := mongo.Database.Collection("users").FindOne(context.Background(), filterOr)

			err = res.Err()
			if err == nil {
				err = res.Decode(user)
			}
			if err == mongo.ErrNoDocuments {
				err = nil
				users, err := api.GetUsers(context.Background(), "", []string{userID}, nil)
				if err != nil {
					log.WithError(err).Error("api")
					err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionApplicationCommandResponseData{
							Content: "Internal server error. Please try again later.",
							// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
							Flags: 64,
						},
					})
					if err != nil {
						log.WithError(err).Error("discord")
					}
					return
				}
				if len(users) == 0 {
					err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionApplicationCommandResponseData{
							Content: "The specified broadcaster does not exist.",
							// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
							Flags: 64,
						},
					})
					if err != nil {
						log.WithError(err).Error("discord")
					}
					return
				}
				u := users[0]
				opts := options.Update().SetUpsert(true)
				user = &mongo.User{
					ID:    u.ID,
					Name:  u.DisplayName,
					Login: u.Login,
				}
				if _, err := mongo.Database.Collection("users").UpdateOne(context.Background(), filterOr, bson.M{"$set": user}, opts); err != nil {
					log.WithError(err).Error("mongo")
					err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionApplicationCommandResponseData{
							Content: "Internal server error. Please try again later.",
							// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
							Flags: 64,
						},
					})
					if err != nil {
						log.WithError(err).Error("discord")
					}
					return
				}
			}

			if err != nil {
				log.WithError(err).Error("mongo")
				err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: "Internal server error. Please try again later.",
						// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
						Flags: 64,
					},
				})
				if err != nil {
					log.WithError(err).Error("discord")
				}
				return
			}

			filter := bson.M{
				"channel_id":  channel.ID,
				"guild_id":    g.ID,
				"streamer_id": user.ID,
			}

			update := bson.M{
				"$set": bson.M{
					"channel_id":  channel.ID,
					"guild_id":    g.ID,
					"streamer_id": user.ID,
					"mode":        mode,
				},
			}

			updateResp, err := mongo.Database.Collection("hooks").UpdateOne(context.Background(), filter, update)
			if err != nil {
				log.WithError(err).Error("mongo")
				err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: "Internal server error. Please try again later.",
						// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
						Flags: 64,
					},
				})
				if err != nil {
					log.WithError(err).Error("discord")
				}
				return
			}

			if updateResp.MatchedCount == 1 {
				err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: fmt.Sprintf("ModLogs hook updated for <https://twitch.tv/%s>, into %s", user.Login, channel.Mention()),
					},
				})
				if err != nil {
					log.WithError(err).Error("discord")
				}
				return
			}

			count, err := mongo.Database.Collection("hooks").CountDocuments(context.Background(), bson.M{
				"guild_id": g.ID,
			})
			if err != nil {
				log.WithError(err).Error("mongo")
				err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: "Internal server error. Please try again later.",
						// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
						Flags: 64,
					},
				})
				if err != nil {
					log.WithError(err).Error("discord")
				}
				return
			}

			max := configure.Config.GetInt64("max_hooks_per_guild")
			if max != -1 && count >= max {
				err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: fmt.Sprintf("There are too many hooks in this discord. (%v/%v)", count, configure.Config.GetInt64("max_hooks_per_guild")),
						// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
						Flags: 64,
					},
				})
				if err != nil {
					log.WithError(err).Error("discord")
				}
				return
			}

			opts := options.Update().SetUpsert(true)

			result, err := mongo.Database.Collection("hooks").UpdateOne(context.Background(), filter, update, opts)
			if err != nil {
				log.WithError(err).Error("mongo")
				err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: "Internal server error. Please try again later.",
						// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
						Flags: 64,
					},
				})
				if err != nil {
					log.WithError(err).Error("discord")
				}
				return
			}
			if result.UpsertedCount == 1 {
				val, err := redis.Client.Incr(context.Background(), fmt.Sprintf("streamers:%s", user.ID)).Result()
				if err != nil {
					log.WithError(err).Error("redis")
					err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionApplicationCommandResponseData{
							Content: "Internal server error. Please try again later.",
							// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
							Flags: 64,
						},
					})
					if err != nil {
						log.WithError(err).Error("discord")
					}
					return
				}
				if val == 1 {
					err := api.CreateWebhooks(context.Background(), user.ID)
					if err != nil {
						log.WithError(err).Error("api")
						err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
							Type: discordgo.InteractionResponseChannelMessageWithSource,
							Data: &discordgo.InteractionApplicationCommandResponseData{
								Content: "Internal server error. Please try again later.",
								// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
								Flags: 64,
							},
						})
						if err != nil {
							log.WithError(err).Error("discord")
						}
						err = redis.Client.Decr(context.Background(), fmt.Sprintf("streamers:%s", user.ID)).Err()
						if err != nil {
							log.WithError(err).Error("redis")
						}
						_, err = mongo.Database.Collection("hooks").DeleteOne(context.Background(), bson.M{
							"_id": result.UpsertedID,
						})
						if err != nil {
							log.WithError(err).Error("mongo")
						}
						return
					}
				}
			}

			err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionApplicationCommandResponseData{
					Content: fmt.Sprintf("ModLogs hook added for <https://twitch.tv/%s>, into %s", user.Login, channel.Mention()),
				},
			})
			if err != nil {
				log.WithError(err).Error("discord")
			}
		}),
		"list": validationWrapper(func(s *discordgo.Session, i *discordgo.InteractionCreate, g *discordgo.Guild) {
			var channel *discordgo.Channel
			for _, o := range i.Data.Options {
				if o.Name == "channel" {
					channel = o.ChannelValue(s)
					if channel != nil && channel.Type != discordgo.ChannelTypeGuildText {
						err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
							Type: discordgo.InteractionResponseChannelMessageWithSource,
							Data: &discordgo.InteractionApplicationCommandResponseData{
								Content: "Please select a valid channel.",
								// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
								Flags: 64,
							},
						})
						if err != nil {
							log.WithError(err).Error("discord")
						}
						return
					}
				}
			}

			filter := bson.M{
				"guild_id": g.ID,
			}

			// msg := fmt.Sprintf("List todo for guild - %s", g.ID)
			// if channel != nil {
			// 	msg = fmt.Sprintf("%s - channel - %s", msg, channel.ID)
			// 	filter["channel_id"] = channel.ID
			// }

			hooks := []*mongo.Hook{}

			cur, err := mongo.Database.Collection("hooks").Find(context.Background(), filter)
			if err == nil {
				err = cur.All(context.Background(), &hooks)
			}
			if err != nil {
				log.WithError(err).Error("mongo")
				err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: "Internal Server Error.",
						// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
						Flags: 64,
					},
				})
				if err != nil {
					log.WithError(err).Error("discord")
				}
				return
			}

			streamersIDsMap := map[string]int{}

			for _, s := range hooks {
				streamersIDsMap[s.StreamerID] = 1
			}
			streamerIDs := []string{}

			for k := range streamersIDsMap {
				streamerIDs = append(streamerIDs, k)
			}

			opts := options.Update().SetUpsert(true)

			filter = bson.M{
				"id": bson.M{
					"$in": streamerIDs,
				},
			}

			users := []*mongo.User{}

			cur, err = mongo.Database.Collection("users").Find(context.Background(), filter)
			if err == nil {
				err = cur.All(context.Background(), &users)
			}
			if err != nil {
				log.WithError(err).Error("mongo")
				err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: "Internal server error. Please try again later.",
						// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
						Flags: 64,
					},
				})
				if err != nil {
					log.WithError(err).Error("discord")
				}
				return
			}

			if len(users) != len(streamerIDs) {
				// find missing ones
				missingIDs := []string{}
				for _, id := range streamerIDs {
					found := false
					for _, u := range users {
						if u.ID == id {
							found = true
							break
						}
					}
					if !found {
						missingIDs = append(missingIDs, id)
					}
				}
				apiUsers, err := api.GetUsers(context.Background(), "", missingIDs, nil)
				if err != nil {
					log.WithError(err).Error("api")
					err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionApplicationCommandResponseData{
							Content: "Internal server error. Please try again later.",
							// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
							Flags: 64,
						},
					})
					if err != nil {
						log.WithError(err).Error("discord")
					}
				}
				wg := sync.WaitGroup{}
				wg.Add(len(apiUsers))

				for _, u := range apiUsers {
					go func(u api.TwitchUser) {
						defer wg.Done()
						user := &mongo.User{
							ID:    u.ID,
							Name:  u.DisplayName,
							Login: u.Login,
						}
						users = append(users, user)
						if _, err := mongo.Database.Collection("users").UpdateOne(context.Background(), bson.M{
							"$or": bson.A{
								bson.M{"id": u.ID},
								bson.M{"login": u.Login},
							},
						}, bson.M{"$set": user}, opts); err != nil {
							log.WithError(err).Error("mongo")
						}
					}(u)
				}
				wg.Done()
			}

			usrStr := make([]string, len(users))
			for i, user := range users {
				usrStr[i] = user.Name
			}

			lines := []string{}

			streamers := map[string][]*mongo.Hook{}
			for _, hook := range hooks {
				v, ok := streamers[hook.StreamerID]
				if !ok {
					v = []*mongo.Hook{}
					streamers[hook.StreamerID] = v
				}
				streamers[hook.StreamerID] = append(v, hook)
			}

			for _, v := range users {
				channels := []string{}
				for _, h := range streamers[v.ID] {
					mode := "minimal"
					if h.Mode == mongo.ModeEmbed {
						mode = "embed"
					}
					channels = append(channels, fmt.Sprintf("<#%s> - %s", h.ChannelID, mode))
				}
				lines = append(lines, fmt.Sprintf(`<https://twitch.tv/%s> -> %s`, v.Login, strings.Join(channels, ", ")))
			}

			if len(lines) == 0 {
				lines = append(lines, "No hooks were found")
			}

			err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionApplicationCommandResponseData{
					Content: strings.Join(lines, "\n"),
				},
			})
			if err != nil {
				log.WithError(err).Error("discord")
			}
		}),
		"delete": validationWrapper(func(s *discordgo.Session, i *discordgo.InteractionCreate, g *discordgo.Guild) {
			var broadcaster string
			var channel *discordgo.Channel

			for _, o := range i.Data.Options {
				if o.Name == "broadcaster" {
					broadcaster = o.StringValue()
				}
				if o.Name == "channel" {
					channel = o.ChannelValue(s)
					if channel != nil && channel.Type != discordgo.ChannelTypeGuildText {
						err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
							Type: discordgo.InteractionResponseChannelMessageWithSource,
							Data: &discordgo.InteractionApplicationCommandResponseData{
								Content: "Logs can only be outputted into a text channel.",
								// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
								Flags: 64,
							},
						})
						if err != nil {
							log.WithError(err).Error("discord")
						}
						return
					}
				}
			}

			filter := bson.M{
				"guild_id":    g.ID,
				"streamer_id": broadcaster,
			}

			var channelID string
			if channel != nil {
				channelID = channel.ID
				filter["channel_id"] = channelID
			}

			broadcaster = strings.ToLower(broadcaster)

			delres, err := mongo.Database.Collection("hooks").DeleteMany(context.Background(), filter)
			if err != nil {
				log.WithError(err).Error("mongo")
				err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: "Internal server error. Please try again later.",
						// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
						Flags: 64,
					},
				})
				if err != nil {
					log.WithError(err).Error("discord")
				}
				return
			}

			if delres.DeletedCount > 0 {
				plural := " has"
				if delres.DeletedCount > 1 {
					plural = "s have"
				}
				err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: fmt.Sprintf("The hook%s been removed.", plural),
					},
				})
				if err != nil {
					log.WithError(err).Error("discord")
				}
				return
			}

			user := &mongo.User{}

			filterOr := bson.M{
				"$or": bson.A{
					bson.M{"id": broadcaster},
					bson.M{"login": broadcaster},
				},
			}

			res := mongo.Database.Collection("users").FindOne(context.Background(), filterOr)

			err = res.Err()
			if err == nil {
				err = res.Decode(user)
			}
			if err == mongo.ErrNoDocuments {
				err = nil
				ids := []string{}
				if _, err := strconv.ParseInt(broadcaster, 10, 64); err == nil {
					ids = append(ids, broadcaster)
				}

				users, err := api.GetUsers(context.Background(), "", ids, []string{broadcaster})
				if err != nil {
					log.WithError(err).Error("api")
					err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionApplicationCommandResponseData{
							Content: "Internal server error. Please try again later.",
							// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
							Flags: 64,
						},
					})
					if err != nil {
						log.WithError(err).Error("discord")
					}
					return
				}
				if len(users) == 0 {
					err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionApplicationCommandResponseData{
							Content: "The specified user does not exist.",
							// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
							Flags: 64,
						},
					})
					if err != nil {
						log.WithError(err).Error("discord")
					}
					return
				}
				u := users[0]
				opts := options.Update().SetUpsert(true)
				user = &mongo.User{
					ID:    u.ID,
					Name:  u.DisplayName,
					Login: u.Login,
				}
				if _, err := mongo.Database.Collection("users").UpdateOne(context.Background(), filterOr, bson.M{"$set": user}, opts); err != nil {
					log.WithError(err).Error("mongo")
					err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionApplicationCommandResponseData{
							Content: "Internal server error. Please try again later.",
							// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
							Flags: 64,
						},
					})
					if err != nil {
						log.WithError(err).Error("discord")
					}
					return
				}
			}

			if err != nil {
				log.WithError(err).Error("mongo")
				err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: "Internal server error. Please try again later.",
						// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
						Flags: 64,
					},
				})
				if err != nil {
					log.WithError(err).Error("discord")
				}
				return
			}

			filter["streamer_id"] = user.ID

			delres, err = mongo.Database.Collection("hooks").DeleteMany(context.Background(), filter)
			if err != nil {
				log.WithError(err).Error("mongo")
				err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: "Internal server error. Please try again later.",
						// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
						Flags: 64,
					},
				})
				if err != nil {
					log.WithError(err).Error("discord")
				}
				return
			}

			if delres.DeletedCount > 0 {
				plural := " has"
				if delres.DeletedCount > 1 {
					plural = "s have"
				}
				err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: fmt.Sprintf("The hook%s been removed.", plural),
					},
				})
				if err != nil {
					log.WithError(err).Error("discord")
				}

				val, err := redis.Client.DecrBy(context.Background(), fmt.Sprintf("streamers:%s", user.ID), delres.DeletedCount).Result()
				if err != nil {
					log.WithError(err).Error("redis")
				} else if val == 0 {
					if err := api.RevokeWebhook(context.Background(), user.ID); err != nil {
						log.WithError(err).Error("api")
					}
				}
				return
			}

			err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionApplicationCommandResponseData{
					Content: "That hook doesn't exist",
					// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
					Flags: 64,
				},
			})
			if err != nil {
				log.WithError(err).Error("discord")
			}
		}),
		"ignore": validationWrapper(func(s *discordgo.Session, i *discordgo.InteractionCreate, g *discordgo.Guild) {
			var userInput string

			for _, o := range i.Data.Options {
				if o.Name == "user" {
					userInput = o.StringValue()
				}
			}

			if userInput == "" {
				err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: "Please enter a valid user.",
						// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
						Flags: 64,
					},
				})
				if err != nil {
					log.WithError(err).Error("discord")
				}
				return
			}

			user := &mongo.User{}

			filterOr := bson.M{
				"$or": bson.A{
					bson.M{"id": userInput},
					bson.M{"login": userInput},
				},
			}

			res := mongo.Database.Collection("users").FindOne(context.Background(), filterOr)

			err := res.Err()
			if err == nil {
				err = res.Decode(user)
			}
			if err == mongo.ErrNoDocuments {
				err = nil
				ids := []string{}
				if _, err := strconv.ParseInt(userInput, 10, 64); err == nil {
					ids = append(ids, userInput)
				}

				users, err := api.GetUsers(context.Background(), "", ids, []string{userInput})
				if err != nil {
					log.WithError(err).Error("api")
					err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionApplicationCommandResponseData{
							Content: "Internal server error. Please try again later.",
							// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
							Flags: 64,
						},
					})
					if err != nil {
						log.WithError(err).Error("discord")
					}
					return
				}
				if len(users) == 0 {
					err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionApplicationCommandResponseData{
							Content: "The specified user does not exist.",
							// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
							Flags: 64,
						},
					})
					if err != nil {
						log.WithError(err).Error("discord")
					}
					return
				}
				u := users[0]
				opts := options.Update().SetUpsert(true)
				user = &mongo.User{
					ID:    u.ID,
					Name:  u.DisplayName,
					Login: u.Login,
				}
				if _, err := mongo.Database.Collection("users").UpdateOne(context.Background(), filterOr, bson.M{"$set": user}, opts); err != nil {
					log.WithError(err).Error("mongo")
					err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionApplicationCommandResponseData{
							Content: "Internal server error. Please try again later.",
							// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
							Flags: 64,
						},
					})
					if err != nil {
						log.WithError(err).Error("discord")
					}
					return
				}
			}

			if err != nil {
				log.WithError(err).Error("mongo")
				err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: "Internal server error. Please try again later.",
						// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
						Flags: 64,
					},
				})
				if err != nil {
					log.WithError(err).Error("discord")
				}
				return
			}

			if err := redis.Client.SAdd(context.Background(), fmt.Sprintf("ignored-users:%s", g.ID), user.ID).Err(); err != nil {
				log.WithError(err).Error("redis")
				err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: "Internal server error.",
						// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
						Flags: 64,
					},
				})
				if err != nil {
					log.WithError(err).Error("discord")
				}
				return
			}
			err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionApplicationCommandResponseData{
					Content: fmt.Sprintf("Successfully ignored `%s`.", user.Name),
				},
			})
			if err != nil {
				log.WithError(err).Error("discord")
			}
		}),
		"unignore": validationWrapper(func(s *discordgo.Session, i *discordgo.InteractionCreate, g *discordgo.Guild) {
			var userInput string

			for _, o := range i.Data.Options {
				if o.Name == "user" {
					userInput = o.StringValue()
				}
			}

			if userInput == "" {
				err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: "Please enter a valid user.",
						// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
						Flags: 64,
					},
				})
				if err != nil {
					log.WithError(err).Error("discord")
				}
				return
			}

			user := &mongo.User{}

			filterOr := bson.M{
				"$or": bson.A{
					bson.M{"id": userInput},
					bson.M{"login": userInput},
				},
			}

			res := mongo.Database.Collection("users").FindOne(context.Background(), filterOr)

			err := res.Err()
			if err == nil {
				err = res.Decode(user)
			}
			if err == mongo.ErrNoDocuments {
				err = nil
				ids := []string{}
				if _, err := strconv.ParseInt(userInput, 10, 64); err == nil {
					ids = append(ids, userInput)
				}

				users, err := api.GetUsers(context.Background(), "", ids, []string{userInput})
				if err != nil {
					log.WithError(err).Error("api")
					err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionApplicationCommandResponseData{
							Content: "Internal server error. Please try again later.",
							// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
							Flags: 64,
						},
					})
					if err != nil {
						log.WithError(err).Error("discord")
					}
					return
				}
				if len(users) == 0 {
					err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionApplicationCommandResponseData{
							Content: "The specified user does not exist.",
							// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
							Flags: 64,
						},
					})
					if err != nil {
						log.WithError(err).Error("discord")
					}
					return
				}
				u := users[0]
				opts := options.Update().SetUpsert(true)
				user = &mongo.User{
					ID:    u.ID,
					Name:  u.DisplayName,
					Login: u.Login,
				}
				if _, err := mongo.Database.Collection("users").UpdateOne(context.Background(), filterOr, bson.M{"$set": user}, opts); err != nil {
					log.WithError(err).Error("mongo")
					err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionApplicationCommandResponseData{
							Content: "Internal server error. Please try again later.",
							// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
							Flags: 64,
						},
					})
					if err != nil {
						log.WithError(err).Error("discord")
					}
					return
				}
			}

			if err != nil {
				log.WithError(err).Error("mongo")
				err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: "Internal server error. Please try again later.",
						// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
						Flags: 64,
					},
				})
				if err != nil {
					log.WithError(err).Error("discord")
				}
				return
			}

			if err := redis.Client.SRem(context.Background(), fmt.Sprintf("ignored-users:%s", g.ID), user.ID).Err(); err != nil {
				log.WithError(err).Error("redis")
				err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: "Internal server error.",
						// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
						Flags: 64,
					},
				})
				if err != nil {
					log.WithError(err).Error("discord")
				}
				return
			}
			err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionApplicationCommandResponseData{
					Content: fmt.Sprintf("Successfully unignored `%s`.", user.Name),
					// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
					Flags: 64,
				},
			})
			if err != nil {
				log.WithError(err).Error("discord")
			}
		}),
		"ignored": validationWrapper(func(s *discordgo.Session, i *discordgo.InteractionCreate, g *discordgo.Guild) {
			val, err := redis.Client.SMembers(context.Background(), fmt.Sprintf("ignored-users:%s", g.ID)).Result()
			if err != nil || len(val) == 0 {
				msg := "Internal server error."
				if err == redis.ErrNil || len(val) == 0 {
					msg = "There are no ignored users."
				} else {
					log.WithError(err).Error("redis")
				}
				err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: msg,
						// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
						Flags: 64,
					},
				})
				if err != nil {
					log.WithError(err).Error("discord")
				}
				return
			}
			// We have a bunch of ids...
			opts := options.Update().SetUpsert(true)

			filter := bson.M{
				"id": bson.M{
					"$in": val,
				},
			}

			users := []*mongo.User{}

			cur, err := mongo.Database.Collection("users").Find(context.Background(), filter)
			if err == nil {
				err = cur.All(context.Background(), &users)
			}
			if err != nil {
				log.WithError(err).Error("mongo")
				err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: "Internal server error. Please try again later.",
						// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
						Flags: 64,
					},
				})
				if err != nil {
					log.WithError(err).Error("discord")
				}
				return
			}

			if len(users) != len(val) {
				// find missing ones
				missingIDs := []string{}
				for _, id := range val {
					found := false
					for _, u := range users {
						if u.ID == id {
							found = true
							break
						}
					}
					if !found {
						missingIDs = append(missingIDs, id)
					}
				}
				apiUsers, err := api.GetUsers(context.Background(), "", missingIDs, nil)
				if err != nil {
					log.WithError(err).Error("api")
					err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionApplicationCommandResponseData{
							Content: "Internal server error. Please try again later.",
							// Makes the response ephemeral https://discord.com/developers/docs/interactions/slash-commands#interaction-response
							Flags: 64,
						},
					})
					if err != nil {
						log.WithError(err).Error("discord")
					}
				}
				wg := sync.WaitGroup{}
				wg.Add(len(apiUsers))

				for _, u := range apiUsers {
					go func(u api.TwitchUser) {
						defer wg.Done()
						user := &mongo.User{
							ID:    u.ID,
							Name:  u.DisplayName,
							Login: u.Login,
						}
						users = append(users, user)
						if _, err := mongo.Database.Collection("users").UpdateOne(context.Background(), bson.M{
							"$or": bson.A{
								bson.M{"id": u.ID},
								bson.M{"login": u.Login},
							},
						}, bson.M{"$set": user}, opts); err != nil {
							log.WithError(err).Error("mongo")
						}
					}(u)
				}
				wg.Done()
			}

			usrStr := make([]string, len(users))
			for i, user := range users {
				usrStr[i] = user.Name
			}

			err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionApplicationCommandResponseData{
					Content: fmt.Sprintf("Ignored Users: %s", strings.Join(usrStr, ", ")),
				},
			})
			if err != nil {
				log.WithError(err).Error("discord")
			}
		}),
		"link": func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionApplicationCommandResponseData{
					Content: fmt.Sprintf("This bot can be invited to a server by going to <%s/login>.\nThis is an opensource bot and it's free, <https://github.com/troydota/modlogs>", configure.Config.GetString("website_url")),
				},
			})
			if err != nil {
				log.WithError(err).Error("discord")
			}
		},
	}
)

func New() *Bot {
	// Create a new Discord session using the provided bot token.
	dg, err := discordgo.New("Bot " + configure.Config.GetString("discord_bot_token"))
	if err != nil {
		panic(err)
	}

	// Register the messageCreate func as a callback for MessageCreate events.
	dg.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		log.Infof("Bot is up - Connected to %v guilds", len(s.State.Guilds))
	})

	dg.AddHandler(func(s *discordgo.Session, r *discordgo.GuildCreate) {
		log.Debugf("Bot joined - %s (%s)", r.Name, r.ID)
	})

	dg.AddHandler(func(s *discordgo.Session, r *discordgo.GuildDelete) {
		log.Debugf("Bot left - %s (%s)", r.Name, r.ID)
	})

	dg.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		// log.Warnln(i.Data.Name)
		if h, ok := commandHandlers[i.Data.Name]; ok {
			h(s, i)
		}
	})

	// Open a websocket connection to Discord and begin listening.
	err = dg.Open()
	if err != nil {
		panic(err)
	}

	bot := &Bot{
		conn:     dg,
		commands: []*cmdWrapper{},
		stopped:  make(chan struct{}),
		limiter:  newLimiter(),
	}

	if configure.Config.GetBool("rebuild_commands") {
		go func() {
			for _, v := range commands {
				c, err := dg.ApplicationCommandCreate(dg.State.User.ID, "", v)
				if err != nil {
					panic(fmt.Sprintf("cannot create '%v' command: %v", v.Name, err))
				}
				bot.commands = append(bot.commands, &cmdWrapper{
					cmd:     c,
					conn:    dg,
					guildID: "",
				})
			}
		}()
	}

	go func() {
		for {
			select {
			case <-bot.stopped:
				return
			case msg := <-Callback:
				go bot.processCallback(msg)
			}
		}
	}()

	return bot
}

func (b *Bot) processCallback(cb WebhookRequest) {
	hooks := []*mongo.Hook{}

	cur, err := mongo.Database.Collection("hooks").Find(context.Background(), bson.M{"streamer_id": cb.BroadcasterID})
	if err == nil {
		err = cur.All(context.Background(), &hooks)
	}

	if err != nil {
		log.WithError(err).Error("mongo")
		return
	}

	wg := &sync.WaitGroup{}
	wg.Add(len(hooks))

	var color int
	var title string
	var cmd string
	fields := []*discordgo.MessageEmbedField{
		{Name: "Broadcaster", Value: cb.BroadcasterUserName},
	}

	if cb.Action == "channel.ban" {
		var reason string
		if cb.Reason == "" {
			reason = "None Provided"
		} else {
			reason = cb.Reason
		}
		fields = append(fields,
			&discordgo.MessageEmbedField{Name: "User", Value: cb.UserName},
			&discordgo.MessageEmbedField{Name: "Moderator", Value: cb.ModeratorUserName},
			&discordgo.MessageEmbedField{Name: "Reason", Value: reason},
		)
		if cb.Expires == nil {
			title = "User Ban Event"
			cmd = fmt.Sprintf("ban %s", cb.UserName)
		} else {
			title = "User Timeout Event"
			fields = append(fields, &discordgo.MessageEmbedField{Name: "Expires", Value: cb.Expires.Format("Mon Jan _2 15:04:05 2006")})
			cmd = fmt.Sprintf("timeout %s %v", cb.UserName, int64(math.Round(float64(cb.Expires.Sub(cb.CreatedAt)/time.Second))+1))
		}
		if cb.Reason != "" {
			cmd = fmt.Sprintf("%s %s", cmd, reason)
		}
		color = 13632027
	} else if cb.Action == "channel.unban" {
		title = "User Unban Event"
		color = 8311585
		fields = append(fields,
			&discordgo.MessageEmbedField{Name: "User", Value: cb.UserName},
			&discordgo.MessageEmbedField{Name: "Moderator", Value: cb.ModeratorUserName},
		)
		cmd = fmt.Sprintf("unban %s", cb.UserName)
	} else if cb.Action == "channel.moderator.add" {
		title = "User Mod Event"
		color = 9442302
		fields = append(fields,
			&discordgo.MessageEmbedField{Name: "User", Value: cb.UserName},
		)
		cmd = fmt.Sprintf("mod %s", cb.UserName)
	} else if cb.Action == "channel.moderator.remove" {
		title = "User Unmod Event"
		color = 16312092
		fields = append(fields,
			&discordgo.MessageEmbedField{Name: "User", Value: cb.UserName},
		)
		cmd = fmt.Sprintf("unmod %s", cb.UserName)
	}

	embed := &discordgo.MessageEmbed{
		Title:       title,
		Description: "_ _",
		Color:       color,
		Timestamp:   cb.CreatedAt.Format(time.RFC3339),
		Footer: &discordgo.MessageEmbedFooter{
			Text: "KomodoHype",
		},
		Fields: fields,
	}

	var executer string
	var executerID string
	if cb.ModeratorUserName != "" {
		executer = cb.ModeratorUserName
		executerID = cb.ModeratorID
	} else {
		executer = cb.BroadcasterUserName
		executerID = cb.BroadcasterID
	}

	minimalText := fmt.Sprintf("**%s: #%s** - `%s` executed `/%s`", title, cb.BroadcasterUserName, strings.ReplaceAll(executer, "`", ""), strings.ReplaceAll(cmd, "`", ""))

	for _, hook := range hooks {
		go func(hook *mongo.Hook) {
			defer wg.Done()

			found := false
			for _, g := range b.conn.State.Guilds {
				if g.ID == hook.GuildID {
					found = true
				}
			}
			if !found {
				_, err := mongo.Database.Collection("hooks").DeleteOne(context.Background(), bson.M{
					"guild_id":    hook.GuildID,
					"channel_id":  hook.ChannelID,
					"streamer_id": hook.StreamerID,
				})
				if err != nil {
					log.WithError(err).WithField("hook", hook).Error("mongo")
					return
				}
				val, err := redis.Client.Decr(context.Background(), fmt.Sprintf("streamers:%s", hook.StreamerID)).Result()
				if err != nil {
					log.WithError(err).WithField("hook", hook).Error("redis")
					return
				}
				if val == 0 {
					if err := api.RevokeWebhook(context.Background(), cb.BroadcasterID); err != nil {
						log.WithError(err).WithField("hook", hook).Error("api")
					}
					return
				}
			}

			if redis.Client.SIsMember(context.Background(), fmt.Sprintf("ignored-users:%s", hook.GuildID), executerID).Val() {
				return
			}

			var err error
			if hook.Mode == mongo.ModeEmbed {
				if result := b.limiter.Limit(hook.ChannelID, "", func(c string) bool {
					return false
				}); result {
					_, err = b.conn.ChannelMessageSendEmbed(hook.ChannelID, embed)
				}
			} else {
				mtx := &sync.Mutex{}
				if result := b.limiter.Limit(hook.ChannelID, minimalText, func(c string) bool {
					mtx.Lock()
					defer mtx.Unlock()
					newMessage := fmt.Sprintf("%s\n%s", minimalText, c)
					if len(newMessage) < 2000 {
						minimalText = newMessage
						return true
					}
					return false
				}); result {
					_, err = b.conn.ChannelMessageSend(hook.ChannelID, minimalText)
				}
			}
			if err != nil {
				log.WithError(err).WithField("hook", hook).Error("discord")
			}
		}(hook)
	}

	wg.Wait()
}

func (b *Bot) Shutdown() error {
	close(b.stopped)
	return b.conn.Close()
}
