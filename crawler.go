package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"golang.org/x/net/html/charset"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type PageReply struct {
	Status   int
	Body     []byte
	Location string
	Language string
	CertEnd  time.Time
}

type Outcome struct {
	State     string
	Reason    string
	Links     []string
	Friends   []string
	Friend    string
	Requests  int
	Timeouts  int
	Cert      bool
	CertEnd   time.Time
	Name      string
	Author    string
	Feed      string
	Language  string
	Framework string
	ICP       string
	Summary   string
}

type BatchTally struct {
	Requests int
	Timeouts int
}

type Crawler struct {
	client      *http.Client
	follower    *http.Client
	loose       *http.Client
	looseFollow *http.Client

	guard   sync.Mutex
	latency float64
	learned map[string]string
}

func newTransport(relaxed bool, maxConns, perHost int) *http.Transport {
	return &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS11, InsecureSkipVerify: relaxed},
		MaxIdleConns:        maxConns,
		MaxIdleConnsPerHost: perHost,
		IdleConnTimeout:     30 * time.Second,
		ForceAttemptHTTP2:   true,
	}
}

func NewCrawler() *Crawler {
	floor := runtime.GOMAXPROCS(0)
	ceiling := floor * 4
	strict, relaxed := newTransport(false, ceiling, floor), newTransport(true, ceiling, floor)
	hold := func(transport *http.Transport) *http.Client {
		return &http.Client{Transport: transport, Timeout: 60 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	}
	pass := func(transport *http.Transport) *http.Client {
		return &http.Client{Transport: transport, Timeout: 60 * time.Second}
	}
	return &Crawler{
		client: hold(strict), follower: pass(strict), loose: hold(relaxed), looseFollow: pass(relaxed),
		latency: 4, learned: map[string]string{},
	}
}

func (crawler *Crawler) Timeout() time.Duration {
	crawler.guard.Lock()
	latency := crawler.latency
	crawler.guard.Unlock()
	return min(max(time.Duration(4*latency*float64(time.Second)), 10*time.Second), 60*time.Second)
}

func (crawler *Crawler) recall(key string) string {
	crawler.guard.Lock()
	defer crawler.guard.Unlock()
	return crawler.learned[key]
}

func (crawler *Crawler) memorize(key, target string) {
	parsed, failure := url.Parse(target)
	if failure != nil || parsed.Path == "" {
		return
	}
	crawler.guard.Lock()
	crawler.learned[key] = parsed.Path
	crawler.guard.Unlock()
}

func (crawler *Crawler) fetch(task context.Context, target string, follow, relaxed bool) (*PageReply, error) {
	client := crawler.client
	switch {
	case relaxed && follow:
		client = crawler.looseFollow
	case relaxed:
		client = crawler.loose
	case follow:
		client = crawler.follower
	}
	request, failure := http.NewRequestWithContext(task, http.MethodGet, target, nil)
	if failure != nil {
		return nil, failure
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	response, failure := client.Do(request)
	if failure != nil {
		return nil, failure
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	decoded := body
	if reader, failure := charset.NewReader(bytes.NewReader(body), response.Header.Get("Content-Type")); failure == nil {
		if text, failure := io.ReadAll(reader); failure == nil && len(text) > 0 {
			decoded = text
		}
	}
	reply := &PageReply{Status: response.StatusCode, Body: decoded,
		Location: strings.TrimSpace(response.Header.Get("Location")),
		Language: strings.TrimSpace(response.Header.Get("Content-Language"))}
	if response.TLS != nil && len(response.TLS.PeerCertificates) > 0 {
		reply.CertEnd = response.TLS.PeerCertificates[0].NotAfter
	}
	return reply, nil
}

func (crawler *Crawler) attempt(task context.Context, outcome *Outcome, target string, follow, relaxed bool) (*PageReply, error) {
	started := time.Now()
	reply, failure := crawler.fetch(task, target, follow, relaxed)
	elapsed := time.Since(started)
	outcome.Requests++
	if failure == nil {
		crawler.guard.Lock()
		crawler.latency += (elapsed.Seconds() - crawler.latency) * 0.1
		crawler.guard.Unlock()
		return reply, nil
	}
	var timeout interface{ Timeout() bool }
	if errors.Is(failure, context.DeadlineExceeded) ||
		(errors.As(failure, &timeout) && timeout.Timeout()) {
		outcome.Timeouts++
	}
	return reply, failure
}

func (crawler *Crawler) fetchEither(task context.Context, outcome *Outcome, target string, follow bool) (*PageReply, string, error) {
	reply, failure := crawler.attempt(task, outcome, target, follow, false)
	if failure == nil {
		return reply, target, nil
	}
	var certFailure *tls.CertificateVerificationError
	if errors.As(failure, &certFailure) {
		outcome.Cert = true
		if reply, failure = crawler.attempt(task, outcome, target, follow, true); failure == nil {
			return reply, target, nil
		}
	}
	fallback := ""
	if strings.HasPrefix(target, "https://") {
		fallback = "http://" + strings.TrimPrefix(target, "https://")
	}
	refused := errors.Is(failure, syscall.ECONNREFUSED)
	closed := errors.Is(failure, io.EOF) || errors.Is(failure, io.ErrUnexpectedEOF)
	if fallback == "" || (!refused && !closed) {
		return nil, target, failure
	}
	reply, failure = crawler.attempt(task, outcome, fallback, follow, false)
	return reply, fallback, failure
}

func (crawler *Crawler) probe(task context.Context, outcome *Outcome, candidates []string) (*PageReply, string) {
	for _, candidate := range candidates {
		reply, target, failure := crawler.fetchEither(task, outcome, candidate, true)
		if failure != nil || reply.Status != http.StatusOK {
			continue
		}
		return reply, target
	}
	return nil, ""
}

func fail(outcome *Outcome, state, reason, payload string) {
	outcome.State = state
	if payload == "" {
		outcome.Reason = reason
		return
	}
	outcome.Reason = reason + "：" + payload
}

var hubHosts = map[string]bool{
	"github.com": true, "zhihu.com": true, "weibo.com": true, "csdn.net": true,
	"jianshu.com": true, "facebook.com": true, "twitter.com": true, "x.com": true,
	"youtube.com": true, "medium.com": true, "douban.com": true, "bilibili.com": true,
	"linkedin.com": true, "instagram.com": true, "pinterest.com": true, "reddit.com": true,
	"tumblr.com": true, "wordpress.com": true, "blogspot.com": true, "wanandroid.com": true,
	"ituring.com.cn": true, "foreverblog.cn": true, "storeweb.cn": true,
	"listbox.app": true, "urschool.org": true, "cafenomad.tw": true,
}

func notBlog(site *Site, page Page, outcome *Outcome) bool {
	if outcome.Feed != "" || site.Feed != "" || outcome.Framework != "" || outcome.ICP != "" ||
		page.HasArchive || len(outcome.Friends) > 0 {
		return false
	}
	mark := strings.ToLower(collapse(page.Name))
	return hubHosts[registrableOf(site.Host)] || mark == "" || mark == "首页" || mark == "home" ||
		slices.ContainsFunc([]string{"index of", "网站维护中", "建站中", "维护中", "coming soon",
			"域名出售", "域名停放", "网址导航", "搜索引擎"}, func(word string) bool {
			return strings.Contains(mark, word)
		})
}

func pathsOn(host string, guesses []string) []string {
	addresses := make([]string, 0, len(guesses))
	for _, guess := range guesses {
		addresses = append(addresses, "https://"+host+guess)
	}
	return addresses
}

type collector struct {
	seen  map[string]bool
	items []string
}

func (collector *collector) add(raw string) string {
	hostname := normalizeHost(raw)
	if hostname == "" || collector.seen[hostname] {
		return ""
	}
	collector.seen[hostname] = true
	collector.items = append(collector.items, hostname)
	return hostname
}

func (crawler *Crawler) home(task context.Context, site *Site, outcome *Outcome, registrable string, collector *collector) (Page, bool) {
	dead := func(failure error) {
		record := &net.DNSError{}
		rejected := errors.As(failure, &record) && !record.IsTemporary &&
			(record.IsNotFound || strings.Contains(record.Err, "no data"))
		if rejected {
			outcome.State, outcome.Reason = "dead", "地址无效"
			return
		}
		fail(outcome, "pend", "抓取失败", failure.Error())
	}
	addresses, failure := net.DefaultResolver.LookupNetIP(task, "ip", site.Host)
	if failure != nil {
		dead(failure)
		return Page{}, false
	}
	if len(addresses) == 0 {
		fail(outcome, "dead", "地址无效", "")
		return Page{}, false
	}
	target := "https://" + site.Host + "/"
	tried := map[string]bool{target: true}
	var reply *PageReply
	for hop := 0; hop <= 4; hop++ {
		wanted := target
		var failure error
		reply, target, failure = crawler.fetchEither(task, outcome, target, false)
		tried[wanted], tried[target] = true, true
		if failure != nil {
			dead(failure)
			return Page{}, false
		}
		if !reply.CertEnd.IsZero() {
			outcome.CertEnd = reply.CertEnd
		}
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
		if tried[next] || hostname == "" {
			fail(outcome, "pend", "地址无效", "")
			return Page{}, false
		}
		collector.add(hostname)
		if registrableOf(hostname) != registrable {
			outcome.State, outcome.Links = "dead", collector.items
			return Page{}, false
		}
		if hop < 4 {
			target = next
			continue
		}
		fail(outcome, "pend", "地址无效", "")
		return Page{}, false
	}
	if reply.Status >= 400 {
		fail(outcome, "pend", "应答失败", strconv.Itoa(reply.Status))
		return Page{}, false
	}
	page := ParsePage(reply.Body, target)
	title := strings.ToLower(collapse(page.Name))
	if slices.ContainsFunc([]string{"404", "not found", "页面不存在", "找不到页面", "access denied",
		"welcome to nginx", "index of /", "default web site page", "站点未找到"},
		func(mark string) bool { return strings.Contains(title, mark) }) {
		fail(outcome, "pend", "应答失败", "200")
		return Page{}, false
	}
	outcome.Name, outcome.Author, outcome.Language, outcome.Framework = page.Name, page.Author, page.Language, page.Framework
	outcome.ICP, outcome.Summary = page.ICP, page.Summary
	if outcome.Language == "" {
		outcome.Language = reply.Language
	}
	return page, true
}

func (crawler *Crawler) expand(task context.Context, site *Site, page Page, outcome *Outcome, registrable string, collector *collector) {
	if hubHosts[registrable] {
		return
	}
	guesses := []string{"/links", "/friends", "/link", "/blogroll", "/friend", "/links/", "/link.html"}
	friends := make([]string, 0, len(page.Friends)+len(guesses))
	friends = append(friends, page.Friends...)
	friends = append(friends, pathsOn(site.Host, guesses)...)
	if reply, target := crawler.probe(task, outcome, friends); reply != nil {
		added := 0
		for _, link := range ParsePage(reply.Body, target).Links {
			if added >= 60 {
				break
			}
			neighbor := registrableOf(link)
			if neighbor == registrable || hubHosts[neighbor] {
				continue
			}
			if host := collector.add(link); host != "" {
				outcome.Friends = append(outcome.Friends, host)
				added++
			}
		}
	}
}

func (crawler *Crawler) subscribe(task context.Context, site *Site, page Page, outcome *Outcome) {
	key := page.Framework
	if key == "" {
		key = "default"
	}
	guesses := []string{
		"/atom.xml", "/index.xml", "/feed.xml", "/feed", "/feed/", "/rss.xml",
		"/rss", "/rss/", "/atom", "/index.atom", "/index.rss",
		"/blog/feed", "/blog/atom.xml", "/blog/index.xml", "/posts/index.xml",
		"/feeds/posts/default",
	}
	candidates := make([]string, 0, len(guesses)+3)
	if site.Feed != "" {
		candidates = append(candidates, site.Feed)
	}
	if recalled := crawler.recall("feed:" + key); recalled != "" {
		candidates = append(candidates, "https://"+site.Host+recalled)
	}
	if page.Feed != site.Feed {
		if page.Feed != "" {
			candidates = append(candidates, page.Feed)
		}
		candidates = append(candidates, pathsOn(site.Host, guesses)...)
	}
	reason := ""
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		reply, target, failure := crawler.fetchEither(task, outcome, candidate, true)
		if failure != nil {
			if reason == "" {
				reason = failure.Error()
			}
			continue
		}
		if reply.Status != http.StatusOK {
			if reason == "" {
				reason = strconv.Itoa(reply.Status)
			}
			continue
		}
		subscription := ParseSubscription(reply.Body)
		if !subscription.Parsed {
			continue
		}
		outcome.Feed = target
		crawler.memorize("feed:"+key, target)
		reason = ""
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
	if reason != "" {
		fail(outcome, "alive", "订阅失败", reason)
	}
}

func (crawler *Crawler) Crawl(task context.Context, site *Site, full bool) Outcome {
	outcome := Outcome{State: "alive"}
	collector := &collector{seen: map[string]bool{site.Host: true}}
	registrable := registrableOf(site.Host)
	page, reached := crawler.home(task, site, &outcome, registrable, collector)
	if !reached {
		return outcome
	}
	if full {
		crawler.subscribe(task, site, page, &outcome)
		candidates := pathsOn(site.Host, []string{"/sitemap.xml", "/sitemap_index.xml"})
		if recalled := crawler.recall("sitemap"); recalled != "" {
			candidates = append([]string{"https://" + site.Host + recalled}, candidates...)
		}
		if reply, target := crawler.probe(task, &outcome, candidates); reply != nil {
			for _, link := range ParseSitemap(reply.Body) {
				if hubHosts[registrableOf(link)] {
					continue
				}
				collector.add(link)
			}
			crawler.memorize("sitemap", target)
		}
	}
	crawler.expand(task, site, page, &outcome, registrable, collector)
	outcome.Links = collector.items
	if notBlog(site, page, &outcome) || (page.SinglePost && !page.HasArchive) {
		outcome.State, outcome.Reason = "other", "非博客"
	}
	return outcome
}

type tuner struct {
	guard   sync.Mutex
	worker  int32
	floor   int32
	ceiling int32
}

func (tuner *tuner) tune(count BatchTally) {
	if count.Requests == 0 {
		return
	}
	tuner.guard.Lock()
	defer tuner.guard.Unlock()
	worker := int(atomic.LoadInt32(&tuner.worker))
	switch ratio := float64(count.Timeouts) / float64(count.Requests); {
	case ratio > 0.10:
		worker /= 2
	case ratio < 0.05:
		worker += 4
	}
	atomic.StoreInt32(&tuner.worker, int32(min(max(worker, int(tuner.floor)), int(tuner.ceiling))))
}

type completed struct {
	site    Site
	outcome Outcome
}

func takeWindow(tally *BatchTally, guard *sync.Mutex) BatchTally {
	guard.Lock()
	snapshot := *tally
	*tally = BatchTally{}
	guard.Unlock()
	return snapshot
}

func crawlPipeline(task context.Context, table *BlogTable, crawler *Crawler) error {
	floor := int32(runtime.GOMAXPROCS(0))
	knob := &tuner{worker: floor, floor: floor, ceiling: floor * 4}
	capacity := int(atomic.LoadInt32(&knob.ceiling))
	jobs := make(chan Site, capacity)
	results := make(chan completed, capacity)
	inflight := map[string]bool{}
	var guard sync.Mutex

	var pool sync.WaitGroup
	for index := 0; index < capacity; index++ {
		pool.Add(1)
		go func() {
			defer pool.Done()
			for site := range jobs {
				siteCtx, cancel := context.WithTimeout(task, crawler.Timeout())
				complete := site.Name != "" && site.Feed != "" && site.Language != ""
				outcome := crawler.Crawl(siteCtx, &site, site.State != "alive" || !complete)
				cancel()
				results <- completed{site: site, outcome: outcome}
			}
		}()
	}

	tuneTicker := time.NewTicker(5 * time.Second)
	defer tuneTicker.Stop()

	var cycle BatchTally

	handle := func(done completed) {
		if len(done.outcome.Links) > 0 {
			table.Enqueue(done.outcome.Links, time.Now().UTC())
		}
		if len(done.outcome.Friends) > 0 {
			done.outcome.Friend = table.FriendOrdinals(done.outcome.Friends)
		}
		table.Save(&done.site, done.outcome)
		guard.Lock()
		delete(inflight, done.site.Host)
		cycle.Requests += done.outcome.Requests
		cycle.Timeouts += done.outcome.Timeouts
		guard.Unlock()
		tail := done.outcome.Reason
		if tail == "" {
			tail = gathered(&done.site)
		}
		fmt.Printf("%s [%s] %s %s\n", time.Now().Format(time.RFC3339), done.site.State, done.site.Host, tail)
	}

	wait := func(limit time.Duration) bool {
		var due <-chan time.Time
		if limit > 0 {
			timer := time.NewTimer(limit)
			defer timer.Stop()
			due = timer.C
		}
		select {
		case <-task.Done():
			return false
		case done := <-results:
			handle(done)
		case <-due:
		case <-tuneTicker.C:
			knob.tune(takeWindow(&cycle, &guard))
		}
		return true
	}

	for running := true; running && task.Err() == nil; {
		guard.Lock()
		pending := len(inflight)
		busy := slices.Collect(maps.Keys(inflight))
		guard.Unlock()

		if want := int(atomic.LoadInt32(&knob.worker)) - pending; want > 0 {
			batch, failure := table.Dequeue(time.Now().UTC(), want, busy)
			if failure != nil {
				fmt.Fprintf(os.Stderr, "取站失败：%v\n", failure)
				break
			}
			if len(batch) == 0 {
				running = wait(table.NextDue(time.Now().UTC()))
				continue
			}
			for _, site := range batch {
				guard.Lock()
				inflight[site.Host] = true
				guard.Unlock()
				jobs <- site
			}
			continue
		}
		running = wait(0)
	}

	close(jobs)
	drained := make(chan struct{})
	go func() {
		for range results {
		}
		close(drained)
	}()
	pool.Wait()
	close(results)
	<-drained
	return nil
}

func gathered(site *Site) string {
	parts := make([]string, 0, 9)
	for _, item := range [][2]string{
		{"feed", site.Feed}, {"lang", site.Language}, {"name", site.Name},
		{"author", site.Author}, {"icp", site.ICP}, {"summary", site.Summary},
		{"tag", site.Tags}, {"friend", site.Friend}, {"framework", site.Framework},
	} {
		if item[1] != "" {
			parts = append(parts, item[0]+"="+item[1])
		}
	}
	return strings.Join(parts, " ")
}
