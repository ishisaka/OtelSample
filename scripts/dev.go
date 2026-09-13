//go:build ignore

// Cross-platform development tasks. Run via mise or go run scripts/dev.go.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run scripts/dev.go build|verify|console|otlp|request")
		os.Exit(1)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func binary() string {
	name := "otelsample"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path, _ := filepath.Abs(filepath.Join(".work", "bin", name))
	return path
}

func run(task string) error {
	switch task {
	case "build":
		return build()
	case "verify":
		return verify()
	case "console", "otlp":
		cmd := exec.Command(binary())
		cmd.Env = telemetryEnv(task)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	case "request":
		return request("http://127.0.0.1:8080/relay?name=otelc", "Hello, otelc!\n")
	default:
		return fmt.Errorf("unknown task: %s", task)
	}
}

func build() error {
	if err := os.MkdirAll(filepath.Dir(binary()), 0755); err != nil {
		return err
	}
	// otelc changes go.mod and writes generated source files. Give it a copy.
	dir, err := os.MkdirTemp(".work", "build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	for _, name := range []string{"main.go", "go.mod", "go.sum"} {
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
			return err
		}
	}
	cmd := exec.Command("otelc", "go", "build", "-o", binary(), ".")
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("instrumented build: %w", err)
	}
	fmt.Println("Built", binary())
	return nil
}

func telemetryEnv(mode string) []string {
	// Keep results independent of the user's existing telemetry configuration.
	env := []string{}
	for _, value := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(value), "OTEL_") {
			env = append(env, value)
		}
	}
	exporter := "console"
	if mode == "otlp" {
		exporter = "otlp"
	}
	return append(env,
		"OTEL_SERVICE_NAME=otelsample",
		"OTEL_TRACES_EXPORTER="+exporter,
		"OTEL_METRICS_EXPORTER="+exporter,
		"OTEL_LOGS_EXPORTER="+exporter,
		"OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf",
		"OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318",
		"OTEL_TRACES_SAMPLER=always_on",
		"OTEL_BSP_SCHEDULE_DELAY=100",
		"OTEL_BLRP_SCHEDULE_DELAY=100",
		"OTEL_METRIC_EXPORT_INTERVAL=1000",
	)
}

func request(url, want string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK || string(body) != want {
		return fmt.Errorf("%s: status=%d body=%q, want 200 %q", url, resp.StatusCode, body, want)
	}
	fmt.Printf("PASS %s: %s", url, body)
	return nil
}

type spanContext struct {
	TraceID string
	SpanID  string
}

type record struct {
	Name         string
	SpanKind     int
	SpanContext  spanContext
	Parent       spanContext
	TraceID      string
	SpanID       string
	SeverityText string
	Body         struct{ Value string }
	Attributes   []struct {
		Key   string
		Value struct{ Value any }
	}
	ScopeMetrics []struct {
		Metrics []struct{ Name string }
	}
}

func verify() error {
	if err := os.MkdirAll(".work", 0755); err != nil {
		return err
	}
	output, err := os.Create(filepath.Join(".work", "telemetry.jsonl"))
	if err != nil {
		return err
	}
	defer output.Close()
	errorsFile, err := os.Create(filepath.Join(".work", "app.log"))
	if err != nil {
		return err
	}
	defer errorsFile.Close()
	cmd := exec.Command(binary(), "-port", "0")
	cmd.Env = telemetryEnv("console")
	cmd.Stdout, cmd.Stderr = output, errorsFile
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() { _ = cmd.Process.Kill(); <-done }()

	// Let the OS choose a free port; use the address announced by the server.
	address := regexp.MustCompile(`url=(http://127\.0\.0\.1:[0-9]+)`)
	deadline := time.Now().Add(20 * time.Second)
	baseURL := ""
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(errorsFile.Name())
		if err != nil {
			return err
		}
		if match := address.FindSubmatch(data); len(match) == 2 {
			baseURL = string(match[1])
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if baseURL == "" {
		return fmt.Errorf("server did not start; see %s", errorsFile.Name())
	}
	for _, test := range []struct{ path, want string }{
		{"/hello", "Hello, Gopher!\n"},
		{"/relay?name=A%26B", "Hello, A&B!\n"},
	} {
		if err := request(baseURL+test.path, test.want); err != nil {
			return err
		}
	}
	deadline = time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		ok, err := telemetryReady(output.Name())
		if err != nil {
			return err
		}
		if ok {
			fmt.Println("PASS relay -> HTTP client -> hello: same TraceID and correct parents")
			fmt.Println("PASS OTel INFO logs: greeting/name and upstream/status linked to their spans")
			fmt.Println("PASS Go runtime metrics; evidence: .work/telemetry.jsonl and .work/app.log")
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("trace chain, correlated OTel logs or Go runtime metrics missing; see %s", output.Name())
}

func telemetryReady(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	spans := map[string]record{}
	var logs []record
	hasRuntime := false
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	for scanner.Scan() {
		var r record
		// The last line may still be being written by the exporter.
		if json.Unmarshal(scanner.Bytes(), &r) != nil {
			continue
		}
		if r.SpanContext.SpanID != "" {
			spans[r.SpanContext.SpanID] = r
		}
		if r.Body.Value != "" {
			logs = append(logs, r)
		}
		for _, scope := range r.ScopeMetrics {
			for _, metric := range scope.Metrics {
				if strings.HasPrefix(metric.Name, "go.") {
					hasRuntime = true
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return false, err
	}
	for _, hello := range spans {
		client, clientOK := spans[hello.Parent.SpanID]
		relay, relayOK := spans[client.Parent.SpanID]
		trace := hello.SpanContext.TraceID
		if hello.Name == "GET /hello" && hello.SpanKind == 2 && clientOK && client.SpanKind == 3 &&
			relayOK && relay.Name == "GET /relay" && relay.SpanKind == 2 &&
			trace != "" && trace != "00000000000000000000000000000000" &&
			client.SpanContext.TraceID == trace && relay.SpanContext.TraceID == trace && hasRuntime &&
			hasLog(logs, "greeting requested", hello.SpanContext, "name", "A&B") &&
			hasLog(logs, "upstream responded", relay.SpanContext, "status", float64(200)) {
			return true, nil
		}
	}
	return false, nil
}

func hasLog(logs []record, message string, span spanContext, key string, value any) bool {
	for _, log := range logs {
		if log.Body.Value != message || log.SeverityText != "INFO" ||
			log.TraceID != span.TraceID || log.SpanID != span.SpanID {
			continue
		}
		for _, attr := range log.Attributes {
			if attr.Key == key && attr.Value.Value == value {
				return true
			}
		}
	}
	return false
}
