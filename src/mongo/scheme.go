package mongo

type Hook struct {
	GuildID    string `json:"guild_id" bson:"guild_id"`
	ChannelID  string `json:"channel_id" bson:"channel_id"`
	StreamerID string `json:"streamer_id" bson:"streamer_id"`
	Mode       int32  `json:"mode" bson:"mode"`
}

const (
	ModeMinimal int32 = iota
	ModeEmbed
)

type User struct {
	ID    string `json:"id" bson:"id"`
	Name  string `json:"name" bson:"name"`
	Login string `json:"login" bson:"login"`
}
