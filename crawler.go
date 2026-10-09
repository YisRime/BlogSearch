package main

// 抓取存活判定
import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 一页应答
type PageReply struct {
	Status    int
	Body      []byte
	Location  string
	Truncated bool
}

// 单站结论
type Outcome struct {
	State     string
	Local     bool
	Reason    string
	Links     []string
	Requests  int
	Bytes     int
	Name      string
	Author    string
	Feed      string
	Language  string
	Framework string
}

// 单轮汇总
type BatchTally struct {
	Requests int
	Bytes    int
	Expanded int
}

// 抓取组件
type Crawler struct {
	client   *http.Client
	follower *http.Client
}

// 构造抓取器
func NewCrawler() *Crawler {
	transport := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 5 * time.Second,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS11},
		MaxIdleConns:        256,
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     30 * time.Second,
		ForceAttemptHTTP2:   true,
	}
	return &Crawler{
		client: &http.Client{Transport: transport, Timeout: 30 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }},
		follower: &http.Client{Transport: transport, Timeout: 30 * time.Second},
	}
}

// 取回一页
func (crawler *Crawler) fetch(task context.Context, follow bool, target string) (*PageReply, error) {
	client := crawler.client
	if follow {
		client = crawler.follower
	}
	request, failure := http.NewRequestWithContext(task, http.MethodGet, target, nil)
	if failure != nil {
		return nil, failure
	}
	request.Header.Set("User-Agent", "blogsearch")
	request.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	response, failure := client.Do(request)
	if failure != nil {
		return nil, failure
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, pageLimit))
	declared, _ := strconv.Atoi(response.Header.Get("Content-Length"))
	return &PageReply{Status: response.StatusCode, Body: body,
		Location:  strings.TrimSpace(response.Header.Get("Location")),
		Truncated: declared > len(body)}, nil
}

// 正文读取上限
const pageLimit = 128 << 10

// 订阅路径表
var feedPaths = []string{"/atom.xml", "/index.xml", "/feed.xml", "/feed", "/rss.xml"}

// 站点地图路径表
var sitemapPaths = []string{"/sitemap.xml", "/sitemap_index.xml"}

// 按站点拼地址
func pathsOn(host string, guesses []string) []string {
	addresses := make([]string, 0, len(guesses))
	for _, guess := range guesses {
		addresses = append(addresses, "https://"+host+guess)
	}
	return addresses
}

// 串接说明
func joinReason(before, after string) string {
	if before == "" {
		return after
	}
	return before + "，" + after
}

// 外链收集
type collector struct {
	seen  map[string]bool
	items []string
}

// 归一入列
func (collector *collector) add(raw string) {
	hostname := normalizeHost(raw)
	if hostname == "" || collector.seen[hostname] {
		return
	}
	collector.seen[hostname] = true
	collector.items = append(collector.items, hostname)
}

// 取首个可用页
func (crawler *Crawler) probe(task context.Context, outcome *Outcome, candidates []string) (*PageReply, string) {
	for _, candidate := range candidates {
		reply, failure := crawler.fetch(task, true, candidate)
		outcome.Requests++
		if failure != nil || reply.Status != http.StatusOK {
			continue
		}
		outcome.Bytes += len(reply.Body)
		return reply, candidate
	}
	return nil, ""
}

// 取回首页
func (crawler *Crawler) home(task context.Context, site *Site, outcome *Outcome, registrable string, collector *collector) (Page, bool) {
	addresses, failure := net.DefaultResolver.LookupNetIP(task, "ip", site.Host)
	if failure != nil {
		var record *net.DNSError
		if errors.As(failure, &record) && record.IsNotFound {
			outcome.State, outcome.Reason = StateDead, "域名不存在："+site.Host
		} else {
			outcome.State, outcome.Local, outcome.Reason = StatePend, true, "域名解析失败："+failure.Error()
		}
		return Page{}, false
	}
	if len(addresses) == 0 {
		outcome.State, outcome.Reason = StateDead, "解析地址无效"
		return Page{}, false
	}
	target := "https://" + site.Host + "/"
	var reply *PageReply
	for hop := 0; hop <= 2; hop++ {
		var failure error
		reply, failure = crawler.fetch(task, false, target)
		outcome.Requests++
		if failure != nil {
			outcome.State, outcome.Reason = StatePend, "首页抓取失败："+failure.Error()
			if text := failure.Error(); strings.Contains(text, "no such host") || strings.Contains(text, "lookup") {
				outcome.State = StateDead
			}
			return Page{}, false
		}
		outcome.Bytes += len(reply.Body)
		if reply.Status < 300 || reply.Status >= 400 {
			break
		}
		next := reply.Location
		if location, failure := url.Parse(next); failure == nil && !location.IsAbs() {
			if root, failure := url.Parse(target); failure == nil {
				next = root.ResolveReference(location).String()
			}
		}
		hostname := normalizeHost(next)
		if hostname == "" {
			outcome.State, outcome.Reason = StatePend, "跳转无效"
			return Page{}, false
		}
		collector.add(hostname)
		if hop < 2 && registrableOf(hostname) == registrable {
			target = next
			continue
		}
		outcome.State, outcome.Reason = StatePend, "站外跳转："+hostname
		return Page{}, false
	}
	if reply.Status >= 400 {
		outcome.State, outcome.Reason = StatePend, "首页应答失败："+strconv.Itoa(reply.Status)
		return Page{}, false
	}
	page := ParsePage(reply.Body, target)
	for _, link := range page.Links {
		collector.add(link)
	}
	outcome.Name, outcome.Author, outcome.Language, outcome.Framework = page.Name, page.Author, page.Language, page.Framework
	if reply.Truncated {
		outcome.Reason = joinReason(outcome.Reason, "正文被截断")
	}
	return page, true
}

// 抓友链拓外链
func (crawler *Crawler) expand(task context.Context, site *Site, page Page, outcome *Outcome, registrable string, collector *collector) {
	friends := make([]string, 0, len(page.Friends)+len(friendPaths))
	friends = append(friends, page.Friends...)
	friends = append(friends, pathsOn(site.Host, friendPaths)...)
	if reply, target := crawler.probe(task, outcome, friends); reply != nil {
		for _, link := range ParsePage(reply.Body, target).Links {
			if registrableOf(link) != registrable {
				collector.add(link)
			}
		}
	}
}

// 单站全流程
func (crawler *Crawler) Crawl(task context.Context, site *Site, full bool) Outcome {
	outcome := Outcome{State: StateAlive}
	collector := &collector{seen: map[string]bool{site.Host: true}}
	registrable := registrableOf(site.Host)
	page, reached := crawler.home(task, site, &outcome, registrable, collector)
	if !reached {
		return outcome
	}
	if full {
		candidates := make([]string, 0, 1+len(feedPaths))
		tried := map[string]bool{}
		addCandidate := func(raw string) {
			if raw != "" && !tried[raw] {
				tried[raw] = true
				candidates = append(candidates, raw)
			}
		}
		addCandidate(site.Feed)
		if page.Feed != "" {
			addCandidate(page.Feed)
		} else {
			for _, address := range pathsOn(site.Host, feedPaths) {
				addCandidate(address)
			}
		}
		for _, candidate := range candidates {
			reply, failure := crawler.fetch(task, true, candidate)
			outcome.Requests++
			if failure != nil {
				outcome.Reason = joinReason(outcome.Reason, "订阅源失败："+candidate)
				continue
			}
			if reply.Status != http.StatusOK {
				if page.Feed != "" {
					outcome.Reason = joinReason(outcome.Reason, "订阅源失败："+strconv.Itoa(reply.Status))
					break
				}
				continue
			}
			outcome.Bytes += len(reply.Body)
			subscription := ParseSubscription(reply.Body)
			if !subscription.Parsed {
				continue
			}
			outcome.Feed = candidate
			if subscription.Name != "" {
				outcome.Name = subscription.Name
			}
			if len(subscription.Authors) == 1 {
				outcome.Author = subscription.Authors[0]
			}
			if subscription.Language != "" {
				outcome.Language = subscription.Language
			}
			break
		}
		if reply, _ := crawler.probe(task, &outcome, pathsOn(site.Host, sitemapPaths)); reply != nil {
			for _, link := range ParseSitemap(reply.Body) {
				collector.add(link)
			}
		}
	}
	crawler.expand(task, site, page, &outcome, registrable, collector)
	outcome.Links = collector.items
	return outcome
}

// 并发跑一轮
func crawlBatch(task context.Context, table *BlogTable, crawler *Crawler, round int) error {
	batch, failure := table.Dequeue(time.Now().UTC())
	if failure != nil {
		return failure
	}
	var group sync.WaitGroup
	var printing sync.Mutex
	jobs := make(chan Site)
	workers := 8
	tallies := make([]BatchTally, workers)
	for slot := range tallies {
		group.Add(1)
		go func() {
			defer group.Done()
			tally := &tallies[slot]
			for site := range jobs {
				var outcome Outcome
				if site.State == StateDead || site.State == StateDeny {
					outcome.State = site.State
				} else {
					outcome = crawler.Crawl(task, &site, site.State != StateAlive)
				}
				if outcome.State == StateAlive {
					tally.Expanded += table.Enqueue(outcome.Links, time.Now().UTC())
				}
				table.Save(&site, outcome)
				tally.Requests += outcome.Requests
				tally.Bytes += outcome.Bytes
				if outcome.Reason != "" {
					printing.Lock()
					fmt.Printf("(%s)%s\n", site.Host, outcome.Reason)
					printing.Unlock()
				}
			}
		}()
	}
	for _, site := range batch {
		jobs <- site
	}
	close(jobs)
	group.Wait()
	var tally BatchTally
	for _, count := range tallies {
		tally.Requests += count.Requests
		tally.Bytes += count.Bytes
		tally.Expanded += count.Expanded
	}
	fmt.Printf("第%d轮：新增%d站，共计请求%d次/%d字节\n",
		round, tally.Expanded, tally.Requests, tally.Bytes)
	return nil
}
