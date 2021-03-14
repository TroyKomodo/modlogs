package bot

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/davecgh/go-spew/spew"
	jsoniter "github.com/json-iterator/go"
	log "github.com/sirupsen/logrus"
	"github.com/troydota/modlogs/api"
	"github.com/troydota/modlogs/configure"
	"github.com/troydota/modlogs/redis"
)

var json = jsoniter.ConfigCompatibleWithStandardLibrary

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

type c struct {
	id   string
	mode int64
}

type cc struct {
	cs  []c
	cmd *redis.StringCmd
}

type Bot struct {
	conn *discordgo.Session

	commands []*cmdWrapper
	stopped  chan struct{}
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
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionApplicationCommandResponseData{
					Content: "Internal Server Error. Please try again later...",
				},
			})
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
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionApplicationCommandResponseData{
					Content: "You do not have permission to execute that command.",
				},
			})
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
					Description: "The ID of the twitch user.",
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
					Description: "The ID of the twitch user.",
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
			var minimal bool
			if len(i.Data.Options) == 2 {
				minimal = i.Data.Options[1].BoolValue()
			}
			if len(i.Data.Options) == 3 {
				channel = i.Data.Options[2].ChannelValue(s)
				if channel != nil && channel.Type != discordgo.ChannelTypeGuildText {
					s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionApplicationCommandResponseData{
							Content: "Logs can only be outputted into a text channel.",
						},
					})
					return
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
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: "Internal server error occured.",
					},
				})
			}

			userStr, err := redis.AuthTokenValues(token)
			if err != nil {
				msg := "Internal server error. Please try again later."
				if err == redis.ErrNil {
					msg = "The token you provided is expired or invalid. Please login again to make a new one."
				} else {
					log.Errorf("redis, err=%e", err)
				}
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: msg,
					},
				})
				return
			}

			user := &api.TwitchUser{}

			if err := json.UnmarshalFromString(userStr, user); err != nil {
				log.Errorf("json, err=%e", err)
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: "Internal server error. Please try again later.",
					},
				})
				return
			}

			val, err := redis.CreateCallback(user.ID, g.ID, channel.ID, minimal)
			if err != nil {
				log.Errorf("redis, err=%e", err)
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: "Internal server error. Please try again later.",
					},
				})
				return
			}

			if val == -1 {
				if err != nil {
					s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionApplicationCommandResponseData{
							Content: "There are too many hooks in this discord.",
						},
					})
					return
				}
			}

			if val == 0 {
				err := api.CreateWebhooks(user.ID)
				if err != nil {
					redis.DeleteCallback(user.ID, g.ID, channel.ID)
					log.Errorf("api, err=%e", err)
					s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionApplicationCommandResponseData{
							Content: "Internal server error. Please try again later.",
						},
					})
					return
				}
			}

			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionApplicationCommandResponseData{
					Content: fmt.Sprintf("ModLogs added for <https://twitch.tv/%s>, into %s", user.Login, channel.Mention()),
				},
			})
		}),
		"list": validationWrapper(func(s *discordgo.Session, i *discordgo.InteractionCreate, g *discordgo.Guild) {
			var channel *discordgo.Channel
			if len(i.Data.Options) == 1 {
				channel = i.Data.Options[0].ChannelValue(s)
			}

			msg := fmt.Sprintf("List todo for guild - %s", g.ID)
			channelID := ""
			if channel != nil {
				msg = fmt.Sprintf("%s - channel - %s", msg, channel.ID)
				channelID = channel.ID
			}

			val, err := redis.GetGuildCallbacks(g.ID, channelID)
			if err != nil {
				log.Error("redis, err=%e", err)
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: "Internal Server Error.",
					},
				})
			}

			chennelIDs := map[string]cc{}

			pipe := redis.Client.Pipeline()

			for _, s := range val {
				streamer, ok := s.([]interface{})
				if !ok {
					log.Warnf("invalid resp from redis, %s", spew.Sdump(s))
					continue
				}
				streamerID, ok := streamer[0].(string)
				if !ok {
					log.Warnf("invalid resp from redis, %s", spew.Sdump(streamer))
					continue
				}
				channels, ok := streamer[1].([]interface{})
				if !ok {
					log.Warnf("invalid resp from redis, %s", spew.Sdump(streamer))
					continue
				}

				cs := make([]c, len(channels))

				for i, ch := range channels {
					channel, ok := ch.([]interface{})
					if !ok {
						log.Warnf("invalid resp from redis, %s", spew.Sdump(ch))
						continue
					}

					channelID, ok := channel[0].(string)
					if !ok {
						log.Warnf("invalid resp from redis, %s", spew.Sdump(ch))
						continue
					}
					mode, ok := channel[1].(int64)
					if !ok {
						log.Warnf("invalid resp from redis, %s", spew.Sdump(ch))
						continue
					}
					cs[i] = c{channelID, mode}
				}

				chennelIDs[streamerID] = cc{cmd: pipe.Get(redis.Ctx, fmt.Sprintf("users:%s", streamerID)), cs: cs}
			}

			if _, err := pipe.Exec(redis.Ctx); err != nil {
				log.Errorf("redis, err=%e", err)
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: "Internal Server Error.",
					},
				})
				return
			}

			lines := []string{}

			for _, v := range chennelIDs {
				user := api.TwitchUser{}
				if err := json.UnmarshalFromString(v.cmd.Val(), &user); err != nil {
					log.Errorf("json, err=%e", err)
					continue
				}
				channels := []string{}
				for _, c := range v.cs {
					mode := "embed"
					if c.mode != 1 {
						mode = "minimal"
					}
					channels = append(channels, fmt.Sprintf("<#%s> - %s", c.id, mode))
				}
				lines = append(lines, fmt.Sprintf(`<https://twitch.tv/%s> -> %s`, user.Login, strings.Join(channels, ", ")))
			}

			if len(lines) == 0 {
				lines = append(lines, "No hooks were found")
			}

			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionApplicationCommandResponseData{
					Content: strings.Join(lines, "\n"),
				},
			})
		}),
		"delete": validationWrapper(func(s *discordgo.Session, i *discordgo.InteractionCreate, g *discordgo.Guild) {
			streamerID := i.Data.Options[0].StringValue()
			var channel *discordgo.Channel

			if len(i.Data.Options) == 2 {
				channel = i.Data.Options[1].ChannelValue(s)
			}

			var channelID string
			if channel != nil {
				channelID = channel.ID
			}

			v, err := redis.Client.Get(redis.Ctx, fmt.Sprintf("users:%s", streamerID)).Result()
			if err != nil {
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: "That hook doesn't exist",
					},
				})
				return
			}

			if len(v) < 16 {
				streamerID = v
			}

			val, err := redis.DeleteCallback(streamerID, g.ID, channelID)
			if err != nil {
				log.Errorf("redis, err=%e", err)
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: "Internal server error. Please try again later.",
					},
				})
				return
			}

			if val == -1 {
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: "That hook doesn't exist",
					},
				})
				return
			}

			if val == 0 {
				if err := api.RevokeWebhook(streamerID); err != nil {
					log.Errorf("api, err=%e", err)
				}
			}

			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionApplicationCommandResponseData{
					Content: "The hook has been removed.",
				},
			})
		}),
		"ignore": validationWrapper(func(s *discordgo.Session, i *discordgo.InteractionCreate, g *discordgo.Guild) {
			userID := i.Data.Options[0].StringValue()
			if len(userID) > 16 {
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: fmt.Sprintf("Please enter a valid twitch id."),
					},
				})
				return
			}
			if _, err := strconv.ParseInt(userID, 10, 64); err != nil {
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: fmt.Sprintf("Please enter a valid twitch id."),
					},
				})
				return
			}
			if err := redis.Client.HSet(redis.Ctx, fmt.Sprintf("ignored-users:%s", g.ID), userID, "1").Err(); err != nil {
				log.Errorf("redis, err=%e", err)
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: fmt.Sprintf("Internal server error."),
					},
				})
				return
			}
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionApplicationCommandResponseData{
					Content: fmt.Sprintf("Successfully ignored user."),
				},
			})
		}),
		"unignore": validationWrapper(func(s *discordgo.Session, i *discordgo.InteractionCreate, g *discordgo.Guild) {
			userID := i.Data.Options[0].StringValue()
			if len(userID) > 16 {
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: fmt.Sprintf("Please enter a valid twitch id."),
					},
				})
				return
			}
			if _, err := strconv.ParseInt(userID, 10, 64); err != nil {
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: fmt.Sprintf("Please enter a valid twitch id."),
					},
				})
				return
			}
			if err := redis.Client.HDel(redis.Ctx, fmt.Sprintf("ignored-users:%s", g.ID), userID).Err(); err != nil {
				log.Errorf("redis, err=%e", err)
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: fmt.Sprintf("Internal server error."),
					},
				})
				return
			}
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionApplicationCommandResponseData{
					Content: fmt.Sprintf("Successfully unignored user."),
				},
			})
		}),
		"ignored": validationWrapper(func(s *discordgo.Session, i *discordgo.InteractionCreate, g *discordgo.Guild) {
			val, err := redis.Client.HKeys(redis.Ctx, fmt.Sprintf("ignored-users:%s", g.ID)).Result()
			if err != nil {
				msg := "Internal server error."
				if err == redis.ErrNil {
					msg = "There are no ignored users."
				}
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionApplicationCommandResponseData{
						Content: msg,
					},
				})
				return
			}
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionApplicationCommandResponseData{
					Content: fmt.Sprintf("Ignored Users: %s", strings.Join(val, ", ")),
				},
			})
		}),
		"link": func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionApplicationCommandResponseData{
					Content: fmt.Sprintf("You can invite me to a server using <%s>\nOr you create a hook by <%s/login>", configure.Config.GetString("website_url"), configure.Config.GetString("website_url")),
				},
			})
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
		log.Infoln("Bot is up!")
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
	}

	for _, v := range commands {
		c, err := dg.ApplicationCommandCreate(dg.State.User.ID, "", v)
		if err != nil {
			panic(fmt.Errorf("Cannot create '%v' command: %v", v.Name, err))
		}
		bot.commands = append(bot.commands, &cmdWrapper{
			cmd:     c,
			conn:    dg,
			guildID: "",
		})
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
	callbacks, err := redis.GetCallbacks(cb.BroadcasterID)
	if err != nil {
		log.Errorf("redis, err=%s", err)
		return
	}

	wg := &sync.WaitGroup{}
	wg.Add(len(callbacks))

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
			cmd = fmt.Sprintf("timeout %s %v", cb.UserName, math.Round(float64(cb.Expires.Sub(cb.CreatedAt)/time.Second))+1)
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
		cmd = fmt.Sprintf("mod %s", cb.ModeratorUserName)
	} else if cb.Action == "channel.moderator.remove" {
		title = "User Unmod Event"
		color = 16312092
		fields = append(fields,
			&discordgo.MessageEmbedField{Name: "User", Value: cb.UserName},
		)
		cmd = fmt.Sprintf("unmod %s", cb.ModeratorUserName)
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

	for _, c := range callbacks {
		go func(c interface{}) {
			defer wg.Done()
			cbs, ok := c.([]interface{})
			if !ok {
				log.Warnf("invalid, resp from redis %v", spew.Sdump(c))
				return
			}
			guildID, ok := cbs[0].(string)
			if !ok {
				log.Warnf("invalid, resp from redis %v", spew.Sdump(c))
				return
			}
			channelIDs, ok := cbs[1].([]interface{})
			if !ok {
				log.Warnf("invalid, resp from redis %v", spew.Sdump(c))
				return
			}
			found := false
			for _, g := range b.conn.State.Guilds {
				if g.ID == guildID {
					found = true
				}
			}
			if !found {
				val, err := redis.DeleteCallback(cb.BroadcasterID, guildID, "")
				if err != nil {
					log.Errorf("redis, err=%e", err)
					return
				}
				if val == 0 {
					if err := api.RevokeWebhook(cb.BroadcasterID); err != nil {
						log.Errorf("api, err=%e", err)
					}
					return
				}
			}

			if redis.Client.HExists(redis.Ctx, fmt.Sprintf("ignored-users:%s", guildID), executerID).Val() {
				return
			}

			wg2 := &sync.WaitGroup{}
			wg2.Add(len(channelIDs))

			for _, cid := range channelIDs {
				if v, ok := cid.([]interface{}); ok {
					channelID, ok := v[0].(string)
					if !ok {
						wg2.Done()
						log.Warnf("invalid redis resp, resp=%s", spew.Sdump(v))
						continue
					}
					mode, ok := v[1].(int64)
					if !ok {
						wg2.Done()
						log.Warnf("invalid redis resp, resp=%s", spew.Sdump(v))
						continue
					}
					go func(cid string, mode int64) {
						defer wg2.Done()
						var err error
						if mode == 1 {
							_, err = b.conn.ChannelMessageSendEmbed(cid, embed)
						} else {
							_, err = b.conn.ChannelMessageSend(cid, minimalText)
						}
						if err != nil {
							log.Errorf("discord, err=%e", err)
							val, err := redis.DeleteCallback(cb.BroadcasterID, guildID, cid)
							if err != nil {
								log.Errorf("redis, err=%e", err)
								return
							}
							if val == 0 {
								if err := api.RevokeWebhook(cb.BroadcasterID); err != nil {
									log.Errorf("api, err=%e", err)
								}
								return
							}
						}
					}(channelID, mode)
				} else {
					wg2.Done()
					log.Warnf("invalid redis resp, resp=%s", spew.Sdump(cid))
				}
			}
			wg2.Wait()
		}(c)
	}

	wg.Wait()
}

func (b *Bot) Shutdown() error {
	// wg := &sync.WaitGroup{}
	// wg.Add(len(b.commands))
	close(b.stopped)
	// for _, c := range b.commands {
	// 	go func(c *cmdWrapper) {
	// 		defer wg.Done()
	// 		err := c.Delete()
	// 		if err != nil {
	// 			log.Errorf("cmd, err=%e", err)
	// 		}
	// 	}(c)
	// }
	// wg.Wait()
	return b.conn.Close()
}
