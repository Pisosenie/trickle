package httpx

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"

	"trickle/internal/secret"
)

type Response struct {
	Success bool   `json:"success"`
	Data    any    `json:"data,omitempty"`
	Error   string `json:"error,omitempty"`
}

func WriteData(w http.ResponseWriter, status int, data any) {
	write(w, status, Response{Success: true, Data: data})
}

func WriteError(w http.ResponseWriter, log *slog.Logger, status int, msg string, err error) {
	if err != nil && status >= http.StatusInternalServerError {
		log.Error("request failed", "status", status, "err", err)
	}
	write(w, status, Response{Success: false, Error: msg})
}

func write(w http.ResponseWriter, status int, body Response) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func DecodeJSON(w http.ResponseWriter, r *http.Request, maxBytes int64, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("trailing data after JSON body")
	}
	return nil
}

func RequireBearer(log *slog.Logger, msg string, token func() string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !secret.Equal(secret.FromBearer(r.Header.Get("Authorization")), token()) {
			WriteError(w, log, http.StatusUnauthorized, msg, nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func IsLoopbackHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
