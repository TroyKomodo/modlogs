package server

import (
	"net"

	log "github.com/sirupsen/logrus"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/troydota/modlogs/configure"
)

type Server struct {
	app      *fiber.App
	listener net.Listener
}

type CustomLogger struct{}

func (*CustomLogger) Write(data []byte) (n int, err error) {
	log.Debugln(string(data))
	return len(data), nil
}

func New() *Server {
	l, err := net.Listen(configure.Config.GetString("conn_type"), configure.Config.GetString("conn_uri"))
	if err != nil {
		log.Fatalf("failed to start listner for http server, err=%e", err)
		return nil
	}

	server := &Server{
		app:      fiber.New(),
		listener: l,
	}

	server.app.Use(logger.New(logger.Config{
		Output: &CustomLogger{},
	}))

	server.app.Use(recover.New())

	server.app.Get("/", func(c *fiber.Ctx) error {
		return c.Redirect(configure.Config.GetString("discord_invite"))
	})

	Twitch(server.app)

	server.app.Use(func(c *fiber.Ctx) error {
		return c.Status(404).JSON(&fiber.Map{
			"status":  404,
			"message": "We don't know what you're looking for.",
		})
	})

	go func() {
		err = server.app.Listener(server.listener)
		if err != nil {
			log.Errorf("failed to start http server, err=%e", err)
		}
	}()

	return server
}

func (s *Server) Shutdown() error {
	return s.listener.Close()
}
