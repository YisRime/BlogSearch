package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"
)

type seedEntry struct {
	URL  string   `json:"url"`
	Name string   `json:"name"`
	Feed string   `json:"feed"`
	Tags []string `json:"tags"`
}

type seedCandidate struct {
	host string
	name string
	feed string
	tags string
}

func downloadSeed(target string) ([]byte, error) {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		time.Sleep(time.Duration(attempt) * 2 * time.Second)
		request, failure := http.NewRequest(http.MethodGet, target, nil)
		if failure != nil {
			return nil, failure
		}
		response, failure := (&http.Client{Timeout: 60 * time.Second}).Do(request)
		if failure != nil {
			last = failure
			continue
		}
		body, failure := io.ReadAll(io.LimitReader(response.Body, 8<<20))
		response.Body.Close()
		if failure != nil {
			last = failure
			continue
		}
		if response.StatusCode != http.StatusOK {
			last = fmt.Errorf("下载失败：%d", response.StatusCode)
			continue
		}
		return body, nil
	}
	return nil, last
}

func seedHost(raw string) string {
	host := normalizeHost(raw)
	if host == "" {
		return ""
	}
	host = HostOf(host)
	if host == "" {
		return ""
	}
	registrable := registrableOf(host)
	if registrable == "" || (host != registrable && host != "blog."+registrable) {
		return ""
	}
	if hubHosts[registrable] {
		return ""
	}
	return host
}

func (scout *Scout) probeHome(task context.Context, host string) (bool, bool) {
	reply, target, failure := scout.tryEither(task, &Result{}, "https://"+host+"/", true)
	if failure != nil {
		var timeout interface{ Timeout() bool }
		if errors.Is(failure, context.DeadlineExceeded) || (errors.As(failure, &timeout) && timeout.Timeout()) {
			return true, false
		}
		return false, true
	}
	if reply.Status == http.StatusOK {
		page := readPage(reply.Body, target)
		if errorPage(page.Name) || (page.Name == "" && page.Feed == "" && len(page.Links) == 0) {
			return false, false
		}
		return true, false
	}
	switch reply.Status {
	case 402, 404, 410, 444, 451, 456, 530:
		return false, false
	}
	return true, false
}

func (scout *Scout) probeSeed(task context.Context, candidate seedCandidate) bool {
	live, flaky := scout.probeHome(task, candidate.host)
	if !live && flaky {
		live, _ = scout.probeHome(task, candidate.host)
	}
	if live {
		return true
	}
	if candidate.feed == "" {
		return false
	}
	target := strings.TrimSpace(candidate.feed)
	if !strings.HasPrefix(target, "http") {
		target = "https://" + candidate.host + "/" + strings.TrimPrefix(target, "/")
	}
	reply, _, failure := scout.tryEither(task, &Result{}, target, true)
	return failure == nil && reply.Status == http.StatusOK && ParseSubscription(reply.Body).Parsed
}

func probeSeeds(task context.Context, scout *Scout, candidates []seedCandidate) []seedCandidate {
	if len(candidates) == 0 {
		return candidates
	}
	alive := make([]bool, len(candidates))
	queue := make(chan int)
	var pool sync.WaitGroup
	for range min(32, max(8, runtime.GOMAXPROCS(0)*4)) {
		pool.Add(1)
		go func() {
			defer pool.Done()
			for slot := range queue {
				probeCtx, cancel := context.WithTimeout(task, 25*time.Second)
				alive[slot] = scout.probeSeed(probeCtx, candidates[slot])
				cancel()
			}
		}()
	}
	go func() {
		for slot := range candidates {
			queue <- slot
		}
		close(queue)
	}()
	pool.Wait()
	kept := candidates[:0]
	for slot, candidate := range candidates {
		if alive[slot] {
			kept = append(kept, candidate)
		}
	}
	return kept
}

func SeedBlogs(table *BlogTable) error {
	body, failure := downloadSeed("https://cdn.jsdelivr.net/gh/timqian/chinese-independent-blogs@master/data/blogs.json")
	if failure != nil {
		return failure
	}
	var document struct {
		Blogs []seedEntry `json:"blogs"`
	}
	if failure := json.Unmarshal(body, &document); failure != nil {
		return failure
	}

	seen := map[string]bool{}
	candidates := make([]seedCandidate, 0, len(document.Blogs))
	for _, entry := range document.Blogs {
		host := seedHost(entry.URL)
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		tags := entry.Tags
		if tags == nil {
			tags = []string{}
		}
		encoded, failure := json.Marshal(tags)
		if failure != nil {
			return failure
		}
		candidates = append(candidates, seedCandidate{
			host: host, name: collapse(entry.Name), feed: strings.TrimSpace(entry.Feed), tags: string(encoded),
		})
	}
	skipped := len(document.Blogs) - len(candidates)

	kept := probeSeeds(context.Background(), newScout(), candidates)
	skipped += len(candidates) - len(kept)
	candidates = kept

	transaction, failure := table.database.Begin()
	if failure != nil {
		return failure
	}
	defer transaction.Rollback()
	insert, failure := transaction.Prepare(`INSERT INTO blog (host,state,create_at,check_at,name,feed,tag)
		VALUES (?,?,?,?,?,?,?) ON CONFLICT(host) DO NOTHING`)
	if failure != nil {
		return failure
	}
	defer insert.Close()
	fill, failure := transaction.Prepare(`UPDATE blog SET
		name = CASE WHEN COALESCE(name,'')='' THEN ? ELSE name END,
		feed = CASE WHEN COALESCE(feed,'')='' THEN ? ELSE feed END,
		tag  = CASE WHEN COALESCE(tag, '')='' THEN ? ELSE tag  END
		WHERE host = ?`)
	if failure != nil {
		return failure
	}
	defer fill.Close()
	stamp := time.Now().UTC().Format(time.RFC3339)
	inserted, existing := 0, 0
	for _, candidate := range candidates {
		handle, failure := insert.Exec(candidate.host, "pend", stamp, stamp,
			candidate.name, candidate.feed, candidate.tags)
		if failure != nil {
			return failure
		}
		if affected, _ := handle.RowsAffected(); affected > 0 {
			inserted++
			continue
		}
		existing++
		if _, failure := fill.Exec(candidate.name, candidate.feed, candidate.tags, candidate.host); failure != nil {
			return failure
		}
	}
	if failure := transaction.Commit(); failure != nil {
		return failure
	}
	fmt.Printf("初始化：新增%d行、已有%d行、跳过%d行", inserted, existing, skipped)
	return nil
}
