package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

// 排除域名集合
var blockHosts = map[string]bool{}

// 页面拦截词表
var blockWords = []string{"页面不存在", "找不到", "未找到", "域名", "出售", "建站中", "买球",
	"棋牌", "娱乐城", "博彩", "下注", "彩票", "投注", "免费观看", "招聘", "购彩", "球友会", "球盟会",
	"免费直播", "登录入口", "真人视讯", "电子游艺", "包赢", "赌球", "404", "not found", "denied",
	"welcome to nginx", "index of", "default web site page", "for sale",
	"expired", "suspended", "construction", "recent content", "career", "job",
	"recruit", "hiring", "afternic", "sedo", "hugedomains", "parked", "forbidden"}

// 错误页面标记
var errorMarks = []string{"导航", "大全", "合集", "收录", "公司", "集团", "商城", "银行", "保险",
	"正品", "光临", "直播", "私服", "加速器", "已暂停", "在线看", "创建成功", "证券", "press release",
	"news center", "redirecting", "comments on", "corporation", "holdings",
	"investor", "pharmaceutical", "insurance", "securities", "nasdaq", "nyse"}

// 文本截断词表
var cutWords = []string{"，", "。", "！", "？", "；", "：", "、", "…", "“",
	"”", "—", "–", "（", "©", "备案", "保留", "·", "•", ",", ";", "<", "(",
	"|", "powered", "built", "generated", "copyright", "all rights", "icp"}

// 噪声节点标记
var friendSkipMarks = []string{"nav", "menu", "footer", "header",
	"social", "share", "comment", "breadcrumb", "pagination", "copyright",
	"credit", "powered", "widget", "sidebar", "toolbar", "privacy",
	"policy", "agreement", "cookie", "legal", "disclaimer", "terms"}

// 友链识别标记
var friendMarks = []string{"友链", "友邻", "朋友", "友情", "friend", "blogroll", "link"}

// 友链名称噪声
var friendNameNoise = []string{"申请", "添加", "提交", "首页", "更多", "返回", "一页", "全文", "评论", "导航",
	"留言", "关于", "归档", "标签", "分类", "订阅", "登录", "注册", "搜索", "友情链接", "友链", "工具", "平台", "官网", "资源",
	"下载", "文档", "社区", "论坛", "联系", "捐赠", "赞助", "广告", "统计", "站点", "应用", "服务", "产品", "项目", "详情",
	"查看", "进入", "点击", "跳转", "访问", "more", "about", "archive", "category", "rss", "links", "tools",
	"platform", "resource", "download", "doc", "forum", "community", "contact", "sponsor", "donate"}

// 通用标题词表
var genericNames = map[string]bool{"首页": true, "主页": true, "网站首页": true,
	"欢迎": true, "博客": true, "频道": true, "关于": true, "个人博客": true, "我的博客": true,
	"网站主页": true, "的评论": true, "home": true, "homepage": true, "home page": true,
	"blog": true, "index": true, "untitled": true, "start": true, "welcome": true,
	"description": true, "posts": true, "about": true, "about me": true,
	"main": true, "portal": true, "hello world": true, "hexo": true, "gridea": true}

// 作者名称噪声
var authorNoise = map[string]bool{"互动": true, "评论": true, "登录": true, "注册": true,
	"分享": true, "首页": true, "投稿": true, "作者": true, "博主": true, "日期": true, "时间": true,
	"張貼者": true, "张贴者": true, "管理员": true, "站长": true, "by": true, "posted by": true,
	"admin": true, "administrator": true, "author": true, "unknown": true, "anonymous": true,
	"none": true, "n/a": true, "na": true, "seo": true, "john doe": true, "xxxx": true}

// 框架名称噪声
var frameworkNoise = map[string]bool{"by": true, "with": true, "using": true,
	"powered": true, "and": true, "the": true, "for": true, "on": true, "in": true,
	"a": true, "an": true, "built": true, "version": true, "all": true, "site": true,
	"kit": true, "wp": true, "seo": true, "google": true, "microsoft": true,
	"clarity": true, "elementor": true, "rocket": true, "pro": true, "cache": true,
	"plugin": true, "theme": true, "yoast": true, "jetpack": true, "woocommerce": true}

// 框架识别特征
var frameworkMarks = [][3]string{{"wordpress", "wp-content", "WordPress"},
	{"typecho", "/usr/themes/", "Typecho"}, {"hexo", "hexo-theme", "Hexo"},
	{"astro", "/_astro/", "Astro"}, {"next.js", "_next/", "Next.js"}, {"nuxt", "/_nuxt/", "Nuxt"},
	{"gatsby", "___gatsby", "Gatsby"}, {"ghost", "/assets/built/", "Ghost"},
	{"halo", "halo-theme", "Halo"}, {"sveltekit", "__sveltekit", "SvelteKit"},
	{"remix", "__remix", "Remix"}, {"docusaurus", "__docusaurus", "Docusaurus"}}

// 标签剥离正则
var htmlTag = regexp.MustCompile(`<[^>]*>`)

// 版权行正则
var copyrightMark = regexp.MustCompile(`(?i)(?:©|\(c\)|copyright)\s*(?:\d{4}(?:\s*[-–~至]\s*\d{4})?)?\s*`)

// 备案号正则
var icpMark = regexp.MustCompile(`[\p{Han}]?ICP备\d+号|公网安备\d+号`)

// 首页信息结构
type Home struct {
	Name       string
	Author     string
	Framework  string
	Language   string
	Feed       string
	ICP        string
	Summary    string
	Friends    []string
	HasArchive bool
	SinglePost bool
	HasLinks   bool
}

// 订阅源信息
type Feed struct {
	Parsed    bool
	Name      string
	Authors   []string
	Language  string
	Summary   string
	Generator string
}

// 作者姓名
type personName struct {
	Name string `json:"name" xml:"name"`
}

// 传统订阅结构
type rssFeed struct {
	Channel struct {
		Title          string `xml:"title"`
		Description    string `xml:"description"`
		Language       string `xml:"language"`
		Creator        string `xml:"http://purl.org/dc/elements/1.1/ creator"`
		ManagingEditor string `xml:"managingEditor"`
		Author         string `xml:"author"`
		Generator      string `xml:"generator"`
		Items          []struct {
			Creator string `xml:"http://purl.org/dc/elements/1.1/ creator"`
			Author  string `xml:"author"`
		} `xml:"item"`
	} `xml:"channel"`
}

// 原子订阅结构
type atomFeed struct {
	XMLName   xml.Name   `xml:"feed"`
	Title     string     `xml:"title"`
	Subtitle  string     `xml:"subtitle"`
	Language  string     `xml:"http://www.w3.org/XML/1998/namespace lang,attr"`
	Generator string     `xml:"generator"`
	Author    personName `xml:"author"`
	Entries   []struct {
		Authors []personName `xml:"author"`
	} `xml:"entry"`
}

// 轻量订阅结构
type jsonFeed struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Items       []struct {
		Authors []personName `json:"authors"`
	} `json:"items"`
}

// 页面信息汇总
type metaInfo struct {
	title     string
	generator string
	author    string
	htmlLang  string
	content   string
	locale    string
	site      string
	ogTitle   string
	twitter   string
	app       string
	summary   string
	address   string
	domAuthor string
}

// 链接扫描结构
type linkScan struct {
	root        *url.URL
	registrable string
	cache       map[string]string
}

// 博客友链条目
type Friend struct {
	Name string
	URL  string
}

// 载入排除域名
func init() {
	body, failure := os.ReadFile("data/host.txt")
	if failure != nil {
		return
	}
	for _, line := range strings.Split(string(body), "\n") {
		host := strings.ToLower(strings.TrimSpace(line))
		if host == "" || strings.HasPrefix(host, "#") {
			continue
		}
		blockHosts[host] = true
	}
}

// 检查拦截词语
func blockedText(text string) bool {
	_, hit := firstMark(strings.ToLower(text), blockWords)
	return hit
}

// 检查拦截域名
func blockedHost(host string) bool {
	for _, label := range strings.Split(strings.ToLower(host), ".") {
		if slices.Contains(blockWords, label) {
			return true
		}
	}
	return false
}

// 筛选友链候选
func friendWorthy(host, site string) bool {
	neighbor := registrableOf(host)
	return neighbor != "" && neighbor != site && !inDomainSet(host, blockHosts) && !blockedHost(host)
}

// 规范化主机名
func normalizeHost(raw string) string {
	word := strings.TrimSpace(raw)
	if word == "" {
		return ""
	}
	if index := strings.Index(word, "://"); index >= 0 {
		word = word[index+3:]
	} else {
		word = strings.TrimPrefix(word, "//")
	}
	if index := strings.LastIndex(word, "@"); index >= 0 {
		word = word[index+1:]
	}
	if index := strings.IndexAny(word, "/?#\\"); index >= 0 {
		word = word[:index]
	}
	if strings.HasPrefix(word, "[") {
		if end := strings.Index(word, "]"); end >= 0 {
			word = word[:end+1]
		}
	} else if index := strings.LastIndex(word, ":"); index >= 0 && !strings.Contains(word[index+1:], ".") {
		word = word[:index]
	}
	word = strings.Trim(word, ".")
	if word == "" {
		return ""
	}
	if _, failure := netip.ParseAddr(word); failure == nil {
		return word
	}
	lower := strings.ToLower(word)
	if encoded, failure := idna.Lookup.ToASCII(lower); failure == nil && encoded != "" {
		lower = strings.ToLower(encoded)
	}
	if labels := strings.Split(lower, "."); len(labels) > 2 && labels[0] == "www" {
		lower = strings.Join(labels[1:], ".")
	}
	return lower
}

// 获取注册域名
func registrableOf(host string) string {
	suffix, _ := publicsuffix.PublicSuffix(host)
	if suffix == "" || !strings.HasSuffix(host, suffix) {
		return ""
	}
	suffixLabels := strings.Count(suffix, ".") + 1
	labels := strings.Split(host, ".")
	if len(labels) <= suffixLabels {
		return ""
	}
	return strings.Join(labels[len(labels)-suffixLabels-1:], ".")
}

// 检查域名集合
func inDomainSet(host string, set map[string]bool) bool {
	for {
		if set[host] {
			return true
		}
		index := strings.Index(host, ".")
		if index < 0 {
			return false
		}
		host = host[index+1:]
	}
}

// 获取合法主机
func HostOf(host string) string {
	if host == "" {
		return ""
	}
	host = strings.ToLower(host)
	if _, failure := netip.ParseAddr(host); failure == nil {
		return ""
	}
	if len(host) > 253 || !strings.Contains(host, ".") {
		return ""
	}
	suffixes := []string{"local", "gov", "mil", "test", "example", "internal", "onion"}
	for _, label := range strings.Split(host, ".")[1:] {
		if slices.Contains(suffixes, label) {
			return ""
		}
	}
	if inDomainSet(host, blockHosts) {
		return ""
	}
	return host
}

// 识别错误页面
func errorPage(name string) (bool, string) {
	word := strings.ToLower(collapse(name))
	if mark, hit := firstMark(word, blockWords); hit {
		return true, mark
	}
	if mark, hit := firstMark(word, errorMarks); hit {
		return true, mark
	}
	return false, ""
}

// 判断站点性质
func classify(site *BlogSite, page Home, result *Result, done bool) (string, string) {
	label := collapse(page.Name)
	plain := result.Feed == "" && site.Feed == "" && result.Framework == "" && result.ICP == "" &&
		!page.HasArchive && len(result.Friends) == 0
	if hit, mark := errorPage(page.Name); hit {
		if site.State == "alive" {
			return "pend", "页面错误：" + mark
		}
		return "other", "页面错误：" + mark
	}
	if done && (page.SinglePost && !page.HasArchive ||
		plain && (inDomainSet(site.Host, blockHosts) || label == "" || genericName(label))) {
		return "other", "其它站点"
	}
	return "", ""
}

// 检查名称有效
func nameOK(word string, maxRunes, maxWords int) bool {
	size := utf8.RuneCountInString(word)
	return size >= 2 && size <= maxRunes && !strings.Contains(word, "://") &&
		len(strings.Fields(word)) <= maxWords
}

// 按词标记截断
func cutAt(text string, stops []string) string {
	lower := strings.ToLower(text)
	end := len(text)
	for _, stop := range stops {
		if index := strings.Index(lower, stop); index >= 0 && index < end {
			end = index
		}
	}
	return strings.TrimSpace(text[:end])
}

// 清除首尾符号
func trimSymbols(text string) string {
	return strings.TrimFunc(text, func(symbol rune) bool {
		return !unicode.IsLetter(symbol) && !unicode.IsDigit(symbol) && !unicode.IsSpace(symbol)
	})
}

// 清理语言标签
func cleanLang(text string) string {
	word := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(text), "_", "-"))
	if index := strings.IndexAny(word, ",; "); index > 0 {
		word = word[:index]
	}
	parts := strings.Split(word, "-")
	if len(parts) == 0 || len(parts[0]) < 2 || len(parts[0]) > 3 {
		return ""
	}
	for _, part := range parts {
		if part == "" || len(part) > 8 || strings.Trim(part, "abcdefghijklmnopqrstuvwxyz0123456789") != "" {
			return ""
		}
	}
	return word
}

// 清理站点名称
func tidyTitle(text string) string {
	full := collapse(html.UnescapeString(text))
	name := full
	cut := max(strings.IndexAny(name, "｜·»•・"), 0)
	offset := 0
	for _, field := range strings.Fields(name) {
		position := strings.Index(name[offset:], field) + offset
		offset = position + len(field)
		if position > 0 && utf8.RuneCountInString(field) <= 2 && strings.Trim(field, "-–—_|｜/·»•・~") == "" {
			cut = position
			break
		}
	}
	if head := strings.TrimSpace(name[:cut]); head != "" && utf8.RuneCountInString(head) <= 32 {
		name = head
	}
	name = trimSymbols(strings.TrimSpace(name))
	if genericName(name) || utf8.RuneCountInString(name) > 40 {
		for _, part := range strings.FieldsFunc(full, func(symbol rune) bool { return strings.ContainsRune("-–—_|｜/·»•・~", symbol) }) {
			part = trimSymbols(strings.TrimSpace(part))
			size := utf8.RuneCountInString(part)
			if size >= 2 && size <= 32 && !genericName(part) && !strings.ContainsAny(part, "0123456789") {
				name = part
				break
			}
		}
	}
	return clip(name, 32)
}

// 清理摘要内容
func summaryText(text string) string {
	word := collapse(htmlTag.ReplaceAllString(html.UnescapeString(text), " "))
	lower := strings.ToLower(word)
	if utf8.RuneCountInString(word) < 4 || blockedText(lower) || genericName(lower) {
		return ""
	}
	return clip(word, 256)
}

// 清理作者名称
func authorName(text string, refs ...string) string {
	word := trimSymbols(collapse(text))
	if utf8.RuneCountInString(word) > 20 && containsAny(word, cutWords) {
		return ""
	}
	word = cutAt(word, cutWords)
	for _, prefix := range []string{"作者是", "作者:", "文/", "by "} {
		word = strings.TrimSpace(strings.TrimPrefix(word, prefix))
	}
	if index := strings.LastIndex(strings.ToLower(word), " by "); index >= 0 {
		if tail := strings.TrimSpace(word[index+4:]); nameOK(tail, 16, 3) && !strings.ContainsAny(tail, "0123456789") {
			word = tail
		}
	}
	word = trimSymbols(strings.TrimPrefix(word, "@"))
	if !nameOK(word, 16, 6) || strings.ContainsAny(word, "0123456789@©") ||
		strings.Trim(word, "0123456789") == "" || authorNoise[strings.ToLower(word)] {
		return ""
	}
	lower := strings.ToLower(word)
	for _, ref := range refs {
		if lower == strings.ToLower(collapse(ref)) {
			return ""
		}
	}
	return word
}

// 解析作者信息
func parseAuthor(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil && text != "" {
		return text
	}
	var one personName
	if json.Unmarshal(raw, &one) == nil && one.Name != "" {
		return one.Name
	}
	var many []personName
	if json.Unmarshal(raw, &many) == nil {
		names := make([]string, 0, len(many))
		for _, item := range many {
			if item.Name != "" {
				names = append(names, item.Name)
			}
		}
		if len(names) == 1 {
			return names[0]
		}
	}
	return ""
}

// 识别站点框架
func detectFramework(stated string, body []byte) string {
	lower := strings.ToLower(stated)
	for _, row := range frameworkMarks {
		if strings.Contains(lower, row[0]) {
			return row[2]
		}
	}
	fields := strings.Fields(strings.Trim(collapse(stated), ",.;: "))
	kept := make([]string, 0, 2)
	for _, field := range fields {
		if strings.ContainsAny(field, "0123456789") {
			break
		}
		kept = append(kept, field)
		if len(kept) == 2 {
			break
		}
	}
	for len(kept) > 0 && frameworkNoise[strings.ToLower(kept[len(kept)-1])] {
		kept = kept[:len(kept)-1]
	}
	if len(kept) > 0 && !frameworkNoise[strings.ToLower(kept[0])] {
		name := strings.Join(kept, " ")
		plain := strings.ToLower(name)
		slug := strings.Contains(name, "-") && strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789-") == ""
		if size := utf8.RuneCountInString(name); size >= 2 && size <= 32 && !slug &&
			!strings.ContainsAny(name, "0123456789@") && !strings.Contains(name, "'s ") &&
			!strings.Contains(name, "’s ") && !containsAny(plain, []string{"atom", "rss", "feed"}) &&
			!strings.Contains(plain, "generator") {
			return name
		}
	}
	lowered := bytes.ToLower(body)
	for _, row := range frameworkMarks {
		if bytes.Contains(lowered, []byte(row[1])) {
			return row[2]
		}
	}
	return ""
}

// 扫描页面链接
func (scan *linkScan) visit(node *html.Node, link, section string, page *Home) {
	entry := deepText(node)
	if !page.HasArchive {
		page.HasArchive = containsAny(strings.ToLower(link+" "+entry), []string{"归档", "分页", "archive", "/page", "older", "newer"})
	}
	parsed := resolveLink(link, scan.root)
	if parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return
	}
	hostname, ok := scan.cache[parsed.Host]
	if !ok {
		hostname = normalizeHost(parsed.Host)
		scan.cache[parsed.Host] = hostname
	}
	if page.Feed == "" && registrableOf(hostname) == scan.registrable &&
		containsAny(strings.ToLower(parsed.Path+"?"+parsed.RawQuery), []string{"atom", "rss", "feed"}) {
		page.Feed = parsed.String()
	}
	rel := strings.ToLower(attrOf(node, "rel"))
	style := strings.ToLower(attrOf(node, "class") + " " + attrOf(node, "id"))
	if section == "nav" || section == "footer" || entry == "" || blockedRelation(rel) ||
		strings.Contains(style, "social") || strings.Contains(style, "share") {
		return
	}
	page.HasLinks = true
	if registrableOf(hostname) == scan.registrable {
		lower := strings.ToLower(entry + " " + parsed.Path)
		if strings.Contains(rel, "friend") || strings.Contains(rel, "blogroll") ||
			containsAny(lower, friendMarks) {
			page.Friends = append(page.Friends, parsed.String())
		}
	}
}

// 提取子树文本
func deepText(root *html.Node) string {
	var buffer strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		switch node.Type {
		case html.TextNode:
			buffer.WriteString(node.Data)
			buffer.WriteByte(' ')
			return
		case html.ElementNode:
			if tag := strings.ToLower(node.Data); tag == "script" || tag == "style" || tag == "svg" || tag == "noscript" {
				return
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return collapse(buffer.String())
}

// 寻找首个匹配
func findNode(root *html.Node, match func(*html.Node) bool) *html.Node {
	if root.Type == html.ElementNode && match(root) {
		return root
	}
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if found := findNode(child, match); found != nil {
			return found
		}
	}
	return nil
}

// 提取标记文本
func markedText(root *html.Node, marks []string) string {
	node := findNode(root, func(node *html.Node) bool {
		if !containsAny(strings.ToLower(attrOf(node, "class")+" "+attrOf(node, "id")), marks) {
			return false
		}
		return deepText(node) != ""
	})
	if node == nil {
		return ""
	}
	return deepText(node)
}

// 提取友链信息
func readFriends(body []byte, origin string) []Friend {
	document, failure := html.Parse(bytes.NewReader(body))
	if failure != nil {
		return nil
	}
	root, _ := url.Parse(origin)
	site := registrableOf(normalizeHost(origin))
	var order []*html.Node
	bucket := map[*html.Node][]Friend{}
	marked := map[*html.Node]bool{}
	seen := map[string]bool{}
	var walk func(*html.Node, string, bool, *html.Node)
	walk = func(node *html.Node, section string, blocked bool, group *html.Node) {
		if node.Type == html.ElementNode {
			tag := strings.ToLower(node.Data)
			if tag == "nav" || tag == "footer" || tag == "header" {
				section = tag
			}
			style := strings.ToLower(attrOf(node, "class") + " " + attrOf(node, "id"))
			friendly := containsAny(style, friendMarks) && !containsAny(style, friendSkipMarks)
			if style != "" {
				switch {
				case friendly:
					blocked = false
				case containsAny(style, friendSkipMarks):
					blocked = true
				}
			}
			if tag == "ul" || tag == "ol" || friendly {
				if _, exists := bucket[node]; !exists {
					bucket[node] = nil
					order = append(order, node)
				}
				group, marked[node] = node, friendly || marked[node]
			}
			if tag == "a" && !blocked && section != "nav" && section != "footer" && section != "header" {
				rel := strings.ToLower(attrOf(node, "rel"))
				parsed := resolveLink(attrOf(node, "href"), root)
				if !blockedRelation(rel) && !containsAny(style, friendSkipMarks) && parsed != nil &&
					(parsed.Scheme == "http" || parsed.Scheme == "https") &&
					friendWorthy(normalizeHost(parsed.Host), site) {
					alt := ""
					if img := findNode(node, func(spot *html.Node) bool {
						return strings.EqualFold(spot.Data, "img") && collapse(attrOf(spot, "alt")) != ""
					}); img != nil {
						alt = collapse(attrOf(img, "alt"))
					}
					for _, candidate := range []string{markedText(node, []string{"name", "title"}), alt, attrOf(node, "title"), deepText(node)} {
						name := strings.Trim(collapse(html.UnescapeString(candidate)), " \t\\-–—_|｜/·»•・~")
						size := utf8.RuneCountInString(name)
						if size < 2 || size > 32 || strings.Contains(name, "://") || strings.ContainsAny(name, "©@") ||
							containsAny(strings.ToLower(name), friendNameNoise) {
							continue
						}
						host := normalizeHost(parsed.Host)
						if !seen[host] {
							seen[host] = true
							if _, exists := bucket[group]; !exists {
								bucket[group] = nil
								order = append(order, group)
							}
							bucket[group] = append(bucket[group], Friend{Name: name, URL: parsed.Scheme + "://" + host + "/"})
						}
						break
					}
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child, section, blocked, group)
		}
	}
	walk(document, "", false, document)
	var friends []Friend
	for _, group := range order {
		if marked[group] || len(bucket[group]) >= 2 {
			friends = append(friends, bucket[group]...)
		}
	}
	return friends
}

// 解析首页内容
func readPage(body []byte, origin string) Home {
	var page Home
	document, failure := html.Parse(bytes.NewReader(body))
	if failure != nil {
		return page
	}
	info := &metaInfo{}
	root, _ := url.Parse(origin)
	scan := &linkScan{root: root, registrable: registrableOf(normalizeHost(origin)), cache: map[string]string{}}
	var texts strings.Builder
	var jsonlds []string
	length := 0
	var walk func(*html.Node, string)
	walk = func(node *html.Node, section string) {
		if node.Type == html.ElementNode {
			tag := strings.ToLower(node.Data)
			if tag == "nav" || tag == "footer" {
				section = tag
			}
			switch tag {
			case "script":
				if strings.Contains(strings.ToLower(attrOf(node, "type")), "ld+json") {
					if text := textOf(node); text != "" {
						jsonlds = append(jsonlds, text)
					}
				}
				return
			case "style", "noscript", "svg":
				return
			case "title":
				info.title = textOf(node)
			case "address":
				if info.address == "" {
					info.address = textOf(node)
				}
			case "meta":
				name := strings.ToLower(attrOf(node, "name"))
				property := strings.ToLower(attrOf(node, "property"))
				equiv := strings.ToLower(attrOf(node, "http-equiv"))
				content := attrOf(node, "content")
				switch {
				case name == "generator" && info.generator == "":
					info.generator = content
				case info.author == "" && (name == "author" || name == "article:author" || property == "article:author" || name == "twitter:creator"):
					info.author = content
				case info.content == "" && (name == "language" || equiv == "content-language"):
					info.content = content
				case info.site == "" && (name == "og:site_name" || property == "og:site_name"):
					info.site = content
				case info.ogTitle == "" && (name == "og:title" || property == "og:title"):
					info.ogTitle = content
				case info.twitter == "" && name == "twitter:title":
					info.twitter = content
				case info.app == "" && name == "application-name":
					info.app = content
				case info.summary == "" && (name == "description" || name == "og:description" || property == "og:description"):
					info.summary = content
				case info.locale == "" && (name == "og:locale" || property == "og:locale"):
					info.locale = content
				}

			case "html":
				info.htmlLang = cleanLang(attrOf(node, "lang"))
			case "body":
				page.SinglePost = slices.ContainsFunc(strings.Fields(strings.ToLower(attrOf(node, "class"))),
					func(token string) bool { return strings.HasPrefix(token, "single") })
			case "link":
				kind, link := strings.ToLower(attrOf(node, "type")), attrOf(node, "href")
				if page.Feed == "" && link != "" && strings.Contains(strings.ToLower(attrOf(node, "rel")), "alternate") &&
					containsAny(kind, []string{"atom", "rss", "feed"}) {
					if parsed := resolveLink(link, root); parsed != nil {
						page.Feed = parsed.String()
					}
				}
			case "a":
				link := attrOf(node, "href")
				if link == "" {
					return
				}
				scan.visit(node, link, section, &page)
			default:
				if info.domAuthor == "" && tag != "article" && tag != "h1" && tag != "h2" {
					style := strings.ToLower(attrOf(node, "class") + " " + attrOf(node, "id"))
					if containsAny(style, []string{"author", "byline", "vcard"}) {
						info.domAuthor = pickFirst(markedText(node, []string{"name", "title"}), deepText(node))
					}
				}
			}
		}
		if node.Type == html.TextNode && length < 4096 {
			texts.WriteString(node.Data)
			length += len(node.Data)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child, section)
		}
	}
	walk(document, "")
	var jsonAuthor, jsonTitle string
	for _, raw := range jsonlds {
		var document struct {
			Name     string          `json:"name"`
			Headline string          `json:"headline"`
			Author   json.RawMessage `json:"author"`
			Graph    []struct {
				Type   interface{}     `json:"@type"`
				Name   string          `json:"name"`
				Author json.RawMessage `json:"author"`
			} `json:"@graph"`
		}
		if json.Unmarshal([]byte(raw), &document) != nil {
			continue
		}
		if jsonTitle == "" {
			jsonTitle = pickFirst(document.Name, document.Headline)
		}
		if jsonAuthor == "" {
			jsonAuthor = parseAuthor(document.Author)
		}
		if jsonAuthor == "" {
			for _, node := range document.Graph {
				person := false
				switch typed := node.Type.(type) {
				case string:
					person = strings.Contains(typed, "Person")
				case []interface{}:
					for _, item := range typed {
						if text, ok := item.(string); ok && strings.Contains(text, "Person") {
							person = true
						}
					}
				}
				if person && node.Name != "" {
					jsonAuthor = node.Name
					break
				}
			}
		}
		if jsonAuthor == "" {
			for _, node := range document.Graph {
				if candidate := parseAuthor(node.Author); candidate != "" {
					jsonAuthor = candidate
					break
				}
			}
		}
		if jsonAuthor != "" && jsonTitle != "" {
			break
		}
	}
	label := ""
	for _, value := range []string{info.site, info.title, info.ogTitle, info.twitter, info.app, jsonTitle} {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if label == "" {
			label = value
		}
		if !genericName(collapse(html.UnescapeString(value))) {
			label = value
			break
		}
	}
	page.Name = tidyTitle(label)
	if page.Name == "" || genericName(page.Name) {
		page.Name = registrableOf(normalizeHost(origin))
	}
	visible := collapse(texts.String())
	page.Language = pickFirst(info.htmlLang, cleanLang(info.locale), cleanLang(info.content))
	page.Framework = detectFramework(info.generator, body)
	host := normalizeHost(origin)
	accept := func(candidate string) string { return authorName(candidate, host) }
	page.Author = pickFirst(accept(info.author), accept(jsonAuthor), accept(info.domAuthor), accept(info.address))
	if page.Author == "" {
		if match := copyrightMark.FindStringIndex(visible); match != nil {
			phrase := collapse(cutAt(visible[match[1]:], cutWords))
			phrase = strings.Trim(phrase, " \t:：-—、,.。;；")
			if !containsAny(strings.ToLower(phrase), []string{"访客", "评论", "登录", "搜索", "·", "•", "|", "｜", "/", "rss", "design", "theme"}) && nameOK(phrase, 32, 6) &&
				!strings.ContainsAny(phrase, "0123456789@©") {
				page.Author = accept(phrase)
			}
		}
	}
	if match := icpMark.Find(body); match != nil {
		page.ICP = string(match)
	}
	if info.summary != "" {
		page.Summary = summaryText(info.summary)
	}
	return page
}

// 解析订阅内容
func unmarshalFeed(body []byte, target any) error {
	if failure := xml.Unmarshal(body, target); failure == nil {
		return nil
	}
	if index := bytes.LastIndex(body, []byte("</item>")); index > 0 {
		body = append(append([]byte{}, body[:index+len("</item>")]...), "</channel></rss>"...)
	} else if index := bytes.LastIndex(body, []byte("</entry>")); index > 0 {
		body = append(append([]byte{}, body[:index+len("</entry>")]...), "</feed>"...)
	}
	return xml.Unmarshal(body, target)
}

// 添加作者信息
func (feed *Feed) pushAuthor(author string) {
	word := authorName(author)
	if word != "" && !slices.ContainsFunc(feed.Authors,
		func(existing string) bool { return strings.EqualFold(existing, word) }) {
		feed.Authors = append(feed.Authors, word)
	}
}

// 解析订阅信息
func ParseSubscription(body []byte) Feed {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return Feed{}
	}
	var subscription Feed
	head := bytes.ToLower(trimmed[:min(len(trimmed), 1024)])
	switch {
	case bytes.HasPrefix(trimmed, []byte("{")):
		var decoded jsonFeed
		if json.Unmarshal(trimmed, &decoded) != nil {
			return Feed{}
		}
		subscription = Feed{Parsed: true, Name: tidyTitle(decoded.Title), Summary: summaryText(decoded.Description)}
		for _, item := range decoded.Items {
			for _, person := range item.Authors {
				subscription.pushAuthor(person.Name)
			}
		}
	case bytes.Contains(head, []byte("<rss")) || bytes.Contains(head, []byte("<rdf")):
		var channel rssFeed
		if unmarshalFeed(trimmed, &channel) != nil {
			return Feed{}
		}
		subscription = Feed{Parsed: true, Name: tidyTitle(channel.Channel.Title), Language: cleanLang(channel.Channel.Language),
			Summary: summaryText(channel.Channel.Description), Generator: channel.Channel.Generator}
		subscription.pushAuthor(channel.Channel.Creator)
		subscription.pushAuthor(channel.Channel.ManagingEditor)
		subscription.pushAuthor(channel.Channel.Author)
		for _, item := range channel.Channel.Items {
			subscription.pushAuthor(item.Creator)
			subscription.pushAuthor(item.Author)
		}
	default:
		var document atomFeed
		if unmarshalFeed(trimmed, &document) != nil {
			return Feed{}
		}
		subscription = Feed{Parsed: true, Name: tidyTitle(document.Title), Language: cleanLang(document.Language),
			Summary: summaryText(document.Subtitle), Generator: document.Generator}
		subscription.pushAuthor(document.Author.Name)
		for _, entry := range document.Entries {
			for _, person := range entry.Authors {
				subscription.pushAuthor(person.Name)
			}
		}
	}
	return subscription
}
