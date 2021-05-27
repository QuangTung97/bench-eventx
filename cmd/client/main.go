package main

import (
	"bench/benchpb"
	"context"
	"fmt"
	"google.golang.org/grpc"
	"io"
	"time"
)

func main() {
	conn, err := grpc.Dial("localhost:10088", grpc.WithInsecure())
	if err != nil {
		panic(err)
	}

	client := benchpb.NewBenchServiceClient(conn)
	stream, err := client.Watch(context.Background(), &benchpb.WatchRequest{
		From:  1,
		Limit: 512,
	})
	if err != nil {
		panic(err)
	}
	for {
		events, err := stream.Recv()
		if err == io.EOF {
			return
		}
		if err != nil {
			panic(err)
		}
		fmt.Println("EVENTS:", events)
		fmt.Println(time.Now())
	}
}
