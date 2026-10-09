package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
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
	return crawlPipeline(task, table, NewCrawler())
}
