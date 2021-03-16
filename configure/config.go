package configure

import (
	"bytes"
	"encoding/json"
	"strings"

	nested "github.com/antonfisher/nested-logrus-formatter"
	"github.com/kr/pretty"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

type ServerCfg struct {
	Level              string   `mapstructure:"level"`
	ConfigFile         string   `mapstructure:"config_file"`
	RedisURI           string   `mapstructure:"redis_uri"`
	APIConnType        string   `mapstructure:"conn_type"`
	APIConnURI         string   `mapstructure:"conn_uri"`
	TwitchRedirectURI  string   `mapstructure:"twitch_redirect_uri"`
	TwitchClientID     string   `mapstructure:"twitch_client_id"`
	TwitchClientSecret string   `mapstructure:"twitch_client_secret"`
	WebsiteURL         string   `mapstructure:"website_url"`
	TwitchBotID        string   `mapstructure:"twitch_bot_id"`
	CookieDomain       string   `mapstructure:"cookie_domain"`
	DiscordInvite      string   `mapstructure:"discord_invite"`
	DiscordBotToken    string   `mapstructure:"discord_bot_token"`
	Admins             []string `mapstructure:"admins"`
	MaxHooksPerGuild   int      `mapstructure:"max_hooks_per_guild"`
	RebuildCommands    bool     `mapstructure:"rebuild_commands"`
	ExitCode           int      `mapstructure:"exit_code"`
}

// default config
var defaultConf = ServerCfg{
	ConfigFile: "config.yaml",
}

var Config = viper.New()

func initLog() {
	if l, err := log.ParseLevel(Config.GetString("level")); err == nil {
		log.SetLevel(l)
		log.SetReportCaller(true)
	}
	log.SetFormatter(&nested.Formatter{
		HideKeys:    true,
		FieldsOrder: []string{"component", "category"},
	})
}

func init() {
	// Default config
	b, _ := json.Marshal(defaultConf)
	defaultConfig := bytes.NewReader(b)
	viper.SetConfigType("json")
	viper.ReadConfig(defaultConfig)
	Config.MergeConfigMap(viper.AllSettings())

	// Flags
	pflag.String("config_file", "config.yaml", "configure filename")
	pflag.String("level", "info", "Log level")
	pflag.String("redis_uri", "", "Address for the redis server.")
	pflag.String("twitch_redirect_uri", "", "Twitch redirect uri")
	pflag.String("conn_type", "", "Connection type, udp/tcp/unix")
	pflag.String("conn_uri", "", "Connection url:port or path")
	pflag.String("twitch_client_id", "", "Twitch client id")
	pflag.String("twitch_client_secret", "", "Twitch client secret")
	pflag.String("website_url", "", "Url for the website")
	pflag.String("twitch_bot_id", "", "The id of the bot account for streamer vods.")
	pflag.String("cookie_domain", "", "Domain for the cookies to be set.")
	pflag.String("discord_invite", "", "The invite url for the discord bot.")
	pflag.String("discord_bot_token", "", "The discord bot token.")
	pflag.Int("max_hooks_per_guild", 10, "Max number of hooks per guild.")
	pflag.Bool("rebuild_commands", false, "Recreate or create the discord commands initially.")
	pflag.String("version", "1.0", "Version of the system.")
	pflag.StringSlice("admins", []string{}, "Ids of global bot admins.")
	pflag.Int("exit_code", 0, "Status code for successful and graceful shutdown, [0-125].")
	pflag.Parse()
	Config.BindPFlags(pflag.CommandLine)

	// File
	Config.SetConfigFile(Config.GetString("config_file"))
	Config.AddConfigPath(".")
	err := Config.ReadInConfig()
	if err != nil {
		log.Warning(err)
		log.Info("Using default config")
	} else {
		Config.MergeInConfig()
	}

	// Environment
	replacer := strings.NewReplacer(".", "_")
	Config.SetEnvKeyReplacer(replacer)
	Config.AllowEmptyEnv(true)
	Config.AutomaticEnv()

	// Log
	initLog()

	// Print final config
	c := ServerCfg{}
	Config.Unmarshal(&c)
	log.Debugf("Current configurations: \n%# v", pretty.Formatter(c))
}
