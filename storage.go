package main

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// 站点数据行
type BlogSite struct {
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
	Retry     int
	Cert      bool
	CheckAt   string
}

// 站点数据库
type BlogTable struct {
	database *sql.DB
	write    *sql.Stmt
}

// 打开数据连接
func openTable(path string) (*BlogTable, error) {
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
	upsert, failure := connection.Prepare(`INSERT INTO blog (host,feed,lang,name,author,icp,summary,tag,friend,framework,state,retry,cert,check_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	ON CONFLICT(host) DO UPDATE SET feed=excluded.feed,lang=excluded.lang,name=excluded.name,author=excluded.author,
	icp=excluded.icp,summary=excluded.summary,tag=excluded.tag,friend=excluded.friend,framework=excluded.framework,
	state=excluded.state,retry=excluded.retry,cert=excluded.cert,check_at=excluded.check_at`)
	if failure != nil {
		connection.Close()
		return nil, failure
	}
	return &BlogTable{database: connection, write: upsert}, nil
}

// 加入待测主机
func (table *BlogTable) queueHosts(hosts []string, now time.Time) {
	stamp := now.Format(time.RFC3339)
	valid := make([]string, 0, len(hosts))
	for _, raw := range hosts {
		host := HostOf(raw)
		if host == "" || inDomainSet(host, blockHosts) {
			continue
		}
		valid = append(valid, host)
	}
	for start := 0; start < len(valid); start += 256 {
		batch := valid[start:min(start+256, len(valid))]
		arguments := make([]any, 0, len(batch)*4)
		for _, host := range batch {
			arguments = append(arguments, host, "pend", stamp, stamp)
		}
		query := `INSERT INTO blog (host, state, create_at, check_at) VALUES ` +
			strings.TrimSuffix(strings.Repeat(slots(4)+",", len(batch)), ",") + ` ON CONFLICT(host) DO NOTHING`
		if _, failure := table.database.Exec(query, arguments...); failure != nil {
			fmt.Fprintf(os.Stderr, "写入失败：外链 %v", failure)
		}
	}
}

// 提取到期记录
func (table *BlogTable) claimDue(now time.Time, batch int, busy []string) ([]BlogSite, error) {
	query := `SELECT host,feed,lang,name,author,icp,summary,tag,friend,framework,state,retry,cert,check_at FROM blog
		WHERE check_at<>'' AND check_at<=?`
	arguments := []any{now.Format(time.RFC3339)}
	if len(busy) > 0 {
		query += ` AND host NOT IN ` + slots(len(busy))
		for _, host := range busy {
			arguments = append(arguments, host)
		}
	}
	rows, failure := table.database.Query(query+` ORDER BY check_at ASC LIMIT ?`, append(arguments, batch)...)
	if failure != nil {
		return nil, failure
	}
	defer rows.Close()
	var sites []BlogSite
	for rows.Next() {
		site := BlogSite{}
		var cert int
		if failure := rows.Scan(&site.Host, &site.Feed, &site.Language, &site.Name, &site.Author, &site.ICP,
			&site.Summary, &site.Tags, &site.Friend, &site.Framework, &site.State, &site.Retry, &cert, &site.CheckAt); failure != nil {
			return nil, failure
		}
		site.Cert = cert != 0
		sites = append(sites, site)
	}
	return sites, rows.Err()
}

// 计算唤醒时间
func (table *BlogTable) nextWake(now time.Time) time.Duration {
	var text sql.NullString
	failure := table.database.QueryRow(`SELECT MIN(check_at) FROM blog WHERE check_at<>'' AND check_at>?`,
		now.Format(time.RFC3339)).Scan(&text)
	when, parseFailure := time.Parse(time.RFC3339, text.String)
	if failure != nil || parseFailure != nil {
		return 5 * time.Minute
	}
	return min(max(time.Until(when), 5*time.Second), 5*time.Minute)
}

// 读取友链编号
func (table *BlogTable) friendIndex(hosts []string) string {
	seen := map[string]bool{}
	keys := make([]string, 0, len(hosts))
	for _, raw := range hosts {
		host := HostOf(raw)
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
	rows, failure := table.database.Query(`SELECT host,rowid FROM blog WHERE host IN `+slots(len(keys)), arguments...)
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

// 保存数据库行
func (table *BlogTable) persist(site *BlogSite, result Result) {
	now := time.Now().UTC()
	schedule := func(delay time.Duration) { site.CheckAt = now.Add(delay).Format(time.RFC3339) }
	switch result.State {
	case "alive":
		site.State, site.Retry = "alive", 0
		site.Name = pickFirst(result.Name, site.Name)
		site.Author, site.Language = result.Author, result.Language
		site.Framework, site.ICP = result.Framework, result.ICP
		site.Summary = result.Summary
		site.Feed = pickFirst(result.Feed, site.Feed)
		site.Friend = pickFirst(result.Friend, site.Friend)
		delay := 30 * 24 * time.Hour
		if margin := result.CertEnd.Sub(now) - 3*24*time.Hour; !result.CertEnd.IsZero() && margin > 0 && margin < delay {
			delay = margin
		}
		schedule(delay)
	case "other", "dead":
		site.State, site.CheckAt, site.Retry = result.State, "", 0
	default:
		site.State, site.Retry = "pend", site.Retry+1
		if site.Retry >= 3 {
			site.State, site.CheckAt = "fail", ""
		} else {
			schedule(min(time.Duration(site.Retry)*24*time.Hour, 7*24*time.Hour))
		}
	}
	site.Cert = site.Cert || result.Cert
	if _, failure := table.write.Exec(site.Host, site.Feed, site.Language, site.Name, site.Author, site.ICP,
		site.Summary, site.Tags, site.Friend, site.Framework, site.State, site.Retry, site.Cert, site.CheckAt); failure != nil {
		fmt.Fprintf(os.Stderr, "写入 %s 失败：%v", site.Host, failure)
	}
}
