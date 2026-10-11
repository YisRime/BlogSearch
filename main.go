package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// 启动主程序
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

// 处理程序错误
func abort(failure error) {
	if failure != nil {
		fmt.Fprintln(os.Stderr, failure)
		os.Exit(1)
	}
}
