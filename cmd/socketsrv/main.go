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

func pingEndpoint(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Content-Type", "application/json")
	w.WriteHeader(200)
	w.Write([]byte(`{"status":"copacetic"}`))
}

var (
	// BuildHash is the git hash, set by go build flags
	BuildHash = "unknown"
	// BuildDate is the build date, set by go build flags
	BuildDate = "unknown"
)

func main() {

	cfg := &config.Config{}
	cfg.Load(os.Args[1:])
	log.Info().Interface("config", cfg).
		Str("build-date", BuildDate).Str("build-hash", BuildHash).Msg("started")

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
	http.HandleFunc("/ping", http.HandlerFunc(pingEndpoint))

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
