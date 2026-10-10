package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	table, failure := openTable("data/blogsearch.db")
	abort(failure)
	defer table.database.Close()
	if len(os.Args) > 1 && os.Args[1] == "seed" {
		abort(SeedBlogs(table))
		return
	}
	task, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	abort(runCrawl(task, table, newScout()))
}

func abort(failure error) {
	if failure != nil {
		fmt.Fprintln(os.Stderr, failure)
		os.Exit(1)
	}
}
