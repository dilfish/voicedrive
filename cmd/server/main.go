package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dilfish/voicedrive/internal/server"
	"github.com/dilfish/voicedrive/internal/store"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	dsn := getenv("DATABASE_URL", "postgres://voicedrive:voicedrive@localhost:5432/voicedrive?sslmode=disable")
	db, err := store.Open(ctx, dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{Addr: getenv("HTTP_ADDR", ":8080"), Handler: server.NewHandler(db), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("voicedrive server listening on %s", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("http server: %v", err)
			stop()
		}
	}()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		log.Printf("shutdown server: %v", err)
	}
}
func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
