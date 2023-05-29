module github.com/troydota/modlogs

go 1.16

require (
	github.com/bwmarrin/discordgo v0.23.2
	github.com/go-redis/redis/v8 v8.10.0
	github.com/gofiber/fiber/v2 v2.46.0
	github.com/golang/snappy v0.0.3 // indirect
	github.com/google/uuid v1.3.0
	github.com/hashicorp/go-multierror v1.1.1
	github.com/json-iterator/go v1.1.11
	github.com/kr/pretty v0.2.1
	github.com/pasztorpisti/qs v0.0.0-20171216220353-8d6c33ee906c
	github.com/sirupsen/logrus v1.8.1
	github.com/spf13/pflag v1.0.5
	github.com/spf13/viper v1.7.1
	go.mongodb.org/mongo-driver v1.5.3
	go.uber.org/ratelimit v0.2.0
)

replace github.com/bwmarrin/discordgo => github.com/bwmarrin/discordgo v0.23.3-0.20210312144535-ba10a00fbcfa
