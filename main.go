// mise run console でコンパイル時に自動計装して起動します。
// mise run plain では計装されません。実行手順は README.md を参照。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
)

func main() {
	// SDKとExporterはotelcが初期化します。ここではslogをOTelへ橋渡しし、
	// 通常のターミナル出力も残します。通常ビルドではOTel側はno-opです。
	slog.SetDefault(slog.New(slog.NewMultiHandler(
		slog.NewTextHandler(os.Stderr, nil),
		otelslog.NewHandler("OtelSample"),
	)))
	port := flag.Int("port", 8080, "HTTP server port")
	flag.Parse()
	if err := run(*port); err != nil {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}

func run(port int) error {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return err
	}
	defer listener.Close()
	baseURL := "http://" + listener.Addr().String()
	client := &http.Client{Timeout: 3 * time.Second}
	defer client.CloseIdleConnections()
	mux := http.NewServeMux()

	// HTTPサーバーの処理と、リクエストに紐づくログを観測します。
	mux.HandleFunc("GET /hello", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if name == "" {
			name = "Gopher"
		}
		slog.InfoContext(r.Context(), "greeting requested", "name", name)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "Hello, %s!\n", name)
	})

	// 同じサーバーの /hello を呼び、受信→送信→受信のスパンを観測します。
	// Contextは通常のHTTP APIで引き継ぎ、手動の計装コードは書きません。
	mux.HandleFunc("GET /relay", func(w http.ResponseWriter, r *http.Request) {
		target := baseURL + "/hello?name=" + url.QueryEscape(r.URL.Query().Get("name"))
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
		if err != nil {
			http.Error(w, "failed to create request", http.StatusInternalServerError)
			return
		}
		resp, err := client.Do(req)
		if err != nil {
			slog.ErrorContext(r.Context(), "upstream request failed", "error", err)
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		slog.InfoContext(r.Context(), "upstream responded", "status", resp.StatusCode)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(resp.StatusCode)
		if _, err := io.Copy(w, resp.Body); err != nil {
			slog.ErrorContext(r.Context(), "failed to write response", "error", err)
		}
	})

	server := &http.Server{
		Handler: mux, ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	slog.Info("HTTP server listening", "url", baseURL, "example", baseURL+"/relay?name=otelc")
	select {
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}
