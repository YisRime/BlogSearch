package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type PageReply struct {
	Status   int
	Body     []byte
	Location string
}

type Outcome struct {
	State     string
	Local     bool
	Reason    string
	Links     []string
	Friends   []string
	Friend    string
	Requests  int
	Timeouts  int
	Bytes     int
	Latency   time.Duration
	Cert      bool
	Name      string
	Author    string
	Feed      string
	Language  string
	Framework string
}

type BatchTally struct {
	Sites    int
	Requests int
	Bytes    int
	Expanded int
	Timeouts int
	Latency  time.Duration
}

func (tally *BatchTally) add(outcome Outcome, inserted int) {
	tally.Sites++
	tally.Requests += outcome.Requests
	tally.Bytes += outcome.Bytes
	tally.Expanded += inserted
	tally.Timeouts += outcome.Timeouts
	tally.Latency += outcome.Latency
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
	if latency <= 0 {
		return 10 * time.Second
	}
	timeout := time.Duration(4 * latency * float64(time.Second))
	if timeout < 10*time.Second {
		return 10 * time.Second
	}
	if timeout > 60*time.Second {
		return 60 * time.Second
	}
	return timeout
}

func (crawler *Crawler) observe(elapsed time.Duration) {
	sample := elapsed.Seconds()
	crawler.guard.Lock()
	crawler.latency += (sample - crawler.latency) * 0.1
	crawler.guard.Unlock()
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

func timedOut(failure error) bool {
	if failure == nil {
		return false
	}
	if errors.Is(failure, context.DeadlineExceeded) {
		return true
	}
	text := failure.Error()
	return strings.Contains(text, "i/o timeout") ||
		strings.Contains(text, "handshake timeout") ||
		strings.Contains(text, "Client.Timeout") ||
		strings.Contains(text, "context deadline exceeded")
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
	request.Header.Set("User-Agent", "blogsearch")
	request.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	response, failure := client.Do(request)
	if failure != nil {
		return nil, failure
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	return &PageReply{Status: response.StatusCode, Body: body,
		Location: strings.TrimSpace(response.Header.Get("Location"))}, nil
}

func (crawler *Crawler) attempt(task context.Context, outcome *Outcome, target string, follow, relaxed bool) (*PageReply, error) {
	started := time.Now()
	reply, failure := crawler.fetch(task, target, follow, relaxed)
	elapsed := time.Since(started)
	outcome.Requests++
	outcome.Latency += elapsed
	if failure == nil {
		crawler.observe(elapsed)
	} else if timedOut(failure) {
		outcome.Timeouts++
	}
	return reply, failure
}

func plain(target string) string {
	if !strings.HasPrefix(target, "https://") {
		return ""
	}
	return "http://" + strings.TrimPrefix(target, "https://")
}

func idlePort(failure error) bool {
	text := failure.Error()
	return strings.Contains(text, "actively refused") || strings.HasSuffix(text, "EOF")
}

func brokenCert(failure error) bool {
	return errors.As(failure, new(*tls.CertificateVerificationError))
}

func (crawler *Crawler) fetchEither(task context.Context, outcome *Outcome, target string, follow bool) (*PageReply, string, error) {
	reply, failure := crawler.attempt(task, outcome, target, follow, false)
	if failure == nil {
		return reply, target, nil
	}
	if brokenCert(failure) {
		outcome.Cert = true
		reply, failure = crawler.attempt(task, outcome, target, follow, true)
		if failure == nil {
			return reply, target, nil
		}
	}
	fallback := plain(target)
	if fallback == "" || !idlePort(failure) {
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
		outcome.Bytes += len(reply.Body)
		return reply, target
	}
	return nil, ""
}

func authoritative(failure error) bool {
	record := &net.DNSError{}
	if !errors.As(failure, &record) || record.IsTemporary {
		return false
	}
	return record.IsNotFound || strings.Contains(record.Err, "no data")
}

func fail(outcome *Outcome, state, reason, payload string) {
	outcome.State = state
	if payload == "" {
		outcome.Reason = reason
		return
	}
	outcome.Reason = reason + "：" + payload
}

var feedPaths = []string{"/atom.xml", "/index.xml", "/feed.xml", "/feed", "/rss.xml"}

var sitemapPaths = []string{"/sitemap.xml", "/sitemap_index.xml"}

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
	addresses, failure := net.DefaultResolver.LookupNetIP(task, "ip", site.Host)
	if failure != nil {
		fail(outcome, "pend", "抓取失败", failure.Error())
		if authoritative(failure) {
			outcome.State, outcome.Reason = "dead", "地址无效"
		} else {
			outcome.Local = errors.As(failure, new(*net.DNSError))
		}
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
			fail(outcome, "pend", "抓取失败", failure.Error())
			if authoritative(failure) {
				outcome.State, outcome.Reason = "dead", "地址无效"
			} else {
				outcome.Local = errors.As(failure, new(*net.DNSError))
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
		if tried[next] || hostname == "" {
			fail(outcome, "pend", "地址无效", "")
			return Page{}, false
		}
		collector.add(hostname)
		if registrableOf(hostname) != registrable {
			outcome.State, outcome.Links = "alive", collector.items
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
	if softMissing(page.Name) {
		fail(outcome, "pend", "应答失败", "200")
		return Page{}, false
	}
	for _, link := range page.Links {
		collector.add(link)
	}
	outcome.Name, outcome.Author, outcome.Language, outcome.Framework = page.Name, page.Author, page.Language, page.Framework
	return page, true
}

func (crawler *Crawler) expand(task context.Context, site *Site, page Page, outcome *Outcome, registrable string, collector *collector) {
	friends := make([]string, 0, len(page.Friends)+len(friendPaths))
	friends = append(friends, page.Friends...)
	friends = append(friends, pathsOn(site.Host, friendPaths)...)
	if reply, target := crawler.probe(task, outcome, friends); reply != nil {
		for _, link := range ParsePage(reply.Body, target).Links {
			if registrableOf(link) != registrable {
				collector.add(link)
				outcome.Friends = append(outcome.Friends, link)
			}
		}
	}
}

func (crawler *Crawler) subscribe(task context.Context, site *Site, page Page, outcome *Outcome) {
	key := page.Framework
	if key == "" {
		key = "default"
	}
	candidates := make([]string, 0, len(feedPaths)+3)
	if site.Feed != "" {
		candidates = append(candidates, site.Feed)
	}
	if recalled := crawler.recall("feed:" + key); recalled != "" {
		candidates = append(candidates, "https://"+site.Host+recalled)
	}
	switch {
	case page.Feed == "":
		candidates = append(candidates, pathsOn(site.Host, feedPaths)...)
	case page.Feed != site.Feed:
		candidates = append(candidates, page.Feed)
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
			if page.Feed != "" {
				break
			}
			continue
		}
		outcome.Bytes += len(reply.Body)
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
		candidates := pathsOn(site.Host, sitemapPaths)
		if recalled := crawler.recall("sitemap"); recalled != "" {
			candidates = append([]string{"https://" + site.Host + recalled}, candidates...)
		}
		if reply, target := crawler.probe(task, &outcome, candidates); reply != nil {
			for _, link := range ParseSitemap(reply.Body) {
				collector.add(link)
			}
			crawler.memorize("sitemap", target)
		}
	}
	crawler.expand(task, site, page, &outcome, registrable, collector)
	outcome.Links = collector.items
	return outcome
}

type tuner struct {
	guard   sync.Mutex
	worker  int32
	floor   int32
	ceiling int32
}

func newTuner() *tuner {
	floor := int32(runtime.GOMAXPROCS(0))
	return &tuner{worker: floor, floor: floor, ceiling: floor * 4}
}

func (tuner *tuner) workers() int { return int(atomic.LoadInt32(&tuner.worker)) }

func (tuner *tuner) cap() int { return int(atomic.LoadInt32(&tuner.ceiling)) }

func (tuner *tuner) tune(count BatchTally) {
	if count.Requests == 0 {
		return
	}
	tuner.guard.Lock()
	defer tuner.guard.Unlock()
	worker := int(atomic.LoadInt32(&tuner.worker))
	floor, ceiling := int(tuner.floor), int(tuner.ceiling)
	ratio := float64(count.Timeouts) / float64(count.Requests)
	switch {
	case ratio > 0.10:
		worker /= 2
	case ratio < 0.05:
		worker += 4
	}
	if worker < floor {
		worker = floor
	}
	if worker > ceiling {
		worker = ceiling
	}
	atomic.StoreInt32(&tuner.worker, int32(worker))
}

type completed struct {
	site     Site
	outcome  Outcome
	inserted int
}

func takeWindow(tally *BatchTally, guard *sync.Mutex) BatchTally {
	guard.Lock()
	snapshot := *tally
	*tally = BatchTally{}
	guard.Unlock()
	return snapshot
}

func crawlPipeline(task context.Context, table *BlogTable, crawler *Crawler) error {
	knob := newTuner()
	capacity := knob.cap()
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
				outcome := crawler.Crawl(siteCtx, &site, site.State != "alive")
				cancel()
				results <- completed{site: site, outcome: outcome}
			}
		}()
	}

	tuneTicker := time.NewTicker(5 * time.Second)
	defer tuneTicker.Stop()
	reportTicker := time.NewTicker(5 * time.Minute)
	defer reportTicker.Stop()

	var window, cycle BatchTally
	mark, round := time.Now(), 1

	handle := func(done completed) {
		if done.outcome.State == "alive" {
			done.inserted = table.Enqueue(done.outcome.Links, time.Now().UTC())
			if len(done.outcome.Friends) > 0 {
				done.outcome.Friend = table.FriendOrdinals(done.outcome.Friends)
			}
		}
		table.Save(&done.site, done.outcome)
		guard.Lock()
		delete(inflight, done.site.Host)
		window.add(done.outcome, done.inserted)
		cycle.add(done.outcome, done.inserted)
		guard.Unlock()
		at := time.Now().Format(time.RFC3339)
		if done.outcome.Reason != "" {
			fmt.Printf("%s [%s] %s %s\n", at, done.site.State, done.site.Host, done.outcome.Reason)
		} else {
			fmt.Printf("%s [%s] %s %s\n", at, done.site.State, done.site.Host, gathered(&done.site))
		}
	}

	running := true
	for running && task.Err() == nil {
		guard.Lock()
		pending := len(inflight)
		busy := make([]string, 0, pending)
		for host := range inflight {
			busy = append(busy, host)
		}
		guard.Unlock()

		if want := knob.workers() - pending; want > 0 {
			batch, failure := table.Dequeue(time.Now().UTC(), want, busy)
			if failure != nil {
				fmt.Fprintf(os.Stderr, "取站失败：%v\n", failure)
				break
			}
			if len(batch) == 0 {
				timer := time.NewTimer(table.NextDue(time.Now().UTC()))
				select {
				case <-task.Done():
					timer.Stop()
					running = false
				case <-timer.C:
				case done := <-results:
					timer.Stop()
					handle(done)
				case <-tuneTicker.C:
					timer.Stop()
					knob.tune(takeWindow(&cycle, &guard))
				case <-reportTicker.C:
					timer.Stop()
					report(knob, &window, &guard, round, time.Since(mark))
					mark, round = time.Now(), round+1
				}
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
		select {
		case <-task.Done():
			running = false
		case done := <-results:
			handle(done)
		case <-tuneTicker.C:
			knob.tune(takeWindow(&cycle, &guard))
		case <-reportTicker.C:
			report(knob, &window, &guard, round, time.Since(mark))
			mark, round = time.Now(), round+1
		}
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

func report(knob *tuner, tally *BatchTally, guard *sync.Mutex, round int, elapsed time.Duration) {
	count := takeWindow(tally, guard)
	if count.Sites == 0 {
		return
	}
	mean := int64(0)
	if count.Requests > 0 {
		mean = (count.Latency / time.Duration(count.Requests)).Milliseconds()
	}
	fmt.Printf("第 %d 轮：新增 %d 站（%d 线程，共请求 %d 次 / %d Byte，平均 %.1f 站/秒 / 时延 %d MS）\n",
		round, count.Expanded, knob.workers(), count.Requests, count.Bytes,
		float64(count.Sites)/elapsed.Seconds(), mean)
}

func gathered(site *Site) string {
	items := [][2]string{{"名称", site.Name}, {"作者", site.Author},
		{"语言", site.Language}, {"框架", site.Framework}, {"订阅", site.Feed}}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if item[1] != "" {
			parts = append(parts, item[0]+item[1])
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "、" + strings.Join(parts, "、")
}
