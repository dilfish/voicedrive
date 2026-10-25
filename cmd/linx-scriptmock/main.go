// linx-scriptmock runs one scripted local platform session without credentials or audio hardware.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dilfish/voicedrive/internal/scriptmock"
)

func run(ctx context.Context, args []string, output io.Writer) int {
	f := flag.NewFlagSet("linx-scriptmock", flag.ContinueOnError)
	f.SetOutput(output)
	path := f.String("script", "", "JSON script path (required)")
	address := f.String("listen", "127.0.0.1:18080", "loopback WebSocket listen address")
	mac := f.String("mac", "", "override expected MAC")
	report := f.String("report", "report.json", "new report file")
	adapterURL := f.String("adapter", "", "optional local device test-ingress HTTP URL")
	timeout := f.Duration("timeout", 0, "override script total timeout (max 4m)")
	if err := f.Parse(args); err != nil {
		return 2
	}
	if *path == "" || f.NArg() != 0 || *timeout < 0 || *timeout > 4*time.Minute {
		fmt.Fprintln(output, "invalid flags: -script required; timeout must be 0..4m")
		return 2
	}
	host, _, err := net.SplitHostPort(*address)
	if err != nil {
		fmt.Fprintln(output, "listen address:", err)
		return 2
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		fmt.Fprintln(output, "listen requires literal loopback IP")
		return 2
	}
	input, err := os.Open(*path)
	if err != nil {
		fmt.Fprintln(output, "open script:", err)
		return 2
	}
	s, parseErr := scriptmock.Parse(input)
	closeErr := input.Close()
	if parseErr != nil || closeErr != nil {
		fmt.Fprintln(output, "read script:", parseErr, closeErr)
		return 2
	}
	if *mac != "" {
		s.MAC = *mac
	}
	if *timeout > 0 {
		s.TimeoutMS = int((*timeout + time.Millisecond - 1) / time.Millisecond)
	}
	var adapter scriptmock.Adapter
	if *adapterURL != "" {
		a, err := scriptmock.NewHTTPAdapter(*adapterURL)
		if err != nil {
			fmt.Fprintln(output, err)
			return 2
		}
		adapter = a
	}
	m, err := scriptmock.New(s, adapter)
	if err != nil {
		fmt.Fprintln(output, err)
		return 2
	}
	file, err := os.OpenFile(*report, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		fmt.Fprintln(output, "create report:", err)
		return 2
	}
	listener, err := net.Listen("tcp", *address)
	if err != nil {
		fmt.Fprintln(output, "listen:", err)
		if e := file.Close(); e != nil {
			fmt.Fprintln(output, "close report:", e)
		}
		return 2
	}
	server := &http.Server{Handler: m, ReadHeaderTimeout: time.Second}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	fmt.Fprintln(output, "mock listening:", listener.Addr().String())
	r := m.Run(ctx)
	if err := server.Close(); err != nil {
		r.Passed = false
		r.Error += "; close server: " + err.Error()
	}
	if err := <-serveDone; err != nil && err != http.ErrServerClosed {
		r.Passed = false
		r.Error += "; serve: " + err.Error()
	}
	encodeErr := json.NewEncoder(file).Encode(r)
	closeErr = file.Close()
	if encodeErr != nil || closeErr != nil {
		fmt.Fprintln(output, "write report:", encodeErr, closeErr)
		return 2
	}
	if !r.Passed {
		fmt.Fprintln(output, r.Error)
		return 1
	}
	fmt.Fprintln(output, "script passed; report:", *report)
	return 0
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stderr)
	stop()
	os.Exit(code)
}
