// conformance checks a model endpoint against spec/inference-api.md.
// This is the binary the R&D team runs against their Wan/Qwen servers before
// integration:
//
//	go run ./cmd/conformance -url http://localhost:9800 -token $TOKEN
//
// Note: the artifact-upload check starts a local HTTP receiver standing in
// for presigned PUT targets — the endpoint under test must be able to reach
// this machine (use a tunnel if the endpoint runs remotely).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/awm33/iris/backend/internal/conformance"
)

func main() {
	url := flag.String("url", "", "base URL of the endpoint under test (required)")
	token := flag.String("token", "dev", "bearer token")
	failureInjection := flag.Bool("failure-injection", false, "run FAIL:*/SLOW magic-prompt checks (endpoint must implement the injection hooks; the mock does)")
	timeout := flag.Duration("timeout", 10*time.Minute, "per-check timeout (real video endpoints are slow)")
	receiverHost := flag.String("receiver-host", "", "hostname the endpoint uses to reach this machine's artifact receiver (e.g. host.docker.internal for a dockerized endpoint)")
	flag.Parse()

	if *url == "" {
		flag.Usage()
		os.Exit(2)
	}

	results := conformance.Run(context.Background(), conformance.Config{
		BaseURL:          *url,
		Token:            *token,
		FailureInjection: *failureInjection,
		Timeout:          *timeout,
		ReceiverHost:     *receiverHost,
	})

	failed := 0
	for _, r := range results {
		switch {
		case r.Skipped:
			fmt.Printf("SKIP  %s\n", r.Name)
		case r.Err != nil:
			failed++
			fmt.Printf("FAIL  %-28s %v\n", r.Name, r.Err)
		default:
			fmt.Printf("PASS  %-28s %s\n", r.Name, r.Detail)
		}
	}
	if failed > 0 {
		fmt.Printf("\n%d check(s) failed\n", failed)
		os.Exit(1)
	}
	fmt.Println("\nall checks passed")
}
