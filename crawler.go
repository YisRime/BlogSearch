package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html/charset"
)

// 订阅探测路径
var feedPaths = []string{
	"/atom.xml", "/index.xml", "/feed.xml", "/feed", "/rss.xml",
	"/rss", "/atom", "/index.atom", "/index.rss", "/blog/feed",
}

// 友链探测路径
var friendPaths = []string{
	"/links", "/friends", "/link", "/friend", "/blogroll",
	"/links.html", "/friends.html", "/friendlinks",
}

// 跳转元标记
var metaJump = regexp.MustCompile(`(?is)<meta[^>]+content\s*=\s*["']?\s*\d+\s*;\s*url\s*=\s*([^"'>\s]+)`)

// 跳转脚本标记
var scriptJump = regexp.MustCompile(`(?is)location\s*\.\s*(?:href\s*=\s*|replace\s*\(\s*|assign\s*\(\s*)["']([^"']+)["']`)

// 解析绝对地址
func absoluteURL(raw, base string) string {
	raw = strings.Trim(strings.TrimSpace(raw), `"'`)
	if raw == "" {
		return ""
	}
	root, failure := url.Parse(base)
	if failure != nil {
		return ""
	}
	parsed, failure := url.Parse(raw)
	if failure != nil {
		return ""
	}
	if !parsed.IsAbs() {
		parsed = root.ResolveReference(parsed)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	return parsed.String()
}

// 请求响应信息
type Reply struct {
	Status    int
	Body      []byte
	Location  string
	Language  string
	CertEnd   time.Time
	Server    string
	Mitigated string
	Refresh   string
}

// 爬取结果数据
type Result struct {
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

// 运行统计项
type Tally struct {
	Requests int
	Timeouts int
}

// 并发爬取器
type Scout struct {
	client      *http.Client
	follower    *http.Client
	loose       *http.Client
	looseFollow *http.Client

	guard   sync.Mutex
	latency float64
	learned map[string]string
}

// 主机去重集合
type hostSet struct {
	seen  map[string]bool
	items []string
}

// 调整并发数量
type governor struct {
	guard   sync.Mutex
	worker  int32
	floor   int32
	ceiling int32
}

// 完成任务项
type finished struct {
	site   BlogSite
	result Result
}

// 构造传输配置
func dialer(relaxed bool, maxConns, perHost int) *http.Transport {
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

// 创建爬取器
func newScout() *Scout {
	floor := runtime.GOMAXPROCS(0)
	ceiling := floor * 4
	strict, relaxed := dialer(false, ceiling, floor), dialer(true, ceiling, floor)
	jar, _ := cookiejar.New(nil)
	build := func(transport *http.Transport, redirect bool) *http.Client {
		client := &http.Client{Transport: transport, Timeout: 60 * time.Second, Jar: jar}
		if redirect {
			client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
		}
		return client
	}
	return &Scout{
		client: build(strict, true), follower: build(strict, false),
		loose: build(relaxed, true), looseFollow: build(relaxed, false),
		latency: 4, learned: map[string]string{},
	}
}

// 获取超时期限
func (scout *Scout) Timeout() time.Duration {
	scout.guard.Lock()
	latency := scout.latency
	scout.guard.Unlock()
	return min(max(time.Duration(4*latency*float64(time.Second)), 10*time.Second), 60*time.Second)
}

// 读取学习缓存
func (scout *Scout) lookup(key string) string {
	scout.guard.Lock()
	defer scout.guard.Unlock()
	return scout.learned[key]
}

// 写入学习缓存
func (scout *Scout) remember(key, target string) {
	parsed, failure := url.Parse(target)
	if failure != nil || parsed.Path == "" {
		return
	}
	scout.guard.Lock()
	scout.learned[key] = parsed.Path
	scout.guard.Unlock()
}

// 发送单次请求
func (scout *Scout) fetch(task context.Context, target string, follow, relaxed bool) (*Reply, error) {
	client := scout.client
	switch {
	case relaxed && follow:
		client = scout.looseFollow
	case relaxed:
		client = scout.loose
	case follow:
		client = scout.follower
	}
	request, failure := http.NewRequestWithContext(task, http.MethodGet, target, nil)
	if failure != nil {
		return nil, failure
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	request.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	request.Header.Set("Cache-Control", "no-cache")
	request.Header.Set("Upgrade-Insecure-Requests", "1")
	request.Header.Set("Sec-Fetch-Dest", "document")
	request.Header.Set("Sec-Fetch-Mode", "navigate")
	request.Header.Set("Sec-Fetch-Site", "none")
	request.Header.Set("Sec-Fetch-User", "?1")
	response, failure := client.Do(request)
	if failure != nil {
		return nil, failure
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if !utf8.Valid(body) {
		if reader, failure := charset.NewReader(bytes.NewReader(body), response.Header.Get("Content-Type")); failure == nil {
			if text, failure := io.ReadAll(reader); failure == nil && len(text) > 0 {
				body = text
			}
		}
	}
	reply := &Reply{Status: response.StatusCode, Body: body,
		Location: strings.TrimSpace(response.Header.Get("Location")),
		Language: strings.TrimSpace(response.Header.Get("Content-Language")),
		Server:   response.Header.Get("Server"),
		Refresh:  response.Header.Get("Refresh"),
	}
	if mitigated := response.Header.Get("Cf-Mitigated"); mitigated != "" {
		reply.Mitigated = mitigated
	}
	if response.TLS != nil && len(response.TLS.PeerCertificates) > 0 {
		reply.CertEnd = response.TLS.PeerCertificates[0].NotAfter
	}
	return reply, nil
}

// 请求页面内容
func (scout *Scout) tryPage(task context.Context, result *Result, target string, follow, relaxed bool) (*Reply, error) {
	started := time.Now()
	reply, failure := scout.fetch(task, target, follow, relaxed)
	elapsed := time.Since(started)
	result.Requests++
	if failure == nil {
		scout.guard.Lock()
		scout.latency += (elapsed.Seconds() - scout.latency) * 0.1
		scout.guard.Unlock()
		return reply, nil
	}
	if isTimeout(failure) {
		result.Timeouts++
	}
	return reply, failure
}

// 直连失败回退
func (scout *Scout) tryEither(task context.Context, result *Result, target string, follow bool) (*Reply, string, error) {
	reply, failure := scout.tryPage(task, result, target, follow, false)
	if failure == nil {
		return reply, target, nil
	}
	var certFailure *tls.CertificateVerificationError
	if errors.As(failure, &certFailure) {
		result.Cert = true
		if reply, failure = scout.tryPage(task, result, target, follow, true); failure == nil {
			return reply, target, nil
		}
	}
	fallback := ""
	if strings.HasPrefix(target, "https://") {
		fallback = "http://" + strings.TrimPrefix(target, "https://")
	}
	refused := errors.Is(failure, syscall.ECONNREFUSED)
	closed := errors.Is(failure, io.EOF) || errors.Is(failure, io.ErrUnexpectedEOF)
	broken := strings.Contains(failure.Error(), "tls:")
	if fallback == "" || (!refused && !closed && !broken) {
		return nil, target, failure
	}
	reply, failure = scout.tryPage(task, result, fallback, follow, false)
	return reply, fallback, failure
}

// 追踪网页跳转
func (scout *Scout) hop(task context.Context, result *Result, start string, deep bool, guard func(string) bool) (*Reply, string, error) {
	target := start
	tried := map[string]bool{start: true}
	for hop := 0; hop <= 3; hop++ {
		reply, final, failure := scout.tryEither(task, result, target, true)
		if failure != nil {
			return nil, final, failure
		}
		tried[target], tried[final] = true, true
		if !reply.CertEnd.IsZero() {
			result.CertEnd = reply.CertEnd
		}
		next := ""
		switch {
		case redirectStatus(reply.Status):
			next = absoluteURL(reply.Location, final)
		case deep:
			raw := ""
			fromBody := false
			switch {
			case strings.Contains(strings.ToLower(reply.Refresh), "url="):
				index := strings.Index(strings.ToLower(reply.Refresh), "url=")
				raw = reply.Refresh[index+4:]
			case len(reply.Body) < 4096:
				fromBody = true
				// 页面跳转信息
				if match := metaJump.FindSubmatch(reply.Body); match != nil {
					raw = string(match[1])
					// 脚本跳转信息
				} else if match := scriptJump.FindSubmatch(reply.Body); match != nil {
					raw = string(match[1])
				}
			}
			next = absoluteURL(raw, final)
			if next != "" && fromBody && registrableOf(normalizeHost(next)) != registrableOf(normalizeHost(final)) {
				next = ""
			}
		}
		if next == "" || tried[next] || hop == 3 {
			return reply, final, nil
		}
		if guard != nil && guard(next) {
			return nil, final, nil
		}
		target = next
	}
	return nil, target, nil
}

// 选取首个可用
func (scout *Scout) pick(task context.Context, result *Result, candidates []string, accept func(*Reply, string) bool) (*Reply, string, string) {
	reason := ""
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		reply, target, failure := scout.hop(task, result, candidate, false, nil)
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
		if accept == nil || accept(reply, target) {
			return reply, target, ""
		}
	}
	return nil, "", reason
}

// 记录结果状态
func mark(result *Result, state, reason, payload string) {
	result.State = state
	if payload == "" {
		result.Reason = reason
		return
	}
	result.Reason = reason + "：" + payload
}

// 拼接候选地址
func siteURLs(host string, guesses []string) []string {
	addresses := make([]string, 0, len(guesses))
	for _, guess := range guesses {
		addresses = append(addresses, "https://"+host+guess)
	}
	return addresses
}

// 收集去重主机
func (set *hostSet) keep(raw string) string {
	hostname := normalizeHost(raw)
	if hostname == "" || set.seen[hostname] {
		return ""
	}
	set.seen[hostname] = true
	set.items = append(set.items, hostname)
	return hostname
}

// 判断失效重试
func deadOrRetry(result *Result, failure error) {
	var record *net.DNSError
	if errors.As(failure, &record) && !record.IsTemporary &&
		(record.IsNotFound || strings.Contains(record.Err, "no data")) {
		result.State, result.Reason = "dead", "地址无效"
		return
	}
	mark(result, "pend", "抓取失败", failure.Error())
}

// 追踪网页跳转
func (scout *Scout) walkHops(task context.Context, site *BlogSite, result *Result, registrable string, hosts *hostSet) (*Reply, string, bool) {
	guard := func(next string) bool {
		hostname := normalizeHost(next)
		switch {
		case hostname == "":
			mark(result, "pend", "地址无效", "")
			return true
		case inDomainSet(hostname, blockHosts):
			mark(result, "dead", "其它跳转", "")
			return true
		}
		if registrableOf(hostname) != registrable {
			result.State, result.Links = "dead", hosts.items
			return true
		}
		hosts.keep(hostname)
		return false
	}
	reply, target, failure := scout.hop(task, result, "https://"+site.Host+"/", true, guard)
	switch {
	case failure != nil:
		deadOrRetry(result, failure)
		return nil, "", false
	case reply == nil:
		return nil, "", false
	}
	return reply, target, true
}

// 抓取首页内容
func (scout *Scout) home(task context.Context, site *BlogSite, result *Result, registrable string, hosts *hostSet) (Home, bool) {
	addresses, failure := net.DefaultResolver.LookupNetIP(task, "ip", site.Host)
	if failure != nil {
		deadOrRetry(result, failure)
		return Home{}, false
	}
	if len(addresses) == 0 {
		mark(result, "dead", "地址无效", "")
		return Home{}, false
	}
	reply, target, ok := scout.walkHops(task, site, result, registrable, hosts)
	if !ok {
		return Home{}, false
	}
	kind := ""
	switch {
	case strings.Contains(strings.ToLower(reply.Mitigated), "challenge"):
		kind = "挑战"
	case reply.Status == 444:
		kind = "丢弃"
	default:
		head := strings.ToLower(string(reply.Body[:min(len(reply.Body), 8192)]))
		shielded := strings.Contains(strings.ToLower(reply.Server), "cloudflare")
		switch {
		case containsAny(head, []string{"just a moment", "cf-challenge", "challenge-platform", "cf_chl_", "checking your browser", "enable javascript and cookies", "please wait while we check"}):
			kind = "挑战"
		case containsAny(head, []string{"attention required", "ddos protection", "under attack"}) ||
			shielded && reply.Status == http.StatusForbidden:
			kind = "拦截"
		case shielded && reply.Status == http.StatusServiceUnavailable:
			kind = "限流"
		}
	}
	if kind != "" {
		mark(result, "pend", "被盾拦截", kind)
		return Home{}, false
	}
	if redirectStatus(reply.Status) {
		mark(result, "pend", "跳转过多", "")
		return Home{}, false
	}
	if reply.Status >= 400 {
		mark(result, "pend", "应答失败", strconv.Itoa(reply.Status))
		return Home{}, false
	}
	page := readPage(reply.Body, target)
	if state, reason := classify(site, page, result, false); reason != "" {
		mark(result, state, reason, "")
		return Home{}, false
	}
	result.Name, result.Author, result.Language, result.Framework = page.Name, page.Author, page.Language, page.Framework
	result.ICP, result.Summary = page.ICP, page.Summary
	if result.Language == "" {
		result.Language = reply.Language
	}
	return page, true
}

// 抓取订阅源
func (scout *Scout) subscribe(task context.Context, site *BlogSite, page Home, result *Result) {
	key := page.Framework
	if key == "" {
		key = "default"
	}
	candidates := make([]string, 0, len(feedPaths)+3)
	if site.Feed != "" {
		candidates = append(candidates, site.Feed)
	}
	if recalled := scout.lookup("feed:" + key); recalled != "" {
		candidates = append(candidates, "https://"+site.Host+recalled)
	}
	if site.Feed == "" || page.Feed != site.Feed {
		if page.Feed != "" {
			candidates = append(candidates, page.Feed)
		}
		candidates = append(candidates, siteURLs(site.Host, feedPaths)...)
	}
	var subscription Feed
	reply, target, reason := scout.pick(task, result, candidates, func(reply *Reply, _ string) bool {
		subscription = ParseSubscription(reply.Body)
		return subscription.Parsed
	})
	if reply != nil {
		result.Feed = target
		scout.remember("feed:"+key, target)
		if subscription.Name != "" && !genericName(subscription.Name) {
			result.Name = subscription.Name
		}
		if len(subscription.Authors) == 1 {
			result.Author = subscription.Authors[0]
		}
		if result.Language == "" && subscription.Language != "" {
			result.Language = subscription.Language
		}
		if result.Summary == "" && subscription.Summary != "" {
			result.Summary = subscription.Summary
		}
		if result.Framework == "" && subscription.Generator != "" {
			result.Framework = detectFramework(subscription.Generator, nil)
		}
		return
	}
	if reason != "" {
		mark(result, "alive", "订阅失败", reason)
	}
}

// 寻找友链页面
func (scout *Scout) probeSitemap(task context.Context, site *BlogSite, result *Result, hosts *hostSet) []string {
	candidates := siteURLs(site.Host, []string{"/sitemap.xml", "/sitemap_index.xml", "/sitemap-index.xml", "/sitemap/index.xml"})
	if recalled := scout.lookup("sitemap"); recalled != "" {
		candidates = append([]string{"https://" + site.Host + recalled}, candidates...)
	}
	reply, target, _ := scout.pick(task, result, candidates, nil)
	if reply == nil {
		return nil
	}
	var document struct {
		URLs []struct {
			Loc string `xml:"loc"`
		} `xml:"url"`
		Sitemaps []struct {
			Loc string `xml:"loc"`
		} `xml:"sitemap"`
	}
	if xml.Unmarshal(reply.Body, &document) != nil {
		return nil
	}
	var seeds []string
	registrable := registrableOf(site.Host)
	for _, entry := range append(document.URLs, document.Sitemaps...) {
		link := entry.Loc
		if link == "" || registrableOf(normalizeHost(link)) != registrable {
			continue
		}
		hosts.keep(link)
		if containsAny(strings.ToLower(link), friendMarks) {
			seeds = append(seeds, link)
		}
	}
	scout.remember("sitemap", target)
	return seeds
}

// 抓取友链页面
func (scout *Scout) expand(task context.Context, site *BlogSite, page Home, result *Result, registrable string, hosts *hostSet, seeds []string) {
	if inDomainSet(site.Host, blockHosts) {
		return
	}
	// 博客信号判定
	if result.Feed == "" && site.Feed == "" && result.Framework == "" && !page.HasArchive {
		return
	}
	key := page.Framework
	if key == "" {
		key = "default"
	}
	candidates := make([]string, 0, len(page.Friends)+len(seeds)+len(friendPaths)+1)
	if recalled := scout.lookup("friend:" + key); recalled != "" {
		candidates = append(candidates, "https://"+site.Host+recalled)
	}
	candidates = append(candidates, seeds...)
	candidates = append(candidates, page.Friends...)
	candidates = append(candidates, siteURLs(site.Host, friendPaths)...)
	visited := map[string]bool{}
	for len(candidates) > 0 {
		candidate := candidates[0]
		candidates = candidates[1:]
		if candidate == "" || visited[candidate] {
			continue
		}
		visited[candidate] = true
		reply, target, _ := scout.pick(task, result, []string{candidate}, nil)
		if reply == nil {
			continue
		}
		if scout.harvestFriends(result, registrable, hosts, reply.Body, target) > 0 {
			scout.remember("friend:"+key, target)
			return
		}
	}
}

// 收集友链主机
func (scout *Scout) harvestFriends(result *Result, registrable string, hosts *hostSet, body []byte, target string) int {
	added := 0
	for _, friend := range readFriends(body, target) {
		if added >= 60 {
			break
		}
		host := normalizeHost(friend.URL)
		if !friendWorthy(host, registrable) {
			continue
		}
		if kept := hosts.keep(host); kept != "" {
			result.Friends = append(result.Friends, kept)
			added++
		}
	}
	return added
}

// 检测单个站点
func (scout *Scout) inspect(task context.Context, site *BlogSite, full bool) Result {
	result := Result{State: "alive"}
	if HostOf(site.Host) == "" || inDomainSet(site.Host, blockHosts) || blockedHost(site.Host) {
		result.State, result.Reason = "other", "其它站点"
		return result
	}
	hosts := &hostSet{seen: map[string]bool{site.Host: true}}
	registrable := registrableOf(site.Host)
	page, reached := scout.home(task, site, &result, registrable, hosts)
	if !reached {
		return result
	}
	if full || ((result.Framework == "" || result.Summary == "") && (site.Feed != "" || page.Feed != "")) {
		scout.subscribe(task, site, page, &result)
	}
	var sitemapFriends []string
	if full {
		sitemapFriends = scout.probeSitemap(task, site, &result, hosts)
	}
	if full || site.Friend == "" {
		scout.expand(task, site, page, &result, registrable, hosts, sitemapFriends)
	}
	result.Links = hosts.items
	if state, reason := classify(site, page, &result, true); reason != "" {
		mark(&result, state, reason, "")
	}
	return result
}

// 调整并发数量
func (knob *governor) tune(count Tally) {
	if count.Requests == 0 {
		return
	}
	knob.guard.Lock()
	defer knob.guard.Unlock()
	worker := int(atomic.LoadInt32(&knob.worker))
	switch ratio := float64(count.Timeouts) / float64(count.Requests); {
	case ratio > 0.10:
		worker /= 2
	case ratio < 0.05:
		worker += 4
	}
	atomic.StoreInt32(&knob.worker, int32(min(max(worker, int(knob.floor)), int(knob.ceiling))))
}

// 执行抓取循环
func runCrawl(task context.Context, table *BlogTable, scout *Scout) error {
	floor := int32(runtime.GOMAXPROCS(0))
	knob := &governor{worker: floor, floor: floor, ceiling: floor * 4}
	capacity := int(atomic.LoadInt32(&knob.ceiling))
	jobs := make(chan BlogSite, capacity)
	results := make(chan finished, capacity)
	inflight := map[string]bool{}
	var guard sync.Mutex

	var pool sync.WaitGroup
	for index := 0; index < capacity; index++ {
		pool.Add(1)
		go func() {
			defer pool.Done()
			for site := range jobs {
				siteCtx, cancel := context.WithTimeout(task, scout.Timeout())
				complete := site.Name != "" && site.Feed != "" && site.Language != ""
				result := scout.inspect(siteCtx, &site, site.State != "alive" || !complete)
				cancel()
				results <- finished{site: site, result: result}
			}
		}()
	}

	store := make(chan finished, capacity)
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		for done := range store {
			if len(done.result.Links) > 0 {
				table.queueHosts(done.result.Links, time.Now().UTC())
			}
			if len(done.result.Friends) > 0 {
				done.result.Friend = table.friendIndex(done.result.Friends)
			}
			table.persist(&done.site, done.result)
			guard.Lock()
			delete(inflight, done.site.Host)
			guard.Unlock()
			tail := done.result.Reason
			if tail == "" {
				fields := [][2]string{
					{"feed", done.site.Feed}, {"lang", done.site.Language}, {"name", done.site.Name},
					{"author", done.site.Author}, {"icp", done.site.ICP}, {"summary", done.site.Summary},
					{"tag", done.site.Tags}, {"friend", done.site.Friend}, {"framework", done.site.Framework},
				}
				parts := make([]string, 0, len(fields))
				for _, field := range fields {
					if field[1] != "" {
						parts = append(parts, field[0]+"="+field[1])
					}
				}
				tail = strings.Join(parts, " ")
			}
			fmt.Printf("%s [%s] %s %s\n", time.Now().Format(time.RFC3339), done.site.State, done.site.Host, tail)
		}
	}()

	tuneTicker := time.NewTicker(5 * time.Second)
	defer tuneTicker.Stop()

	var cycle Tally

	handle := func(done finished) {
		guard.Lock()
		cycle.Requests += done.result.Requests
		cycle.Timeouts += done.result.Timeouts
		guard.Unlock()
		store <- done
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
			guard.Lock()
			window := cycle
			cycle = Tally{}
			guard.Unlock()
			knob.tune(window)
		}
		return true
	}

	for running := true; running && task.Err() == nil; {
		guard.Lock()
		pending := len(inflight)
		busy := slices.Collect(maps.Keys(inflight))
		guard.Unlock()

		if want := int(atomic.LoadInt32(&knob.worker)) - pending; want > 0 {
			batch, failure := table.claimDue(time.Now().UTC(), want, busy)
			if failure != nil {
				fmt.Fprintf(os.Stderr, "取站失败：%v\n", failure)
				break
			}
			if len(batch) == 0 {
				running = wait(table.nextWake(time.Now().UTC()))
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
	close(store)
	writer.Wait()
	return nil
}
