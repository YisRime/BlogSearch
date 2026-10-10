package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	table, failure := OpenBlogTable("data/blogsearch.db")
	fatal(failure)
	defer table.database.Close()
	if len(os.Args) > 1 && os.Args[1] == "seed" {
		fatal(SeedBlogs(table))
		return
	}
	task, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	fatal(crawlPipeline(task, table, NewCrawler()))
}

func fatal(failure error) {
	if failure != nil {
		fmt.Fprintln(os.Stderr, failure)
		os.Exit(1)
	}
}
