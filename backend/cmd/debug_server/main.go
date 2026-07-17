package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"text-annotation-platform/runner"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sig
		cancel()
	}()

	log.Println("Starting debug server...")
	if err := runner.RunServer(ctx); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
