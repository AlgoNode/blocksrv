package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/rs/xid"
)

// RenderJSON writes an http response using the value passed in v as JSON.
// If it cannot convert the value to JSON, it returns an error
func RenderJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	b := new(bytes.Buffer)
	if err := json.NewEncoder(b).Encode(v); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(b, `{"message":"render error: %s"}`, errString(err))
	} else {
		w.WriteHeader(code)
	}
	_, _ = w.Write(b.Bytes())
}

func RenderEmptyOK(w http.ResponseWriter) {
	w.WriteHeader(http.StatusOK)
}

func RenderNoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// ErrResponse has the same shape as algod's ErrorResponse: a human readable
// message plus optional structured data.
type ErrResponse struct {
	Message string                 `json:"message"`
	Data    map[string]interface{} `json:"data,omitempty"`
}

// RenderErr writes an algod-style error body with the given status code.
func RenderErr(w http.ResponseWriter, code int, msg string) {
	RenderJSON(w, code, ErrResponse{Message: msg})
}

func RenderErrNotFound(w http.ResponseWriter, msg string) {
	RenderErr(w, http.StatusNotFound, msg)
}

func RenderErrResourceNotFound(w http.ResponseWriter, resource string) {
	RenderErr(w, http.StatusNotFound, resource+" not found")
}

func RenderErrUnauthorized(w http.ResponseWriter) {
	RenderErr(w, http.StatusUnauthorized, "not authorized")
}

func RenderErrInvalidRequest(w http.ResponseWriter, err error) {
	RenderErr(w, http.StatusBadRequest, errString(err))
}

func RenderErrConflict(w http.ResponseWriter, err error) {
	RenderErr(w, http.StatusConflict, errString(err))
}

// RenderErrInternal answers 500 with msg; the underlying error, if any, is
// exposed under data.error so operators can see what failed.
func RenderErrInternal(w http.ResponseWriter, msg string, err error) {
	resp := ErrResponse{Message: msg}
	if err != nil {
		resp.Data = map[string]interface{}{"error": err.Error()}
	}
	RenderJSON(w, http.StatusInternalServerError, resp)
}

func RenderErrInternalWithID(w http.ResponseWriter, msg string, err error) string {
	errID := xid.New().String()
	resp := ErrResponse{Message: msg, Data: map[string]interface{}{"error_id": errID}}
	if err != nil {
		resp.Data["error"] = err.Error()
	}
	RenderJSON(w, http.StatusInternalServerError, resp)
	return errID
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func DecodeJSON(r io.Reader, v interface{}) error {
	defer io.Copy(io.Discard, r)
	return json.NewDecoder(r).Decode(v)
}

func RenderBlob(w http.ResponseWriter, contentType string, blob []byte, cc string) error {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(blob)))
	if len(cc) > 0 {
		w.Header().Set("Cache-Control", cc)
	}
	w.WriteHeader(200)
	_, err := w.Write(blob)
	return err
}
