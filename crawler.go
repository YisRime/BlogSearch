package main

import (
	"bytes"
	"context"
	"crypto/tls"
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

// 订阅源探测路径
var feedPaths = []string{
	"/atom.xml", "/index.xml", "/feed.xml", "/feed", "/feed/", "/rss.xml",
	"/rss", "/rss/", "/atom", "/index.atom", "/index.rss", "/blog/feed",
}

// 友链页探测路径
var friendPaths = []string{
	"/links", "/friends", "/link", "/friend", "/blogroll",
	"/links/", "/friends/", "/links.html", "/friends.html", "/friendlinks",
}

var sitemapPaths = []string{"/sitemap.xml", "/sitemap_index.xml", "/sitemap-index.xml", "/sitemap/index.xml"}

// 浏览器 UA
const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

// 盾页面标记
var shieldMarks = []string{"just a moment", "cf-challenge", "challenge-platform", "_cf_chl_",
	"attention required", "checking your browser", "enable javascript and cookies",
	"cf_chl_opt", "please wait while we check", "ddos protection by", "under attack mode"}

// meta 跳转
var refreshPattern = regexp.MustCompile(`(?is)<meta[^>]+content\s*=\s*["']?\s*\d+\s*;\s*url\s*=\s*([^"'>\s]+)`)

// JS 跳转
var jsRedirectPattern = regexp.MustCompile(`(?is)location\s*\.\s*(?:href\s*=\s*|replace\s*\(\s*|assign\s*\(\s*)["']([^"']+)["']`)

// 取跳转目标
func refreshTarget(reply *Reply, origin string) string {
	raw := ""
	switch {
	case strings.Contains(strings.ToLower(reply.Refresh), "url="):
		index := strings.Index(strings.ToLower(reply.Refresh), "url=")
		raw = reply.Refresh[index+4:]
	case len(reply.Body) < 4096:
		if match := refreshPattern.FindSubmatch(reply.Body); match != nil {
			raw = string(match[1])
		} else if match := jsRedirectPattern.FindSubmatch(reply.Body); match != nil {
			raw = string(match[1])
		}
	}
	raw = strings.Trim(strings.TrimSpace(raw), `"'`)
	if raw == "" {
		return ""
	}
	root, failure := url.Parse(origin)
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

// 判盾类型
func shieldKind(reply *Reply) string {
	if reply == nil {
		return ""
	}
	if strings.Contains(strings.ToLower(reply.Mitigated), "challenge") {
		return "CloudFlare 挑战"
	}
	if reply.Status == 444 {
		return "数据丢弃"
	}
	head := strings.ToLower(string(reply.Body[:min(len(reply.Body), 8192)]))
	if containsAny(head, shieldMarks) {
		return "WAF 挑战"
	}
	server := strings.ToLower(reply.Server)
	switch {
	case reply.Status == 403 && strings.Contains(server, "cloudflare"):
		return "CloudFlare 拦截"
	case reply.Status == 503 && strings.Contains(server, "cloudflare"):
		return "CloudFlare 限流"
	}
	return ""
}

// 应答
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

// 抓取结果
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

// 统计
type Tally struct {
	Requests int
	Timeouts int
}

// 抓取器
type Scout struct {
	client      *http.Client
	follower    *http.Client
	loose       *http.Client
	looseFollow *http.Client

	guard   sync.Mutex
	latency float64
	learned map[string]string
}

// 主机去重集
type hostSet struct {
	seen  map[string]bool
	items []string
}

// 并发调节
type governor struct {
	guard   sync.Mutex
	worker  int32
	floor   int32
	ceiling int32
}

// 完成项
type finished struct {
	site   BlogSite
	result Result
}

// 构造传输层
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

// 构造抓取器
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

// 取超时值
func (scout *Scout) Timeout() time.Duration {
	scout.guard.Lock()
	latency := scout.latency
	scout.guard.Unlock()
	return min(max(time.Duration(4*latency*float64(time.Second)), 10*time.Second), 60*time.Second)
}

// 读学习缓存
func (scout *Scout) lookup(key string) string {
	scout.guard.Lock()
	defer scout.guard.Unlock()
	return scout.learned[key]
}

// 写学习缓存
func (scout *Scout) remember(key, target string) {
	parsed, failure := url.Parse(target)
	if failure != nil || parsed.Path == "" {
		return
	}
	scout.guard.Lock()
	scout.learned[key] = parsed.Path
	scout.guard.Unlock()
}

// 解码响应体
func decodeBody(body []byte, contentType string) []byte {
	if utf8.Valid(body) {
		return body
	}
	reader, failure := charset.NewReader(bytes.NewReader(body), contentType)
	if failure != nil {
		return body
	}
	text, failure := io.ReadAll(reader)
	if failure != nil || len(text) == 0 {
		return body
	}
	return text
}

// 发单次请求
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
	request.Header.Set("User-Agent", browserUA)
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
	reply := &Reply{Status: response.StatusCode, Body: decodeBody(body, response.Header.Get("Content-Type")),
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

// 请求单页
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
	var timeout interface{ Timeout() bool }
	if errors.Is(failure, context.DeadlineExceeded) || (errors.As(failure, &timeout) && timeout.Timeout()) {
		result.Timeouts++
	}
	return reply, failure
}

// 先直连再回退
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

// 取首个可用
func (scout *Scout) pick(task context.Context, result *Result, candidates []string, accept func(*Reply, string) bool) (*Reply, string, string) {
	reason := ""
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		reply, target, failure := scout.tryEither(task, result, candidate, true)
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

// 记结果状态
func mark(result *Result, state, reason, payload string) {
	result.State = state
	if payload == "" {
		result.Reason = reason
		return
	}
	result.Reason = reason + "：" + payload
}

// 拼候选地址
func siteURLs(host string, guesses []string) []string {
	addresses := make([]string, 0, len(guesses))
	for _, guess := range guesses {
		addresses = append(addresses, "https://"+host+guess)
	}
	return addresses
}

// 收主机去重
func (set *hostSet) keep(raw string) string {
	hostname := normalizeHost(raw)
	if hostname == "" || set.seen[hostname] {
		return ""
	}
	set.seen[hostname] = true
	set.items = append(set.items, hostname)
	return hostname
}

// 判 DNS 缺失
func dnsMissing(failure error) bool {
	record := &net.DNSError{}
	return errors.As(failure, &record) && !record.IsTemporary &&
		(record.IsNotFound || strings.Contains(record.Err, "no data"))
}

// 判死或重试
func deadOrRetry(result *Result, failure error) {
	if dnsMissing(failure) {
		result.State, result.Reason = "dead", "地址无效"
		return
	}
	mark(result, "pend", "抓取失败", failure.Error())
}

// 解析主机
func resolveHost(task context.Context, site *BlogSite, result *Result) bool {
	addresses, failure := net.DefaultResolver.LookupNetIP(task, "ip", site.Host)
	if failure != nil {
		deadOrRetry(result, failure)
		return false
	}
	if len(addresses) == 0 {
		mark(result, "dead", "地址无效", "")
		return false
	}
	return true
}

// 跟随跳转
func (scout *Scout) walkHops(task context.Context, site *BlogSite, result *Result, registrable string, hosts *hostSet) (*Reply, string, bool) {
	target := "https://" + site.Host + "/"
	tried := map[string]bool{target: true}
	var reply *Reply
	var failure error
	for hop := 0; hop <= 3; hop++ {
		wanted := target
		reply, target, failure = scout.tryEither(task, result, target, false)
		tried[wanted], tried[target] = true, true
		if failure != nil {
			deadOrRetry(result, failure)
			return nil, "", false
		}
		if !reply.CertEnd.IsZero() {
			result.CertEnd = reply.CertEnd
		}
		next := ""
		if reply.Status >= 300 && reply.Status < 400 {
			next = reply.Location
			if location, failure := url.Parse(next); failure == nil && !location.IsAbs() {
				if root, failure := url.Parse(target); failure == nil {
					next = root.ResolveReference(location).String()
				}
			}
		} else {
			next = refreshTarget(reply, target)
		}
		if next == "" {
			return reply, target, true
		}
		hostname := normalizeHost(next)
		if hostname == "" {
			mark(result, "pend", "地址无效", "")
			return nil, "", false
		}
		if tried[next] {
			mark(result, "pend", "跳转过多", "")
			return nil, "", false
		}
		hosts.keep(hostname)
		if registrableOf(hostname) != registrable {
			result.State, result.Links = "dead", hosts.items
			return nil, "", false
		}
		if hop == 3 {
			mark(result, "pend", "跳转过多", "")
			return nil, "", false
		}
		target = next
	}
	return nil, "", false
}

// 抓首页
func (scout *Scout) home(task context.Context, site *BlogSite, result *Result, registrable string, hosts *hostSet) (Home, bool) {
	if !resolveHost(task, site, result) {
		return Home{}, false
	}
	reply, target, ok := scout.walkHops(task, site, result, registrable, hosts)
	if !ok {
		return Home{}, false
	}
	if kind := shieldKind(reply); kind != "" {
		mark(result, "pend", "被盾拦截", kind)
		return Home{}, false
	}
	if reply.Status >= 400 {
		mark(result, "pend", "应答失败", strconv.Itoa(reply.Status))
		return Home{}, false
	}
	page := readPage(reply.Body, target)
	if errorPage(page.Name) {
		state := "other"
		if site.State == "alive" {
			state = "pend"
		}
		mark(result, state, "页面错误", "200")
		return Home{}, false
	}
	result.Name, result.Author, result.Language, result.Framework = page.Name, page.Author, page.Language, page.Framework
	result.ICP, result.Summary = page.ICP, page.Summary
	if result.Language == "" {
		result.Language = reply.Language
	}
	return page, true
}

// 抓订阅源
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
		if subscription.Name != "" {
			result.Name = subscription.Name
		}
		if len(subscription.Authors) == 1 {
			result.Author = subscription.Authors[0]
		}
		if subscription.Language != "" {
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

// 找友链页
func (scout *Scout) probeSitemap(task context.Context, site *BlogSite, result *Result, hosts *hostSet) []string {
	candidates := siteURLs(site.Host, sitemapPaths)
	if recalled := scout.lookup("sitemap"); recalled != "" {
		candidates = append([]string{"https://" + site.Host + recalled}, candidates...)
	}
	reply, target, _ := scout.pick(task, result, candidates, nil)
	if reply == nil {
		return nil
	}
	var seeds []string
	for _, link := range ParseSitemap(reply.Body) {
		if hubHosts[registrableOf(link)] {
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

// 抓友链页
func (scout *Scout) expand(task context.Context, site *BlogSite, page Home, result *Result, registrable string, hosts *hostSet, seeds []string) {
	if hubHosts[registrable] {
		return
	}
	key := page.Framework
	if key == "" {
		key = "default"
	}
	if len(seeds) > 3 {
		seeds = seeds[:3]
	}
	homeFriends := page.Friends
	if len(homeFriends) > 3 {
		homeFriends = homeFriends[:3]
	}
	candidates := make([]string, 0, len(seeds)+len(homeFriends)+len(friendPaths)+1)
	if recalled := scout.lookup("friend:" + key); recalled != "" {
		candidates = append(candidates, "https://"+site.Host+recalled)
	}
	candidates = append(candidates, seeds...)
	candidates = append(candidates, homeFriends...)
	candidates = append(candidates, siteURLs(site.Host, friendPaths)...)
	visited := map[string]bool{}
	for tries := 0; tries < 3; tries++ {
		candidate := nextCandidate(&candidates, visited)
		if candidate == "" {
			return
		}
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

// 取下一候选
func nextCandidate(candidates *[]string, visited map[string]bool) string {
	for len(*candidates) > 0 {
		candidate := (*candidates)[0]
		*candidates = (*candidates)[1:]
		if candidate != "" && !visited[candidate] {
			visited[candidate] = true
			return candidate
		}
	}
	return ""
}

// 采集友链主机
func (scout *Scout) harvestFriends(result *Result, registrable string, hosts *hostSet, body []byte, target string) int {
	added := 0
	for _, friend := range readFriends(body, target) {
		host := normalizeHost(friend.URL)
		neighbor := registrableOf(host)
		if neighbor == "" || neighbor == registrable || hubHosts[neighbor] {
			continue
		}
		if kept := hosts.keep(host); kept != "" {
			result.Friends = append(result.Friends, kept)
			added++
		}
	}
	return added
}

// 抓取单站
func (scout *Scout) inspect(task context.Context, site *BlogSite, full bool) Result {
	result := Result{State: "alive"}
	hosts := &hostSet{seen: map[string]bool{site.Host: true}}
	registrable := registrableOf(site.Host)
	page, reached := scout.home(task, site, &result, registrable, hosts)
	if !reached {
		return result
	}
	if full || (result.Summary == "" && (site.Feed != "" || page.Feed != "")) {
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
	if notBlog(site, page, &result) || (page.SinglePost && !page.HasArchive) {
		result.State, result.Reason = "other", "其它站点"
	}
	return result
}

// 调整并发
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

// 取统计窗口
func takeWindow(tally *Tally, guard *sync.Mutex) Tally {
	guard.Lock()
	snapshot := *tally
	*tally = Tally{}
	guard.Unlock()
	return snapshot
}

// 组装日志
func gathered(site *BlogSite) string {
	fields := [][2]string{
		{"feed", site.Feed}, {"lang", site.Language}, {"name", site.Name}, {"author", site.Author},
		{"icp", site.ICP}, {"summary", site.Summary}, {"tag", site.Tags}, {"friend", site.Friend},
		{"framework", site.Framework},
	}
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		if field[1] != "" {
			parts = append(parts, field[0]+"="+field[1])
		}
	}
	return strings.Join(parts, " ")
}

// 跑抓取主循环
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
				tail = gathered(&done.site)
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
