package main

import (
	"net/http"
	"os"
	"time"

	"github.com/domino14/liwords-socket/pkg/config"
	sockets "github.com/domino14/liwords-socket/pkg/hub"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	GracefulShutdownTimeout = 30 * time.Second
)

func main() {

	cfg := &config.Config{}
	cfg.Load(os.Args[1:])
	log.Info().Msgf("Loaded config: %v", cfg)

	if cfg.Debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	} else {
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}

	log.Debug().Msg("debug log is on")

	h, err := sockets.NewHub(cfg)
	if err != nil {
		panic(err)
	}
	go h.Run()
	// go hub.RunGameEventHandler()

	// http.HandleFunc("/")

	// http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
	// 	r.URL.Path = "/"
	// 	staticHandler.ServeHTTP(w, r)
	// })

	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		sockets.ServeWS(h, w, r)
	})
	err = http.ListenAndServe(cfg.WebsocketAddress, nil)
	if err != nil {
		log.Fatal().Err(err).Msg("ListenAndServe")
	}

	// srv := &http.Server{Addr: ":8088", Handler: handler}

	// idleConnsClosed := make(chan struct{})
	// sig := make(chan os.Signal, 1)

	// go func() {
	// 	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	// 	<-sig
	// 	log.Info().Msg("got quit signal...")
	// 	ctx, cancel := context.WithTimeout(context.Background(), GracefulShutdownTimeout)
	// 	if err := srv.Shutdown(ctx); err != nil {
	// 		// Error from closing listeners, or context timeout:
	// 		log.Error().Msgf("HTTP server Shutdown: %v", err)
	// 	}
	// 	cancel()
	// 	close(idleConnsClosed)
	// }()

	// if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
	// 	log.Fatal().Err(err).Msg("")
	// }
	// <-idleConnsClosed
	// log.Info().Msg("server gracefully shutting down")
}
