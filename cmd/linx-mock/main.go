package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"time"

	"github.com/dilfish/voicedrive/internal/mock"
)

func main() {
	baseURL := flag.String("server", "http://localhost:8080", "voicedrive HTTP endpoint")
	scriptPath := flag.String("script", "", "JSON script path")
	reportPath := flag.String("report", "report.json", "report output path")
	listen := flag.String("listen", "", "serve one platform WebSocket session, e.g. 127.0.0.1:9090")
	mac := flag.String("mac", "", "required X-VD-MAC for WebSocket mode (optional)")
	timeout := flag.Duration("timeout", time.Minute, "maximum scenario duration including connection wait")
	flag.Parse()
	if *scriptPath == "" {
		log.Fatal("-script is required")
	}
	script, err := mock.Load(*scriptPath)
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	ctx, deadlineCancel := context.WithTimeout(ctx, *timeout)
	defer deadlineCancel()
	var report mock.Report
	if *listen == "" {
		report = mock.Run(ctx, *baseURL, script)
	} else {
		reports := make(chan mock.Report, 1)
		var accepted atomic.Bool
		mux := http.NewServeMux()
		mux.HandleFunc("/v1/ws/", func(w http.ResponseWriter, r *http.Request) {
			if *mac != "" && r.Header.Get("X-VD-MAC") != *mac {
				http.Error(w, "unexpected MAC", http.StatusForbidden)
				return
			}
			if !accepted.CompareAndSwap(false, true) {
				http.Error(w, "session already started", http.StatusConflict)
				return
			}
			conn, err := mock.Upgrade(w, r)
			if err != nil {
				accepted.Store(false)
				return
			}
			reports <- mock.ServeSession(ctx, conn, script)
		})
		listener, err := net.Listen("tcp", *listen)
		if err != nil {
			log.Fatal(err)
		}
		server := &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second}
		go func() { _ = server.Serve(listener) }()
		fmt.Printf("mock listening on ws://%s/v1/ws/\n", listener.Addr())
		select {
		case report = <-reports:
		case <-ctx.Done():
			report = mock.Report{Name: script.Name, Results: []mock.Result{{Name: "session", Error: ctx.Err().Error()}}}
		}
		_ = server.Close()
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*reportPath, append(data, '\n'), 0644); err != nil {
		log.Fatal(err)
	}
	for _, result := range report.Results {
		fmt.Printf("%s: %v %s\n", result.Name, result.Passed, result.Error)
	}
	if !report.Passed {
		os.Exit(1)
	}
}
