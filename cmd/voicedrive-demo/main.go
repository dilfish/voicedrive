// voicedrive-demo reports preflight blockers without launching an old media chain.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/dilfish/voicedrive/internal/demo"
)

func run(args []string, output io.Writer) int {
	flags := flag.NewFlagSet("voicedrive-demo", flag.ContinueOnError)
	flags.SetOutput(output)
	configPath := flags.String("config", "", "strict JSON config (optional)")
	reportPath := flags.String("report", "report.json", "new report file (0600)")
	check := flags.Bool("check", true, "run preflight")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if !*check || flags.NArg() != 0 {
		fmt.Fprintln(output, "only preflight is currently available")
		return 2
	}
	c := demo.DefaultConfig()
	if *configPath != "" {
		b, err := os.ReadFile(*configPath)
		if err != nil {
			fmt.Fprintln(output, "read config failed")
			return 2
		}
		c, err = demo.LoadConfig(b)
		if err != nil {
			fmt.Fprintln(output, err)
			return 2
		}
	}
	r := demo.Preflight(context.Background(), c)
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		fmt.Fprintln(output, "encode report failed")
		return 2
	}
	f, err := os.OpenFile(*reportPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		fmt.Fprintln(output, "create new report failed")
		return 2
	}
	_, writeErr := f.Write(append(b, '\n'))
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		fmt.Fprintln(output, "write report failed")
		return 2
	}
	fmt.Fprintln(output, "preflight:", r.Overall, "report:", *reportPath)
	if r.Overall != demo.StatusPassed {
		return 1
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stderr)) }
