package main

import (
	"database/sql"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	StateSeed  = "seed"
	StatePend  = "pend"
	StateRetry = "retry"
	StateAlive = "alive"
	StateDeny  = "deny"
	StateDead  = "dead"
)

const schemaText = `
CREATE TABLE IF NOT EXISTS blog (
 host      TEXT PRIMARY KEY,
 state     TEXT NOT NULL DEFAULT 'pend',
 name      TEXT NOT NULL DEFAULT '',
 author    TEXT NOT NULL DEFAULT '',
 feed      TEXT NOT NULL DEFAULT '',
 lang      TEXT NOT NULL DEFAULT '',
 framework TEXT NOT NULL DEFAULT '',
 tag       TEXT NOT NULL DEFAULT '',
 create_at TEXT NOT NULL DEFAULT '',
 check_at  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS blog_queue ON blog (check_at, create_at);`

var siteColumns = []string{"host", "state", "name", "author", "feed", "lang", "framework", "tag", "check_at"}

const insertText = `INSERT INTO blog (host,state,name,author,feed,lang,framework,tag,check_at) VALUES (?,?,?,?,?,?,?,?,?)
	ON CONFLICT(host) DO UPDATE SET state=excluded.state,name=excluded.name,author=excluded.author,
	feed=excluded.feed,lang=excluded.lang,framework=excluded.framework,tag=excluded.tag,check_at=excluded.check_at`

type Site struct {
	Host      string
	State     string
	Name      string
	Author    string
	Feed      string
	Language  string
	Framework string
	Tags      string
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
	connection, failure := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if failure != nil {
		return nil, failure
	}
	if _, failure := connection.Exec(schemaText); failure != nil {
		connection.Close()
		return nil, failure
	}
	write, failure := connection.Prepare(insertText)
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
		handle, failure = statement.Exec(host, StatePend, stamp, stamp)
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

func (table *BlogTable) Dequeue(now time.Time) ([]Site, error) {
	rows, failure := table.database.Query(`SELECT `+strings.Join(siteColumns, `,`)+` FROM blog
		WHERE check_at<>'' AND check_at<=? ORDER BY create_at ASC`, now.Format(time.RFC3339))
	if failure != nil {
		return nil, failure
	}
	defer rows.Close()
	var sites []Site
	for rows.Next() {
		site := Site{}
		if failure := rows.Scan(&site.Host, &site.State, &site.Name, &site.Author, &site.Feed, &site.Language,
			&site.Framework, &site.Tags, &site.CheckAt); failure != nil {
			return nil, failure
		}
		sites = append(sites, site)
	}
	return sites, rows.Err()
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
	case StateAlive:
		site.State = outcome.State
		keep(&site.Name, outcome.Name)
		keep(&site.Author, outcome.Author)
		keep(&site.Feed, outcome.Feed)
		keep(&site.Language, outcome.Language)
		keep(&site.Framework, outcome.Framework)
		schedule(30 * 24 * time.Hour)
	case StateDeny, StateDead:
		site.State, site.CheckAt = outcome.State, ""
	default:
		if outcome.Local {
			schedule(24 * time.Hour)
			break
		}
		switch site.State {
		case StateSeed, StatePend, StateAlive:
			site.State = StateRetry
			schedule(24 * time.Hour)
		default:
			site.State, site.CheckAt = StateDead, ""
		}
	}
	_, failure := table.write.Exec(site.Host, site.State, site.Name, site.Author, site.Feed,
		site.Language, site.Framework, site.Tags, site.CheckAt)
	if failure != nil {
		fmt.Fprintf(os.Stderr, "写入 %s 失败：%v\n", site.Host, failure)
	}
}
