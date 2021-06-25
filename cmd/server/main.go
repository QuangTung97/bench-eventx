package main

import (
	"bench/benchpb"
	"context"
	"flag"
	"fmt"
	"github.com/QuangTung97/eventx"
	_ "github.com/go-sql-driver/mysql"
	grpc_zap "github.com/grpc-ecosystem/go-grpc-middleware/logging/zap"
	grpc_prometheus "github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/jmoiron/sqlx"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"time"
)

var mysqlHost = flag.String("mysql", "root:1@tcp(localhost:3306)/bench?parseTime=true", "MySQL DSN")

type repository struct {
	db *sqlx.DB
}

type benchEvent struct {
	ID   uint64 `db:"id"`
	Seq  uint64 `db:"seq"`
	Data string `db:"data"`
}

func transformBenchEvents(events []benchEvent) []eventx.Event {
	result := make([]eventx.Event, 0, len(events))
	for _, e := range events {
		result = append(result, eventx.Event{
			ID:   e.ID,
			Seq:  e.Seq,
			Data: e.Data,
		})
	}
	return result
}

func computeEventsForUpdate(events []eventx.Event) []benchEvent {
	result := make([]benchEvent, 0, len(events))
	for _, e := range events {
		result = append(result, benchEvent{
			ID:  e.ID,
			Seq: e.Seq,
		})
	}
	return result
}

func (r *repository) GetLastEvents(ctx context.Context, limit uint64) ([]eventx.Event, error) {
	fmt.Println("GetLastEvents:", limit)
	query := `
SELECT t.id, t.seq, t.data FROM (
	SELECT e.id, s.seq, e.data FROM events e
	INNER JOIN event_seqs s ON s.id = e.id
	ORDER BY s.seq DESC LIMIT ?
) t ORDER BY t.seq ASC
`

	var result []benchEvent
	err := r.db.SelectContext(ctx, &result, query, limit)
	if err != nil {
		return nil, err
	}
	return transformBenchEvents(result), nil
}

func (r *repository) GetUnprocessedEvents(ctx context.Context, limit uint64) ([]eventx.Event, error) {
	fmt.Println("GetUnprocessedEvents:", time.Now(), limit)
	query := `
SELECT e.id, e.data FROM events e
INNER JOIN event_seqs s ON s.id = e.id
WHERE s.seq IS NULL
ORDER BY s.id ASC LIMIT ?
`
	var result []benchEvent
	err := r.db.SelectContext(ctx, &result, query, limit)
	if err != nil {
		return nil, err
	}
	return transformBenchEvents(result), nil
}

func (r *repository) GetEventsFrom(ctx context.Context, from uint64, limit uint64) ([]eventx.Event, error) {
	fmt.Println("GetEventsFrom:", from, limit)
	query := `
SELECT e.id, s.seq, e.data FROM events e
INNER JOIN event_seqs s ON s.id = e.id
WHERE s.seq >= ?
ORDER BY s.seq ASC LIMIT ?
`
	var result []benchEvent
	err := r.db.SelectContext(ctx, &result, query, from, limit)
	if err != nil {
		return nil, err
	}
	return transformBenchEvents(result), nil
}

func (r *repository) UpdateSequences(ctx context.Context, events []eventx.Event) error {
	query := `
INSERT INTO event_seqs (id, seq)
VALUES (:id, :seq)
ON DUPLICATE KEY UPDATE seq = VALUES(seq)
`
	_, err := r.db.NamedExecContext(ctx, query, computeEventsForUpdate(events))
	return err
}

func newRepository(db *sqlx.DB) *repository {
	return &repository{
		db: db,
	}
}

type benchServer struct {
	benchpb.UnimplementedBenchServiceServer
	streamCtx context.Context
	runner    *eventx.Runner
}

func newBenchServer(streamCtx context.Context, runner *eventx.Runner) *benchServer {
	return &benchServer{
		streamCtx: streamCtx,
		runner:    runner,
	}
}

func (s *benchServer) Watch(req *benchpb.WatchRequest, server benchpb.BenchService_WatchServer) error {
	from := req.From
	if from == 0 {
		from = 1
	}

	limit := req.Limit
	if limit == 0 {
		limit = 256
	}

	sub := s.runner.NewSubscriber(from, limit)
	ctx, cancel := context.WithCancel(server.Context())
	go func() {
		<-s.streamCtx.Done()
		cancel()
	}()

	for {
		events, err := sub.Fetch(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err
		}

		sendEvents := make([]*benchpb.Event, 0, len(events))
		for _, e := range events {
			sendEvents = append(sendEvents, e.(*benchpb.Event))
		}
		err = server.Send(&benchpb.Events{
			Events: sendEvents,
		})
		if err != nil {
			return err
		}
	}
}

func (s *benchServer) Signal(_ context.Context, _ *benchpb.SignalRequest) (*benchpb.SignalResponse, error) {
	s.runner.Signal()
	return &benchpb.SignalResponse{}, nil
}

func unmarshalEvent(e eventx.Event) proto.Message {
	event := &benchpb.Event{}
	err := proto.Unmarshal([]byte(e.Data), event)
	if err != nil {
		panic(err)
	}
	event.Id = e.ID
	event.Seq = e.Seq
	return event
}

func getSequence(event proto.Message) uint64 {
	return (event.(*benchpb.Event)).Seq
}

func main() {
	flag.Parse()

	logger, err := zap.NewDevelopment()
	if err != nil {
		panic(err)
	}

	db := sqlx.MustConnect("mysql", *mysqlHost)

	repo := newRepository(db)
	runner := eventx.NewRunner(repo,
		unmarshalEvent, getSequence,
		eventx.WithDBProcessorRetryTimer(2*time.Minute),
		eventx.WithLogger(logger),
	)

	server := grpc.NewServer(
		grpc.ChainStreamInterceptor(
			grpc_prometheus.StreamServerInterceptor,
			grpc_zap.StreamServerInterceptor(logger),
		),
	)
	mux := runtime.NewServeMux()

	endpoint := "localhost:10088"

	ctx := context.Background()
	opts := []grpc.DialOption{grpc.WithInsecure()}

	streamCtx, cancel := context.WithCancel(ctx)

	benchpb.RegisterBenchServiceServer(server, newBenchServer(streamCtx, runner))
	err = benchpb.RegisterBenchServiceHandlerFromEndpoint(ctx, mux, endpoint, opts)
	if err != nil {
		panic(err)
	}

	grpc_prometheus.EnableHandlingTimeHistogram()
	grpc_prometheus.Register(server)

	startServers(streamCtx, cancel, server, mux, runner)
}

func startServers(
	streamCtx context.Context, cancelStream func(),
	server *grpc.Server, mux *runtime.ServeMux, runner *eventx.Runner,
) {
	exit := make(chan os.Signal, 1)
	signal.Notify(exit, os.Interrupt, os.Kill)

	httpMux := http.NewServeMux()
	httpMux.Handle("/metrics", promhttp.Handler())
	httpMux.Handle("/api/", mux)

	httpServer := &http.Server{
		Addr:    ":8080",
		Handler: httpMux,
	}

	fmt.Println("HTTP: localhost:8080")
	fmt.Println("gRPC: localhost:10088")

	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()

		runner.Run(streamCtx)
	}()

	go func() {
		defer wg.Done()

		err := httpServer.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			panic(err)
		}
	}()

	go func() {
		defer wg.Done()

		listener, err := net.Listen("tcp", ":10088")
		if err != nil {
			panic(err)
		}

		err = server.Serve(listener)
		if err != nil {
			panic(err)
		}
	}()

	<-exit

	cancelStream()

	ctx := context.Background()
	err := httpServer.Shutdown(ctx)
	if err != nil {
		panic(err)
	}
	server.GracefulStop()

	wg.Wait()
	fmt.Println("Graceful shutdown completed")
}
