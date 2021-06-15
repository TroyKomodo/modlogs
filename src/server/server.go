package server

import (
	"net"

	log "github.com/sirupsen/logrus"

	"github.com/gofiber/fiber/v2"
	"github.com/troydota/modlogs/src/configure"
	"github.com/troydota/modlogs/src/utils"
)

type Server struct {
	app      *fiber.App
	listener net.Listener
}

func Logger() func(c *fiber.Ctx) error {
	return func(c *fiber.Ctx) error {
		var (
			err interface{}
		)
		func() {
			defer func() {
				err = recover()
			}()
			err = c.Next()
		}()
		if err != nil {
			_ = c.SendStatus(500)
		}
		l := log.WithFields(log.Fields{
			"status": c.Response().StatusCode(),
			"path":   utils.B2S(c.Request().RequestURI()),
		})
		if err != nil {
			l = l.WithFields(log.Fields{
				"error": err,
			})
		}
		l.Info()
		return nil
	}
}

func New() *Server {
	l, err := net.Listen(configure.Config.GetString("conn_type"), configure.Config.GetString("conn_uri"))
	if err != nil {
		log.Fatalf("failed to start listner for http server, err=%e", err)
		return nil
	}

	server := &Server{
		app:      fiber.New(fiber.Config{DisableStartupMessage: true}),
		listener: l,
	}

	server.app.Use(Logger())

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
			log.WithError(err).Fatal("failed to start http server")
		}
	}()

	return server
}

func (s *Server) Shutdown() error {
	return s.listener.Close()
}
