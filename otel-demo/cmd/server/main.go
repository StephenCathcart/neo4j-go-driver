package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
	neo4jconfig "github.com/neo4j/neo4j-go-driver/v6/neo4j/config"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

const (
	defaultAppName      = "nodes-go-demo"
	defaultHTTPAddr     = ":8080"
	defaultNeo4jURI     = "neo4j://localhost:7687"
	defaultNeo4jUser    = "neo4j"
	defaultNeo4jPass    = "secret12345!"
	defaultOTLPEndpoint = "http://localhost:4318/v1/traces"
)

type config struct {
	AppName      string
	HTTPAddr     string
	Neo4jURI     string
	Neo4jUser    string
	Neo4jPass    string
	OTLPEndpoint string
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cfg := loadConfig()

	tp, err := setupTracerProvider(ctx, cfg)
	if err != nil {
		log.Fatalf("failed to configure tracing: %v", err)
	}
	defer func() {
		if tp != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := tp.Shutdown(shutdownCtx); err != nil {
				log.Printf("failed to shutdown tracer provider: %v", err)
			}
		}
	}()

	driver, err := neo4j.NewDriver(cfg.Neo4jURI, neo4j.BasicAuth(cfg.Neo4jUser, cfg.Neo4jPass, ""), func(conf *neo4jconfig.Config) {
		conf.MaxConnectionPoolSize = 50
	})
	if err != nil {
		log.Fatalf("failed to create neo4j driver: %v", err)
	}
	defer func() {
		if err := driver.Close(ctx); err != nil {
			log.Printf("failed to close neo4j driver: %v", err)
		}
	}()

	mux := http.NewServeMux()
	mux.Handle("/movies", otelhttp.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		movies, err := fetchMovies(r.Context(), driver)
		if err != nil {
			http.Error(w, "failed to fetch movies", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(movies); err != nil {
			http.Error(w, "failed to encode response", http.StatusInternalServerError)
		}
	}), "GET /movies"))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("starting %s on %s", cfg.AppName, cfg.HTTPAddr)
	if err := listenAndServe(ctx, server); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func loadConfig() config {
	return config{
		AppName:      getEnv("APP_NAME", defaultAppName),
		HTTPAddr:     getEnv("HTTP_ADDR", defaultHTTPAddr),
		Neo4jURI:     getEnv("NEO4J_URI", defaultNeo4jURI),
		Neo4jUser:    getEnv("NEO4J_USERNAME", defaultNeo4jUser),
		Neo4jPass:    getEnv("NEO4J_PASSWORD", defaultNeo4jPass),
		OTLPEndpoint: getEnv("OTLP_TRACES_ENDPOINT", defaultOTLPEndpoint),
	}
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func setupTracerProvider(ctx context.Context, cfg config) (*sdktrace.TracerProvider, error) {
	res, err := sdkresource.New(ctx,
		sdkresource.WithAttributes(
			semconv.ServiceName(cfg.AppName),
		),
	)
	if err != nil {
		return nil, err
	}

	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
		sdktrace.WithResource(res),
	}

	if cfg.OTLPEndpoint != "" {
		exporter, err := newOTLPExporter(ctx, cfg.OTLPEndpoint)
		if err != nil {
			return nil, err
		}
		opts = append(opts, sdktrace.WithBatcher(exporter))
	}

	tp := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return tp, nil
}

func newOTLPExporter(ctx context.Context, endpoint string) (sdktrace.SpanExporter, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid OTLP endpoint: %w", err)
	}

	if parsed.Scheme == "" {
		return nil, errors.New("OTLP endpoint must include scheme, e.g. http://localhost:4318/v1/traces")
	}

	host := parsed.Host
	if host == "" {
		host = parsed.Path
		parsed.Path = ""
	}

	opts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(host),
		otlptracehttp.WithURLPath(strings.TrimSuffix(parsed.Path+parsed.RawPath, "/")),
	}

	if strings.EqualFold(parsed.Scheme, "http") {
		opts = append(opts, otlptracehttp.WithInsecure())
	}

	return otlptracehttp.New(ctx, opts...)
}

func listenAndServe(ctx context.Context, server *http.Server) error {
	errCh := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return nil
	case err := <-errCh:
		return err
	}
}

func fetchMovies(ctx context.Context, driver neo4j.Driver) ([]map[string]any, error) {
	session := driver.NewSession(ctx, neo4j.SessionConfig{
		AccessMode: neo4j.AccessModeRead,
	})
	defer session.Close(ctx)

	result, err := session.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		query := `
			MATCH (movie:Movie)
			RETURN movie{
			  .tagline,
			  .title,
			  directors: [(movie)<-[:DIRECTED]-(directors:Person) | directors{.born, .name}],
			  actors: [(movie)<-[:ACTED_IN]-(actors:Person) | actors{.born, .name}]
			} AS movie
		`
		res, err := tx.Run(ctx, query, nil)
		if err != nil {
			return nil, err
		}

		movies := []map[string]any{}
		for res.Next(ctx) {
			if movie, ok := res.Record().Get("movie"); ok {
				if movieMap, ok := movie.(map[string]any); ok {
					movies = append(movies, movieMap)
				}
			}
		}

		if err := res.Err(); err != nil {
			return nil, err
		}

		return movies, nil
	})
	if err != nil {
		return nil, err
	}
	return result.([]map[string]any), nil
}
