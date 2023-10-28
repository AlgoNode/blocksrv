package cmd

import (
	"net/http"

	cli "github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/algonode/blocksrv/conf"
	"github.com/algonode/blocksrv/embed"
	"github.com/algonode/blocksrv/gorestapi/mainrpc"
	"github.com/algonode/blocksrv/server"
	"github.com/algonode/blocksrv/store/pebble"
)

func init() {
	rootCmd.AddCommand(apiCmd)
}

var (
	apiCmd = &cli.Command{
		Use:   "api",
		Short: "Start API",
		Long:  `Start API`,
		Run: func(cmd *cli.Command, args []string) { // Initialize the database

			// Database
			db, err := pebble.New(conf.C)
			if err != nil {
				logger.Fatalw("Clickhouse error", "error", err)
			}

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

			conf.Stop.InitInterrupt()
			<-conf.Stop.Chan() // Wait until Stop
			conf.Stop.Wait()   // Wait until everyone cleans up
			_ = zap.L().Sync() // Flush the logger

		},
	}
)
