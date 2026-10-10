package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type seedEntry struct {
	URL  string   `json:"url"`
	Name string   `json:"name"`
	Feed string   `json:"feed"`
	Tags []string `json:"tags"`
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
	seen := map[string]bool{}
	inserted, existing, skipped := 0, 0, 0
	for _, entry := range document.Blogs {
		host := seedHost(entry.URL)
		if host == "" || seen[host] {
			skipped++
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
		name, feed := collapse(entry.Name), strings.TrimSpace(entry.Feed)
		handle, failure := insert.Exec(host, "pend", stamp, stamp, name, feed, string(encoded))
		if failure != nil {
			return failure
		}
		if affected, _ := handle.RowsAffected(); affected > 0 {
			inserted++
			continue
		}
		existing++
		if _, failure := fill.Exec(name, feed, string(encoded), host); failure != nil {
			return failure
		}
	}
	if failure := transaction.Commit(); failure != nil {
		return failure
	}
	fmt.Printf("初始化：新增%d行、已有%d行、跳过%d行", inserted, existing, skipped)
	return nil
}
