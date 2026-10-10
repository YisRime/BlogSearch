package main

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var schemaSQL string

type Site struct {
	Host      string
	Feed      string
	Language  string
	Name      string
	Author    string
	ICP       string
	Summary   string
	Tags      string
	Friend    string
	Framework string
	State     string
	Cert      bool
	CheckAt   string
}

type BlogTable struct {
	database *sql.DB
	write    *sql.Stmt
}

func placeholders(count int) string {
	return "(?" + strings.Repeat(",?", count-1) + ")"
}

func OpenBlogTable(path string) (*BlogTable, error) {
	if directory := filepath.Dir(path); directory != "" && directory != "." {
		if failure := os.MkdirAll(directory, 0o755); failure != nil {
			return nil, failure
		}
	}
	connection, failure := sql.Open("sqlite", fmt.Sprintf("%s?_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)",
		path, (5*time.Second).Milliseconds()))
	if failure != nil {
		return nil, failure
	}
	connection.SetMaxOpenConns(16)
	connection.SetMaxIdleConns(16)
	connection.SetConnMaxIdleTime(5 * time.Minute)
	connection.SetConnMaxLifetime(60 * time.Minute)
	if _, failure := connection.Exec(schemaSQL); failure != nil {
		connection.Close()
		return nil, failure
	}
	write, failure := connection.Prepare(`INSERT INTO blog (host,feed,lang,name,author,icp,summary,tag,friend,framework,state,cert,check_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
	ON CONFLICT(host) DO UPDATE SET feed=excluded.feed,lang=excluded.lang,name=excluded.name,author=excluded.author,
	icp=excluded.icp,summary=excluded.summary,tag=excluded.tag,friend=excluded.friend,framework=excluded.framework,
	state=excluded.state,cert=excluded.cert,check_at=excluded.check_at`)
	if failure != nil {
		connection.Close()
		return nil, failure
	}
	return &BlogTable{database: connection, write: write}, nil
}

func (table *BlogTable) Enqueue(hosts []string, now time.Time) {
	stamp := now.Format(time.RFC3339)
	failure := func() error {
		transaction, failure := table.database.Begin()
		if failure != nil {
			return failure
		}
		defer transaction.Rollback()
		statement, failure := transaction.Prepare(`INSERT INTO blog (host, state, create_at, check_at) VALUES (?,?,?,?)
		ON CONFLICT(host) DO NOTHING`)
		if failure != nil {
			return failure
		}
		defer statement.Close()
		for _, raw := range hosts {
			if host := Judge(raw); host != "" {
				if _, failure := statement.Exec(host, "pend", stamp, stamp); failure != nil {
					return failure
				}
			}
		}
		return transaction.Commit()
	}()
	if failure != nil {
		fmt.Fprintf(os.Stderr, "写入失败：外链 %v", failure)
	}
}

func (table *BlogTable) Dequeue(now time.Time, batch int, busy []string) ([]Site, error) {
	query := `SELECT host,feed,lang,name,author,icp,summary,tag,friend,framework,state,cert,check_at FROM blog
		WHERE check_at<>'' AND check_at<=?`
	arguments := []any{now.Format(time.RFC3339)}
	if len(busy) > 0 {
		query += ` AND host NOT IN ` + placeholders(len(busy))
		for _, host := range busy {
			arguments = append(arguments, host)
		}
	}
	rows, failure := table.database.Query(query+` ORDER BY check_at ASC LIMIT ?`, append(arguments, batch)...)
	if failure != nil {
		return nil, failure
	}
	defer rows.Close()
	var sites []Site
	for rows.Next() {
		site := Site{}
		var cert int
		if failure := rows.Scan(&site.Host, &site.Feed, &site.Language, &site.Name, &site.Author, &site.ICP,
			&site.Summary, &site.Tags, &site.Friend, &site.Framework, &site.State, &cert, &site.CheckAt); failure != nil {
			return nil, failure
		}
		site.Cert = cert != 0
		sites = append(sites, site)
	}
	return sites, rows.Err()
}

func (table *BlogTable) NextDue(now time.Time) time.Duration {
	var text sql.NullString
	failure := table.database.QueryRow(`SELECT MIN(check_at) FROM blog WHERE check_at<>'' AND check_at>?`,
		now.Format(time.RFC3339)).Scan(&text)
	when, parseFailure := time.Parse(time.RFC3339, text.String)
	if failure != nil || parseFailure != nil {
		return 5 * time.Minute
	}
	return min(max(time.Until(when), 5*time.Second), 5*time.Minute)
}

func (table *BlogTable) FriendOrdinals(hosts []string) string {
	seen := map[string]bool{}
	keys := make([]string, 0, len(hosts))
	for _, raw := range hosts {
		host := Judge(raw)
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		keys = append(keys, host)
	}
	if len(keys) == 0 {
		return ""
	}
	arguments := make([]any, len(keys))
	for index, host := range keys {
		arguments[index] = host
	}
	rows, failure := table.database.Query(`SELECT host,rowid FROM blog WHERE host IN `+placeholders(len(keys)), arguments...)
	if failure != nil {
		return ""
	}
	defer rows.Close()
	ordinals := map[string]int64{}
	for rows.Next() {
		var host string
		var id int64
		if failure := rows.Scan(&host, &id); failure != nil {
			return ""
		}
		ordinals[host] = id
	}
	if rows.Err() != nil {
		return ""
	}
	ids := make([]int64, 0, len(keys))
	for _, host := range keys {
		if id, ok := ordinals[host]; ok {
			ids = append(ids, id)
		}
	}
	text, failure := json.Marshal(ids)
	if failure != nil || len(ids) == 0 {
		return ""
	}
	return string(text)
}

func (table *BlogTable) Save(site *Site, outcome Outcome) {
	now := time.Now().UTC()
	schedule := func(delay time.Duration) { site.CheckAt = now.Add(delay).Format(time.RFC3339) }
	switch outcome.State {
	case "alive":
		site.State = "alive"
		for _, pair := range [][2]*string{
			{&site.Name, &outcome.Name}, {&site.Author, &outcome.Author}, {&site.Feed, &outcome.Feed},
			{&site.Language, &outcome.Language}, {&site.Framework, &outcome.Framework},
			{&site.ICP, &outcome.ICP}, {&site.Summary, &outcome.Summary}, {&site.Friend, &outcome.Friend},
		} {
			if *pair[1] != "" {
				*pair[0] = *pair[1]
			}
		}
		delay := 30 * 24 * time.Hour
		if margin := outcome.CertEnd.Sub(now) - 3*24*time.Hour; !outcome.CertEnd.IsZero() && margin > 0 && margin < delay {
			delay = margin
		}
		schedule(delay)
	case "other", "dead":
		site.State, site.CheckAt = outcome.State, ""
	default:
		if site.State == "alive" {
			schedule(24 * time.Hour)
		} else {
			site.State, site.CheckAt = "dead", ""
		}
	}
	site.Cert = site.Cert || outcome.Cert
	if _, failure := table.write.Exec(site.Host, site.Feed, site.Language, site.Name, site.Author, site.ICP,
		site.Summary, site.Tags, site.Friend, site.Framework, site.State, site.Cert, site.CheckAt); failure != nil {
		fmt.Fprintf(os.Stderr, "写入 %s 失败：%v", site.Host, failure)
	}
}
