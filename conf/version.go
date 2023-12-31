package conf

import (
	"encoding/json"
	"net/http"
)

var (
	// Executable is overridden by Makefile with executable name
	Executable = "NoExecutable"
	// GitVersion is overridden by Makefile with git information
	GitVersion = "dev"
)

// GetVersion returns version as a simple json
func GetVersion() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v := struct {
			Version string `json:"version"`
		}{
			Version: "blocksrv-" + GitVersion,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(v)
	}
}
