package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type response struct {
	Service      string `json:"service"`
	Release      string `json:"release"`
	Hostname     string `json:"hostname"`
	PostgresHits int64  `json:"postgres_hits"`
	ValkeyHits   int64  `json:"valkey_hits"`
}

func main() {
	handler := http.HandlerFunc(handle)
	server := &http.Server{Addr: ":8080", Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("app-web-test listening on %s", server.Addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func handle(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()

	postgresHits, err := recordPostgresHit(ctx)
	if err != nil {
		http.Error(writer, "postgres unavailable: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	valkeyHits, err := recordValkeyHit(ctx)
	if err != nil {
		http.Error(writer, "valkey unavailable: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	hostname, _ := os.Hostname()
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(response{
		Service:      "app-web-test",
		Release:      envOrDefault("RELEASE", "dev"),
		Hostname:     hostname,
		PostgresHits: postgresHits,
		ValkeyHits:   valkeyHits,
	})
}

func recordPostgresHit(ctx context.Context) (int64, error) {
	connection, err := pgx.Connect(ctx, envOrDefault("DATABASE_URL", "postgres://app:sandbox@postgres:5432/app?sslmode=disable"))
	if err != nil {
		return 0, fmt.Errorf("connect to postgres: %w", err)
	}
	defer connection.Close(ctx)
	if _, err := connection.Exec(ctx, `CREATE TABLE IF NOT EXISTS hits (id BIGSERIAL PRIMARY KEY, created_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return 0, fmt.Errorf("initialize postgres schema: %w", err)
	}
	if _, err := connection.Exec(ctx, `INSERT INTO hits DEFAULT VALUES`); err != nil {
		return 0, fmt.Errorf("insert postgres hit: %w", err)
	}
	var count int64
	if err := connection.QueryRow(ctx, `SELECT count(*) FROM hits`).Scan(&count); err != nil {
		return 0, fmt.Errorf("query postgres hits: %w", err)
	}
	return count, nil
}

func recordValkeyHit(ctx context.Context) (int64, error) {
	dialer := net.Dialer{Timeout: 3 * time.Second}
	connection, err := dialer.DialContext(ctx, "tcp", envOrDefault("VALKEY_ADDR", "valkey:6379"))
	if err != nil {
		return 0, fmt.Errorf("connect to valkey: %w", err)
	}
	defer connection.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
	if _, err := fmt.Fprint(connection, "*2\r\n$4\r\nINCR\r\n$4\r\nhits\r\n"); err != nil {
		return 0, fmt.Errorf("write valkey increment: %w", err)
	}
	line, err := bufio.NewReader(connection).ReadString('\n')
	if err != nil {
		return 0, fmt.Errorf("read valkey response: %w", err)
	}
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, ":") {
		return 0, fmt.Errorf("unexpected RESP response %q", line)
	}
	count, err := strconv.ParseInt(strings.TrimPrefix(line, ":"), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse valkey response %q: %w", line, err)
	}
	return count, nil
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
