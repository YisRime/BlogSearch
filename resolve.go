package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

var hubHosts = map[string]bool{
	"github.com": true, "zhihu.com": true, "weibo.com": true, "csdn.net": true,
	"jianshu.com": true, "facebook.com": true, "twitter.com": true, "x.com": true,
	"youtube.com": true, "medium.com": true, "douban.com": true, "bilibili.com": true,
	"linkedin.com": true, "instagram.com": true, "pinterest.com": true, "reddit.com": true,
	"tumblr.com": true, "wordpress.com": true, "blogspot.com": true, "wanandroid.com": true,
	"ituring.com.cn": true, "foreverblog.cn": true, "storeweb.cn": true,
	"listbox.app": true, "urschool.org": true, "cafenomad.tw": true,
}

var noiseHosts = map[string]bool{
	"gstatic.com": true, "googleapis.com": true, "googleusercontent.com": true, "ggpht.com": true,
	"gravatar.com": true, "wp.com": true, "w.org": true, "typekit.net": true, "fontawesome.com": true,
	"cloudflare.com": true, "cloudfront.net": true, "fastly.net": true, "akamaihd.net": true,
	"akamai.net": true, "azureedge.net": true, "bunnycdn.com": true, "gcorelabs.com": true,
	"jsdelivr.net": true, "unpkg.com": true, "cdnjs.com": true, "bootcdn.net": true,
	"staticfile.org": true, "statically.io": true, "bootstrapcdn.com": true, "aspnetcdn.com": true,
	"google-analytics.com": true, "googletagmanager.com": true, "doubleclick.net": true,
	"sentry.io": true, "hotjar.com": true, "mixpanel.com": true, "segment.io": true,
	"amplitude.com": true, "clarity.ms": true, "umami.is": true, "plausible.io": true, "matomo.cloud": true,
	"cnzz.com": true, "umeng.com": true, "umengcloud.com": true, "ibruce.info": true,
	"disqus.com": true, "addthis.com": true, "sharethis.com": true, "addtoany.com": true,
	"imgur.com": true, "imgbb.com": true, "sm.ms": true, "sinaimg.cn": true, "zhimg.com": true,
	"gtimg.cn": true, "hdslb.com": true, "doubanio.com": true, "byteimg.com": true, "pstatp.com": true,
	"fbcdn.net": true, "twimg.com": true, "ytimg.com": true, "cdninstagram.com": true, "pinimg.com": true,
	"licdn.com": true, "zstatic.com": true, "githubassets.com": true, "githubusercontent.com": true,
	"alicdn.com": true, "aliyuncs.com": true, "myqcloud.com": true, "upyun.com": true,
	"qiniucdn.com": true, "qiniudn.com": true, "bdstatic.com": true, "126.net": true, "127.net": true,
	"xhscdn.com": true,
}

var reservedHosts = []string{".local", ".localhost", ".test", ".invalid", ".example", ".internal", ".home", ".lan", ".onion"}

var blockedSuffixes = []string{".gov", ".mil"}

var errorTitles = []string{"404", "not found", "页面不存在", "找不到页面", "access denied",
	"welcome to nginx", "index of /", "default web site page", "站点未找到",
	"域名出售", "域名停放", "域名已过期", "已到期", "请续费",
	"domain for sale", "this domain is for sale", "buy this domain"}

var junkWords = []string{"index of", "网站维护中", "建站中", "维护中", "coming soon",
	"域名出售", "域名停放", "网址导航", "搜索引擎"}

var feedTypes = []string{"atom", "rss", "feed+json"}

var summaryNoise = []string{"will go into a meta tag", "recent content on "}

var summaryBlank = map[string]bool{"description": true, "blog": true, "posts": true, "home": true, "just a moment": true}

var archiveHints = []string{"archive", "归档", "分页", "/page/", "older", "newer"}

var friendHints = []string{"友链", "友情链接", "链接", "friend"}

var authorMarks = []string{"author", "byline", "vcard"}

var authorNoise = map[string]bool{
	"by": true, "posted by": true, "admin": true, "administrator": true, "author": true,
	"unknown": true, "anonymous": true, "none": true, "n/a": true, "na": true,
	"互动": true, "评论": true, "登录": true, "注册": true, "分享": true, "首页": true,
	"投稿": true, "作者": true, "博主": true,
}

var copyrightStops = []string{"保留", "all rights", "。", "备案", "icp"}

var copyrightNoise = []string{"·", "•", "|", "｜", "/", "访客", "评论", "登录", "搜索", "rss", "design", "theme"}

var frameworkNoise = map[string]bool{
	"by": true, "with": true, "using": true, "powered": true, "and": true, "the": true,
	"for": true, "on": true, "in": true, "a": true, "an": true, "built": true, "version": true,
}

var frameworkMarks = [][3]string{
	{"wordpress", "wp-content", "WordPress"},
	{"hexo", "hexo-theme", "Hexo"},
	{"typecho", "/usr/themes/", "Typecho"},
	{"astro", "/_astro/", "Astro"},
	{"next.js", "_next/", "Next.js"},
	{"gatsby", "___gatsby", "Gatsby"},
	{"ghost", "/assets/built/", "Ghost"},
}

var feedClosers = [][2]string{{"</item>", "</channel></rss>"}, {"</entry>", "</feed>"}}

var icpPattern = regexp.MustCompile(`[\p{Han}]?ICP备\d+号|公网安备\d+号`)

var copyrightPattern = regexp.MustCompile(`(?i)(?:©|\(c\)|copyright)\s*(?:\d{4}(?:\s*[-–~至]\s*\d{4})?)?\s*`)

const (
	titleEdges     = " \t|｜/\\-—–_~·»•・"
	separatorChars = "-–—_|｜/·»•・~"
	titleMarks     = "｜·»•・"
)

type Home struct {
	Name       string
	Author     string
	Framework  string
	Language   string
	Feed       string
	ICP        string
	Summary    string
	Links      []string
	Friends    []string
	HasArchive bool
	SinglePost bool
}

type Feed struct {
	Parsed   bool
	Name     string
	Authors  []string
	Language string
	Summary  string
}

type personName struct {
	Name string `json:"name" xml:"name"`
}

type rssFeed struct {
	Channel struct {
		Title          string `xml:"title"`
		Description    string `xml:"description"`
		Language       string `xml:"language"`
		Creator        string `xml:"http://purl.org/dc/elements/1.1/ creator"`
		ManagingEditor string `xml:"managingEditor"`
		Author         string `xml:"author"`
		Items          []struct {
			Creator string `xml:"http://purl.org/dc/elements/1.1/ creator"`
			Author  string `xml:"author"`
		} `xml:"item"`
	} `xml:"channel"`
}

type atomFeed struct {
	XMLName  xml.Name   `xml:"feed"`
	Title    string     `xml:"title"`
	Subtitle string     `xml:"subtitle"`
	Language string     `xml:"http://www.w3.org/XML/1998/namespace lang,attr"`
	Author   personName `xml:"author"`
	Entries  []struct {
		Authors []personName `xml:"author"`
	} `xml:"entry"`
}

type jsonFeed struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Items       []struct {
		Authors []personName `json:"authors"`
	} `json:"items"`
}

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

type linkScan struct {
	root        *url.URL
	registrable string
	cache       map[string]string
}

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

func registrableOf(host string) string {
	suffix, icann := publicsuffix.PublicSuffix(host)
	if !icann || suffix == "" || !strings.HasSuffix(host, suffix) {
		return ""
	}
	suffixLabels := strings.Count(suffix, ".") + 1
	labels := strings.Split(host, ".")
	if len(labels) <= suffixLabels {
		return ""
	}
	return strings.Join(labels[len(labels)-suffixLabels-1:], ".")
}

func noiseHost(host string) bool {
	for {
		if noiseHosts[host] {
			return true
		}
		index := strings.Index(host, ".")
		if index < 0 {
			return false
		}
		host = host[index+1:]
	}
}

func HostOf(host string) string {
	if host == "" {
		return ""
	}
	if _, failure := netip.ParseAddr(host); failure == nil {
		return ""
	}
	if len(host) > 253 || !strings.Contains(host, ".") {
		return ""
	}
	for _, suffix := range blockedSuffixes {
		if strings.HasSuffix(host, suffix) || strings.Contains(host, suffix+".") {
			return ""
		}
	}
	for _, suffix := range reservedHosts {
		if strings.HasSuffix(host, suffix) {
			return ""
		}
	}
	if noiseHost(host) {
		return ""
	}
	return host
}

func errorPage(name string) bool {
	return containsAny(strings.ToLower(collapse(name)), errorTitles)
}

func notBlog(site *BlogSite, page Home, result *Result) bool {
	if result.Feed != "" || site.Feed != "" || result.Framework != "" || result.ICP != "" ||
		page.HasArchive || len(result.Friends) > 0 {
		return false
	}
	label := strings.ToLower(collapse(page.Name))
	return hubHosts[registrableOf(site.Host)] || label == "" || label == "首页" || label == "home" ||
		containsAny(label, junkWords)
}

func weakTitle(text string) bool {
	word := collapse(text)
	size := utf8.RuneCountInString(word)
	return size < 2 || size > 40 || strings.ContainsAny(word, "0123456789@©") ||
		strings.Contains(word, "://") || len(strings.Fields(word)) > 6
}

func cleanLang(text string) string {
	word := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(text), "_", "-"))
	if index := strings.IndexAny(word, ",; "); index > 0 {
		word = word[:index]
	}
	if word == "" || len([]rune(word)) > 32 {
		return ""
	}
	for _, part := range strings.Split(word, "-") {
		if part == "" || len(part) > 16 || strings.Trim(part, "abcdefghijklmnopqrstuvwxyz0123456789") != "" {
			return ""
		}
	}
	return word
}

func titleCut(name string) int {
	offset := 0
	for _, field := range strings.Fields(name) {
		position := strings.Index(name[offset:], field) + offset
		offset = position + len(field)
		if position > 0 && utf8.RuneCountInString(field) <= 2 && strings.Trim(field, separatorChars) == "" {
			return position
		}
	}
	return max(strings.IndexAny(name, titleMarks), 0)
}

func tidyTitle(text string) string {
	name := collapse(html.UnescapeString(text))
	if head := strings.TrimSpace(name[:titleCut(name)]); head != "" && utf8.RuneCountInString(head) <= 32 {
		name = head
	}
	name = strings.Trim(name, titleEdges)
	return clip(name, 64)
}

func summaryText(text string) string {
	word := plainText(text)
	lower := strings.ToLower(word)
	if utf8.RuneCountInString(word) < 4 || containsAny(lower, summaryNoise) || summaryBlank[lower] {
		return ""
	}
	return clip(word, 256)
}

func authorName(text string, refs ...string) string {
	word := collapse(text)
	if index := strings.IndexAny(word, ",;，；<（("); index > 0 {
		word = strings.TrimSpace(word[:index])
	}
	word = strings.TrimSpace(strings.TrimPrefix(word, "@"))
	size := utf8.RuneCountInString(word)
	if size < 2 || size > 40 || strings.Contains(word, "://") || len(strings.Fields(word)) > 6 ||
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

func isPerson(value interface{}) bool {
	switch typed := value.(type) {
	case string:
		return strings.Contains(typed, "Person")
	case []interface{}:
		for _, item := range typed {
			if text, ok := item.(string); ok && strings.Contains(text, "Person") {
				return true
			}
		}
	}
	return false
}

func jsonldFields(texts []string) (author, name string) {
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
	for _, raw := range texts {
		if json.Unmarshal([]byte(raw), &document) != nil {
			continue
		}
		if name == "" {
			name = pickFirst(document.Name, document.Headline)
		}
		if author == "" {
			author = parseAuthor(document.Author)
		}
		if author == "" {
			for _, node := range document.Graph {
				if isPerson(node.Type) && node.Name != "" {
					author = node.Name
					break
				}
			}
		}
		if author == "" {
			for _, node := range document.Graph {
				if candidate := parseAuthor(node.Author); candidate != "" {
					author = candidate
					break
				}
			}
		}
		if author != "" && name != "" {
			break
		}
	}
	return author, name
}

func copyrightName(text string) string {
	match := copyrightPattern.FindStringIndex(text)
	if match == nil {
		return ""
	}
	phrase := text[match[1]:]
	lower := strings.ToLower(phrase)
	for _, stop := range copyrightStops {
		if index := strings.Index(lower, stop); index >= 0 {
			phrase, lower = phrase[:index], lower[:index]
		}
	}
	phrase = collapse(strings.Trim(phrase, " \t:：-—、,.。;；"))
	lower = strings.ToLower(phrase)
	if containsAny(lower, copyrightNoise) || weakTitle(phrase) {
		return ""
	}
	return phrase
}

func frameworkName(stated string) string {
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
	if len(kept) == 0 {
		return ""
	}
	name := strings.Join(kept, " ")
	size := utf8.RuneCountInString(name)
	if size < 2 || size > 32 || strings.ContainsAny(name, "0123456789@") ||
		strings.Contains(name, "'s ") || strings.Contains(name, "’s ") {
		return ""
	}
	return name
}

func detectFramework(stated string, body []byte) string {
	lower := strings.ToLower(stated)
	for _, row := range frameworkMarks {
		if strings.Contains(lower, row[0]) {
			return row[2]
		}
	}
	if name := frameworkName(stated); name != "" {
		return name
	}
	lowered := bytes.ToLower(body)
	for _, row := range frameworkMarks {
		if bytes.Contains(lowered, []byte(row[1])) {
			return row[2]
		}
	}
	return ""
}

func scanMeta(node *html.Node, info *metaInfo) {
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
}

func (scan *linkScan) visit(node *html.Node, link, section string, page *Home) {
	if !page.HasArchive {
		page.HasArchive = containsAny(strings.ToLower(link+" "+textOf(node)), archiveHints)
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
	rel := strings.ToLower(attrOf(node, "rel"))
	style := strings.ToLower(attrOf(node, "class") + " " + attrOf(node, "id"))
	entry := textOf(node)
	if section == "nav" || section == "footer" || entry == "" ||
		strings.Contains(rel, "nofollow") || strings.Contains(rel, "sponsored") ||
		strings.Contains(style, "social") || strings.Contains(style, "share") {
		return
	}
	if len(page.Links) < 208 {
		page.Links = append(page.Links, hostname)
	}
	if len(page.Friends) < 16 && registrableOf(hostname) == scan.registrable {
		lower := strings.ToLower(entry + " " + parsed.Path)
		if strings.Contains(rel, "friend") || strings.Contains(rel, "blogroll") || containsAny(lower, friendHints) {
			page.Friends = append(page.Friends, parsed.String())
		}
	}
}

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
				scanMeta(node, info)
			case "html":
				info.htmlLang = cleanLang(attrOf(node, "lang"))
			case "body":
				page.SinglePost = slices.ContainsFunc(strings.Fields(strings.ToLower(attrOf(node, "class"))),
					func(token string) bool { return strings.HasPrefix(token, "single") })
			case "link":
				kind, link := strings.ToLower(attrOf(node, "type")), attrOf(node, "href")
				if page.Feed == "" && link != "" && strings.Contains(strings.ToLower(attrOf(node, "rel")), "alternate") &&
					containsAny(kind, feedTypes) {
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
					if containsAny(style, authorMarks) {
						info.domAuthor = textOf(node)
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
	jsonAuthor, jsonTitle := jsonldFields(jsonlds)
	page.Name = tidyTitle(pickFirst(info.site, info.title, info.ogTitle, info.twitter, info.app, jsonTitle))
	page.Language = pickFirst(info.htmlLang, cleanLang(info.locale), cleanLang(info.content))
	page.Framework = detectFramework(info.generator, body)
	host := normalizeHost(origin)
	accept := func(candidate string) string { return authorName(candidate, host) }
	page.Author = pickFirst(accept(info.author), accept(jsonAuthor), accept(info.domAuthor), accept(info.address))
	if page.Author == "" {
		page.Author = accept(copyrightName(collapse(texts.String())))
	}
	if match := icpPattern.Find(body); match != nil {
		page.ICP = string(match)
	}
	if info.summary != "" {
		page.Summary = summaryText(info.summary)
	}
	return page
}

func unmarshalFeed(body []byte, target any) error {
	if failure := xml.Unmarshal(body, target); failure == nil {
		return nil
	}
	closed := body
	for _, row := range feedClosers {
		index := bytes.LastIndex(body, []byte(row[0]))
		if index > 0 {
			closed = append(append([]byte{}, body[:index+len(row[0])]...), row[1]...)
			break
		}
	}
	return xml.Unmarshal(closed, target)
}

func (feed *Feed) pushAuthor(author string) {
	word := authorName(author)
	if word != "" && !slices.ContainsFunc(feed.Authors,
		func(existing string) bool { return strings.EqualFold(existing, word) }) {
		feed.Authors = append(feed.Authors, word)
	}
}

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
		subscription = Feed{Parsed: true, Name: tidyTitle(channel.Channel.Title), Language: cleanLang(channel.Channel.Language), Summary: summaryText(channel.Channel.Description)}
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
		subscription = Feed{Parsed: true, Name: tidyTitle(document.Title), Language: cleanLang(document.Language), Summary: summaryText(document.Subtitle)}
		subscription.pushAuthor(document.Author.Name)
		for _, entry := range document.Entries {
			for _, person := range entry.Authors {
				subscription.pushAuthor(person.Name)
			}
		}
	}
	return subscription
}

func ParseSitemap(body []byte) []string {
	var document struct {
		URLs []struct {
			Loc string `xml:"loc"`
		} `xml:"url"`
		Sitemaps []struct {
			Loc string `xml:"loc"`
		} `xml:"sitemap"`
	}
	if xml.Unmarshal(body, &document) != nil {
		return nil
	}
	var locations []string
	for _, entry := range append(document.URLs, document.Sitemaps...) {
		if entry.Loc != "" {
			locations = append(locations, entry.Loc)
		}
	}
	return locations
}
