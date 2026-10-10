package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Site struct {
	Host      string
	Feed      string
	Language  string
	Name      string
	Author    string
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
	schema := `
CREATE TABLE IF NOT EXISTS blog (
 host      TEXT PRIMARY KEY,
 feed      TEXT NOT NULL DEFAULT '',
 lang      TEXT NOT NULL DEFAULT '',
 name      TEXT NOT NULL DEFAULT '',
 author    TEXT NOT NULL DEFAULT '',
 tag       TEXT NOT NULL DEFAULT '',
 friend    TEXT NOT NULL DEFAULT '',
 framework TEXT NOT NULL DEFAULT '',
 state     TEXT NOT NULL DEFAULT 'pend',
 cert      INTEGER NOT NULL DEFAULT 0,
 create_at TEXT NOT NULL DEFAULT '',
 check_at  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS blog_queue ON blog (check_at, create_at);`
	if _, failure := connection.Exec(schema); failure != nil {
		connection.Close()
		return nil, failure
	}
	write, failure := connection.Prepare(`INSERT INTO blog (host,feed,lang,name,author,tag,friend,framework,state,cert,check_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)
	ON CONFLICT(host) DO UPDATE SET feed=excluded.feed,lang=excluded.lang,name=excluded.name,author=excluded.author,
	tag=excluded.tag,friend=excluded.friend,framework=excluded.framework,state=excluded.state,cert=excluded.cert,
	check_at=excluded.check_at`)
	if failure != nil {
		connection.Close()
		return nil, failure
	}
	return &BlogTable{database: connection, write: write}, nil
}

func (table *BlogTable) Close() error { return table.database.Close() }

func (table *BlogTable) Enqueue(hosts []string, now time.Time) (inserted int) {
	var failure error
	defer func() {
		if failure != nil {
			fmt.Fprintf(os.Stderr, "写入失败：外链%v\n", failure)
			inserted = 0
		}
	}()
	stamp := now.Format(time.RFC3339)
	transaction, failure := table.database.Begin()
	if failure != nil {
		return
	}
	defer transaction.Rollback()
	statement, failure := transaction.Prepare(`INSERT INTO blog (host, state, create_at, check_at) VALUES (?,?,?,?)
		ON CONFLICT(host) DO NOTHING`)
	if failure != nil {
		return
	}
	var handle sql.Result
	for _, raw := range hosts {
		host := Judge(raw)
		if host == "" {
			continue
		}
		handle, failure = statement.Exec(host, "pend", stamp, stamp)
		if failure != nil {
			return
		}
		if affected, _ := handle.RowsAffected(); affected > 0 {
			inserted++
		}
	}
	if failure = statement.Close(); failure != nil {
		return
	}
	if failure = transaction.Commit(); failure != nil {
		return
	}
	return
}

func (table *BlogTable) Dequeue(now time.Time, batch int, busy []string) ([]Site, error) {
	query := `SELECT host,feed,lang,name,author,tag,friend,framework,state,cert,check_at FROM blog
		WHERE check_at<>'' AND check_at<=?`
	arguments := []any{now.Format(time.RFC3339)}
	if len(busy) > 0 {
		query += ` AND host NOT IN (?` + strings.Repeat(",?", len(busy)-1) + `)`
		for _, host := range busy {
			arguments = append(arguments, host)
		}
	}
	rows, failure := table.database.Query(query+` ORDER BY check_at ASC LIMIT ?`,
		append(arguments, batch)...)
	if failure != nil {
		return nil, failure
	}
	defer rows.Close()
	var sites []Site
	for rows.Next() {
		site := Site{}
		var cert int
		if failure := rows.Scan(&site.Host, &site.Feed, &site.Language, &site.Name, &site.Author, &site.Tags,
			&site.Friend, &site.Framework, &site.State, &cert, &site.CheckAt); failure != nil {
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
	if failure != nil || !text.Valid {
		return 5 * time.Minute
	}
	when, failure := time.Parse(time.RFC3339, text.String)
	if failure != nil {
		return 5 * time.Minute
	}
	delay := time.Until(when)
	if delay < 5*time.Second {
		delay = 5 * time.Second
	}
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	return delay
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
	rows, failure := table.database.Query(`SELECT host,rowid FROM blog WHERE host IN (?`+
		strings.Repeat(",?", len(keys)-1)+`)`, arguments...)
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
	if len(ids) == 0 {
		return ""
	}
	text, failure := json.Marshal(ids)
	if failure != nil {
		return ""
	}
	return string(text)
}

func (table *BlogTable) Save(site *Site, outcome Outcome) {
	now := time.Now().UTC()
	schedule := func(delay time.Duration) { site.CheckAt = now.Add(delay).Format(time.RFC3339) }
	keep := func(field *string, value string) {
		if value != "" {
			*field = value
		}
	}
	switch outcome.State {
	case "alive":
		site.State = outcome.State
		keep(&site.Name, outcome.Name)
		keep(&site.Author, outcome.Author)
		keep(&site.Feed, outcome.Feed)
		keep(&site.Language, outcome.Language)
		keep(&site.Framework, outcome.Framework)
		keep(&site.Friend, outcome.Friend)
		schedule(30 * 24 * time.Hour)
	case "dead":
		site.State, site.CheckAt = outcome.State, ""
	default:
		if outcome.Local {
			schedule(24 * time.Hour)
			break
		}
		switch site.State {
		case "pend", "alive":
			site.State = "retry"
			schedule(24 * time.Hour)
		default:
			site.State, site.CheckAt = "dead", ""
		}
	}
	site.Cert = site.Cert || outcome.Cert
	_, failure := table.write.Exec(site.Host, site.Feed, site.Language, site.Name, site.Author, site.Tags,
		site.Friend, site.Framework, site.State, site.Cert, site.CheckAt)
	if failure != nil {
		fmt.Fprintf(os.Stderr, "写入 %s 失败：%v\n", site.Host, failure)
	}
}
