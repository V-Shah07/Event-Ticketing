// Command analytics is the standalone analytics service. It exposes the
// AnalyticsService gRPC API that the core API pushes purchase events to.
package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/redis/go-redis/v9"
	"github.com/v-shah07/event-ticketing/internal/analytics"
	"github.com/v-shah07/event-ticketing/internal/cache"
	"github.com/v-shah07/event-ticketing/internal/config"
	analyticspb "github.com/v-shah07/event-ticketing/proto/analyticspb"
	"google.golang.org/grpc"
)

func main() {
	cfg := config.Load()

	// Redis is optional for the analytics service; it degrades to in-memory.
	rdb := redisOrNil(cfg.RedisAddr)

	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		log.Fatalf("listen %s: %v", cfg.GRPCAddr, err)
	}

	grpcServer := grpc.NewServer()
	analyticspb.RegisterAnalyticsServiceServer(grpcServer, analytics.NewServer(rdb))

	go func() {
		log.Printf("analytics gRPC listening on %s", cfg.GRPCAddr)
		if err := grpcServer.Serve(lis); err != nil {
			log.Fatalf("serve: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("analytics shutting down")
	grpcServer.GracefulStop()
}

func redisOrNil(addr string) *redis.Client {
	rdb, err := cache.New(context.Background(), addr)
	if err != nil {
		log.Printf("analytics: redis unavailable (%v); running in-memory only", err)
		return nil
	}
	log.Println("analytics: redis connected")
	return rdb
}
