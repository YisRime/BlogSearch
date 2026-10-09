package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if failure := run(); failure != nil {
		fmt.Fprintln(os.Stderr, failure)
		os.Exit(1)
	}
}

func run() error {
	table, failure := OpenBlogTable("data/blogsearch.db")
	if failure != nil {
		return failure
	}
	defer table.Close()
	task, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	crawler := NewCrawler()
	for round := 1; task.Err() == nil; round++ {
		if failure := crawlBatch(task, table, crawler, round); failure != nil {
			return failure
		}
		select {
		case <-task.Done():
		case <-time.After(5 * time.Minute):
		}
	}
	return nil
}
