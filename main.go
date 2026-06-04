package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/talosred/ce/dashboard"
	"github.com/talosred/ce/metrics"
	"github.com/talosred/ce/proxy"
	"github.com/talosred/ce/store"
)

func main() {
	port := flag.Int("port", 8080, "Port to listen on")
	dbPath := flag.String("db-path", "talosred.db", "Path to SQLite database file")
	otelEndpoint := flag.String("otel-endpoint", "", "OTLP HTTP endpoint for trace export (e.g. http://localhost:4318)")
	flag.Parse()

	db, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if err := store.Migrate(db); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	os.Setenv("TALOSRED_PORT", fmt.Sprintf("%d", *port))

	broadcaster := store.NewBroadcaster()
	requestStore := store.New(db, broadcaster)

	costCalc := metrics.NewCostCalculator(requestStore)

	var otelShutdown func(context.Context) error
	if *otelEndpoint != "" {
		otelShutdown, err = metrics.InitOTel(context.Background(), *otelEndpoint)
		if err != nil {
			log.Fatalf("init otel: %v", err)
		}
	}

	mux := http.NewServeMux()

	proxyHandler := proxy.NewHandler(requestStore, costCalc)
	mux.Handle("/v1/", proxyHandler)

	dash := dashboard.NewServer(requestStore, broadcaster, costCalc)
	mux.Handle("/ui", dash)
	mux.Handle("/ui/", dash)
	mux.Handle("/static/", dash)

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/ui", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})

	srv := &http.Server{
		Addr:         fmt.Sprintf("127.0.0.1:%d", *port),
		Handler:      mux,
		ReadTimeout:  5 * time.Minute,
		WriteTimeout: 10 * time.Minute,
		IdleTimeout:  120 * time.Second,
	}

	log.Printf("TalosRed CE listening on http://127.0.0.1:%d", *port)
	log.Printf("Proxy:     http://127.0.0.1:%d/v1/chat/completions", *port)
	log.Printf("Dashboard: http://127.0.0.1:%d/ui", *port)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("server shutdown: %v", err)
	}

	if otelShutdown != nil {
		if err := otelShutdown(ctx); err != nil {
			log.Printf("otel shutdown: %v", err)
		}
	}
}
