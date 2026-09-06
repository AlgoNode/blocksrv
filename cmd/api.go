package cmd

import (
	"context"
	"errors"
	"net/http"

	cli "github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/algonode/blocksrv/conf"
	"github.com/algonode/blocksrv/embed"
	"github.com/algonode/blocksrv/gorestapi"
	"github.com/algonode/blocksrv/gorestapi/mainrpc"
	"github.com/algonode/blocksrv/importer"
	"github.com/algonode/blocksrv/server"
	"github.com/algonode/blocksrv/store/pebble"
	"github.com/algonode/blocksrv/store/seaweed"
)

func init() {
	rootCmd.AddCommand(apiCmd)
}

func openStore() gorestapi.Ledger {
	backend := conf.C.String("store.backend")
	var (
		db  gorestapi.Ledger
		err error
	)
	switch backend {
	case "pebble":
		db, err = pebble.New(conf.C)
	case "seaweed":
		db, err = seaweed.New(conf.C)
	default:
		logger.Fatalw("unknown store.backend, want pebble or seaweed", "backend", backend)
	}
	if err != nil {
		logger.Fatalw("Store init error", "backend", backend, "error", err)
	}
	logger.Infow("Store ready", "backend", backend)
	return db
}

var (
	apiCmd = &cli.Command{
		Use:   "api",
		Short: "Start API",
		Long:  `Start API`,
		Run: func(cmd *cli.Command, args []string) {

			// Database
			db := openStore()

			// Create the server
			s, err := server.New(conf.C)
			if err != nil {
				logger.Fatalw("Could not create server", "error", err)
			}

			s.Router().Get("/version", conf.GetVersion())

			if err = mainrpc.Setup(s.Router(), db); err != nil {
				logger.Fatalw("Could not setup rpc", "error", err)
			}

			docsFileServer := http.FileServer(http.FS(embed.PublicHTMLFS()))

			s.Router().Mount("/v2/api-docs", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Vary", "Accept-Encoding")
				w.Header().Set("Cache-Control", "no-cache")
				docsFileServer.ServeHTTP(w, r)
			}))

			s.Router().Mount("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Vary", "Accept-Encoding")
				w.Header().Set("Cache-Control", "no-cache")
				docsFileServer.ServeHTTP(w, r)
			}))

			if err = s.ListenAndServe(conf.C); err != nil {
				logger.Fatalw("Could not start server", "error", err)
			}

			// Optional background import from another blocksrv
			var importDone chan struct{}
			if icfg := importer.ConfigFromKoanf(conf.C); icfg.Enabled() {
				imp, err := importer.New(icfg, db)
				if err != nil {
					logger.Fatalw("Import config error", "error", err)
				}
				importDone = make(chan struct{})
				go func() {
					defer close(importDone)
					if err := imp.Run(conf.Stop.Context()); err != nil && !errors.Is(err, context.Canceled) {
						logger.Errorw("Import failed", "error", err)
					}
				}()
			}

			conf.Stop.InitInterrupt()
			<-conf.Stop.Chan() // Wait until Stop
			if importDone != nil {
				<-importDone // Let the importer persist its progress
			}
			conf.Stop.Wait() // Wait until everyone cleans up
			if err := db.Close(); err != nil {
				logger.Errorw("Store close error", "error", err)
			}
			_ = zap.L().Sync() // Flush the logger

		},
	}
)
