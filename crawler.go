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
	Latency  time.Duration
}

func (tally *BatchTally) add(outcome Outcome, inserted int) {
	tally.Sites++
	tally.Requests += outcome.Requests
	tally.Bytes += outcome.Bytes
	tally.Expanded += inserted
	tally.Latency += outcome.Latency
}

type Crawler struct {
	client      *http.Client
	follower    *http.Client
	loose       *http.Client
	looseFollow *http.Client
}

func newTransport(relaxed bool) *http.Transport {
	return &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 5 * time.Second,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS11, InsecureSkipVerify: relaxed},
		MaxIdleConns:        256,
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     30 * time.Second,
		ForceAttemptHTTP2:   true,
	}
}

func NewCrawler() *Crawler {
	strict, relaxed := newTransport(false), newTransport(true)
	hold := func(transport *http.Transport) *http.Client {
		return &http.Client{Transport: transport, Timeout: 30 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	}
	pass := func(transport *http.Transport) *http.Client {
		return &http.Client{Transport: transport, Timeout: 30 * time.Second}
	}
	return &Crawler{client: hold(strict), follower: pass(strict), loose: hold(relaxed), looseFollow: pass(relaxed)}
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
	outcome.Requests++
	outcome.Latency += time.Since(started)
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
	for hop := 0; hop <= 2; hop++ {
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
		if hop < 2 {
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

func (crawler *Crawler) Crawl(task context.Context, site *Site, full bool) Outcome {
	outcome := Outcome{State: "alive"}
	collector := &collector{seen: map[string]bool{site.Host: true}}
	registrable := registrableOf(site.Host)
	page, reached := crawler.home(task, site, &outcome, registrable, collector)
	if !reached {
		return outcome
	}
	if full {
		candidates := []string{site.Feed}
		switch {
		case page.Feed == "":
			candidates = append(candidates, pathsOn(site.Host, feedPaths)...)
		case page.Feed != site.Feed:
			candidates = append(candidates, page.Feed)
		}
		for _, candidate := range candidates {
			if candidate == "" {
				continue
			}
			reply, target, failure := crawler.fetchEither(task, &outcome, candidate, true)
			if failure != nil {
				fail(&outcome, "alive", "订阅失败", failure.Error())
				continue
			}
			if reply.Status != http.StatusOK {
				if page.Feed != "" {
					fail(&outcome, "alive", "订阅失败", strconv.Itoa(reply.Status))
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

type tuner struct {
	guard   sync.Mutex
	worker  int32
	rate    float64
	latency time.Duration
}

func newTuner() *tuner {
	return &tuner{worker: int32(runtime.GOMAXPROCS(0))}
}

func (tuner *tuner) batch() int { return int(atomic.LoadInt32(&tuner.worker)) }

func (tuner *tuner) workers() int { return int(atomic.LoadInt32(&tuner.worker)) }

func (tuner *tuner) tune(count BatchTally, elapsed time.Duration) {
	if count.Requests == 0 || elapsed <= 0 {
		return
	}
	tuner.guard.Lock()
	defer tuner.guard.Unlock()
	worker := int(atomic.LoadInt32(&tuner.worker))
	if worker < 1 {
		worker = 1
	}
	rate := float64(count.Sites) / elapsed.Seconds()
	mean := count.Latency / time.Duration(count.Requests)
	if tuner.rate > 0 {
		better := rate > tuner.rate*105/100
		worse := mean > tuner.latency*120/100
		switch {
		case better && !worse:
			worker++
		case worse:
			worker /= 2
			if worker < 1 {
				worker = 1
			}
		}
	}
	atomic.StoreInt32(&tuner.worker, int32(worker))
	tuner.rate, tuner.latency = rate, mean
}

func crawlPipeline(task context.Context, table *BlogTable, crawler *Crawler) error {
	knob := newTuner()
	jobs := make(chan Site, runtime.GOMAXPROCS(0))
	live := int32(0)
	var group sync.WaitGroup
	var guard sync.Mutex
	var window BatchTally
	var cycle BatchTally
	cycleMark := time.Now()
	inflight := map[string]bool{}

	var consume func()
	var fill func()

	group.Add(1)
	go func() {
		defer group.Done()
		defer close(jobs)
		for task.Err() == nil {
			guard.Lock()
			busy := make([]string, 0, len(inflight))
			for host := range inflight {
				busy = append(busy, host)
			}
			guard.Unlock()
			batch, failure := table.Dequeue(time.Now().UTC(), knob.batch(), busy)
			if failure != nil {
				fmt.Fprintf(os.Stderr, "取到期站点失败：%v\n", failure)
				return
			}
			if len(batch) == 0 {
				hold(task, table.NextDue(time.Now().UTC()))
				continue
			}
			for _, site := range batch {
				guard.Lock()
				inflight[site.Host] = true
				guard.Unlock()
				select {
				case jobs <- site:
				case <-task.Done():
					return
				}
			}
		}
	}()

	consume = func() {
		defer group.Done()
		defer atomic.AddInt32(&live, -1)
		for site := range jobs {
			if task.Err() != nil {
				return
			}
			outcome := crawler.Crawl(task, &site, site.State != "alive")
			inserted := 0
			if outcome.State == "alive" {
				inserted = table.Enqueue(outcome.Links, time.Now().UTC())
				if len(outcome.Friends) > 0 {
					outcome.Friend = table.FriendOrdinals(outcome.Friends)
				}
			}
			table.Save(&site, outcome)
			guard.Lock()
			delete(inflight, site.Host)
			window.add(outcome, inserted)
			cycle.add(outcome, inserted)
			ready := cycle.Sites >= knob.workers()
			snapshot, elapsed := BatchTally{}, time.Duration(0)
			if ready {
				snapshot, elapsed = cycle, time.Since(cycleMark)
				cycle, cycleMark = BatchTally{}, time.Now()
			}
			guard.Unlock()
			at := time.Now().Format(time.RFC3339)
			if outcome.Reason != "" {
				fmt.Printf("%s [%s] %s %s\n", at, site.State, site.Host, outcome.Reason)
			} else {
				fmt.Printf("%s [%s] %s %s\n", at, site.State, site.Host, gathered(&site))
			}
			if ready {
				knob.tune(snapshot, elapsed)
				fill()
			}
			if int(atomic.LoadInt32(&live)) > knob.workers() {
				return
			}
		}
	}

	fill = func() {
		for int(atomic.LoadInt32(&live)) < knob.workers() {
			atomic.AddInt32(&live, 1)
			group.Add(1)
			go consume()
		}
	}

	group.Add(1)
	go func() {
		defer group.Done()
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		mark, round := time.Now(), 1
		for {
			select {
			case <-task.Done():
				report(knob, &window, &guard, round, time.Since(mark))
				return
			case now := <-ticker.C:
				report(knob, &window, &guard, round, now.Sub(mark))
				mark, round = now, round+1
				fill()
			}
		}
	}()

	fill()
	group.Wait()
	return nil
}

func report(knob *tuner, tally *BatchTally, guard *sync.Mutex, round int, elapsed time.Duration) {
	guard.Lock()
	count := *tally
	*tally = BatchTally{}
	guard.Unlock()
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

func hold(task context.Context, delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-task.Done():
	case <-timer.C:
	}
}
