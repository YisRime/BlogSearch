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
}

type personName struct {
	Name string `json:"name" xml:"name"`
}

type rssFeed struct {
	Channel struct {
		Title          string `xml:"title"`
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
	Language string     `xml:"http://www.w3.org/XML/1998/namespace lang,attr"`
	Author   personName `xml:"author"`
	Entries  []struct {
		Authors []personName `xml:"author"`
	} `xml:"entry"`
}

type jsonFeed struct {
	Title string `json:"title"`
	Items []struct {
		Authors []personName `json:"authors"`
	} `json:"items"`
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

var reservedHosts = []string{".local", ".localhost", ".test", ".invalid", ".example", ".internal", ".home", ".lan", ".onion"}

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

var errorTitles = []string{"404", "not found", "页面不存在", "找不到页面", "access denied",
	"welcome to nginx", "index of /", "default web site page", "站点未找到"}

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
	for _, suffix := range []string{".gov", ".mil"} {
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
	title := strings.ToLower(collapse(name))
	return slices.ContainsFunc(errorTitles, func(mark string) bool { return strings.Contains(title, mark) })
}

func notBlog(site *BlogSite, page Home, result *Result) bool {
	if result.Feed != "" || site.Feed != "" || result.Framework != "" || result.ICP != "" ||
		page.HasArchive || len(result.Friends) > 0 {
		return false
	}
	label := strings.ToLower(collapse(page.Name))
	return hubHosts[registrableOf(site.Host)] || label == "" || label == "首页" || label == "home" ||
		slices.ContainsFunc([]string{"index of", "网站维护中", "建站中", "维护中", "coming soon",
			"域名出售", "域名停放", "网址导航", "搜索引擎"}, func(word string) bool {
			return strings.Contains(label, word)
		})
}

func weakTitle(text string) bool {
	word := collapse(text)
	size := utf8.RuneCountInString(word)
	return size < 2 || size > 40 || strings.ContainsAny(word, "0123456789@©") ||
		strings.Contains(word, "://") || len(strings.Fields(word)) > 6
}

func credible(text string, refs ...string) bool {
	word := collapse(text)
	if word == "" || weakTitle(word) {
		return false
	}
	lower := strings.ToLower(word)
	return !slices.ContainsFunc(refs, func(ref string) bool { return lower == strings.ToLower(collapse(ref)) })
}

func cleanLang(text string) string {
	word := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(text), "_", "-"))
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

func firstSeparatorIndex(name string) int {
	offset := 0
	for _, field := range strings.Fields(name) {
		position := strings.Index(name[offset:], field) + offset
		offset = position + len(field)
		if position > 0 && utf8.RuneCountInString(field) <= 2 && strings.Trim(field, "-–—_|｜/·»•・~") == "" {
			return position
		}
	}
	return max(strings.IndexAny(name, "｜·»•・"), 0)
}

func tidyTitle(text string) string {
	name := collapse(text)
	if head := strings.TrimSpace(name[:firstSeparatorIndex(name)]); head != "" && utf8.RuneCountInString(head) <= 32 {
		name = head
	}
	return clip(name, 64)
}

func authorFromJSON(raw json.RawMessage) string {
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

func isPersonType(value interface{}) bool {
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
			author = authorFromJSON(document.Author)
		}
		if author == "" {
			for _, node := range document.Graph {
				if isPersonType(node.Type) && node.Name != "" {
					author = node.Name
					break
				}
			}
		}
		if author == "" {
			for _, node := range document.Graph {
				if candidate := authorFromJSON(node.Author); candidate != "" {
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
	match := regexp.MustCompile(`(?i)(?:©|\(c\)|copyright)\s*(?:\d{4}(?:\s*[-–~至]\s*\d{4})?)?\s*`).FindStringIndex(text)
	if match == nil {
		return ""
	}
	phrase := text[match[1]:]
	lower := strings.ToLower(phrase)
	for _, stop := range []string{"保留", "all rights", "。", "备案", "icp"} {
		if index := strings.Index(lower, stop); index >= 0 {
			phrase, lower = phrase[:index], lower[:index]
		}
	}
	phrase = collapse(strings.Trim(phrase, " \t:：-—、,.。;；"))
	lower = strings.ToLower(phrase)
	if slices.ContainsFunc([]string{"·", "•", "|", "｜", "/", "访客", "评论", "登录", "搜索", "rss", "design", "theme"},
		func(noise string) bool { return strings.Contains(lower, noise) }) || weakTitle(phrase) {
		return ""
	}
	return phrase
}

func frameworkName(stated string) string {
	fields := strings.Fields(strings.Trim(collapse(stated), ",.;: "))
	for len(fields) > 1 && strings.ContainsAny(fields[len(fields)-1], "0123456789") {
		fields = fields[:len(fields)-1]
	}
	if len(fields) != 1 {
		return ""
	}
	name := fields[0]
	size := utf8.RuneCountInString(name)
	if size < 2 || size > 32 || strings.ContainsAny(name, "0123456789@") ||
		strings.Contains(name, "'s ") || strings.Contains(name, "’s ") {
		return ""
	}
	return name
}

func detectFramework(stated string, body []byte) string {
	rows := [][3]string{
		{"wordpress", "wp-content", "WordPress"},
		{"hexo", "hexo-theme", "Hexo"},
		{"typecho", "/usr/themes/", "Typecho"},
		{"astro", "/_astro/", "Astro"},
		{"next.js", "_next/", "Next.js"},
		{"gatsby", "___gatsby", "Gatsby"},
		{"ghost", "/assets/built/", "Ghost"},
	}
	lower := strings.ToLower(stated)
	for _, row := range rows {
		if strings.Contains(lower, row[0]) {
			return row[2]
		}
	}
	if name := frameworkName(stated); name != "" {
		return name
	}
	lowered := bytes.ToLower(body)
	for _, row := range rows {
		if bytes.Contains(lowered, []byte(row[1])) {
			return row[2]
		}
	}
	return ""
}

func readPage(body []byte, origin string) Home {
	var page Home
	document, failure := html.Parse(bytes.NewReader(body))
	if failure != nil {
		return page
	}
	var titleTag, stated, generator, htmlLang, locale, contentLang, domAuthor string
	var ogSite, ogTitle, twitterTitle, applicationName, description, address string
	root, _ := url.Parse(origin)
	originRegistrable := registrableOf(normalizeHost(origin))
	hostCache := map[string]string{}
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
				titleTag = textOf(node)
			case "address":
				if address == "" {
					address = textOf(node)
				}
			case "meta":
				name, property := strings.ToLower(attrOf(node, "name")), strings.ToLower(attrOf(node, "property"))
				httpequiv, content := strings.ToLower(attrOf(node, "http-equiv")), attrOf(node, "content")
				switch {
				case name == "generator" && generator == "":
					generator = content
				case stated == "" && (name == "author" || name == "article:author" || property == "article:author" || name == "twitter:creator"):
					stated = content
				case contentLang == "" && (name == "language" || httpequiv == "content-language"):
					contentLang = content
				case ogSite == "" && (name == "og:site_name" || property == "og:site_name"):
					ogSite = content
				case ogTitle == "" && (name == "og:title" || property == "og:title"):
					ogTitle = content
				case twitterTitle == "" && name == "twitter:title":
					twitterTitle = content
				case applicationName == "" && name == "application-name":
					applicationName = content
				case description == "" && (name == "description" || name == "og:description" || property == "og:description"):
					description = content
				case locale == "" && (name == "og:locale" || property == "og:locale"):
					locale = content
				}
			case "html":
				htmlLang = cleanLang(attrOf(node, "lang"))
			case "body":
				page.SinglePost = slices.ContainsFunc(strings.Fields(strings.ToLower(attrOf(node, "class"))),
					func(token string) bool { return strings.HasPrefix(token, "single") })
			case "link":
				kind, link := strings.ToLower(attrOf(node, "type")), attrOf(node, "href")
				if page.Feed == "" && link != "" && strings.Contains(strings.ToLower(attrOf(node, "rel")), "alternate") &&
					slices.ContainsFunc([]string{"atom", "rss", "feed+json"},
						func(mark string) bool { return strings.Contains(kind, mark) }) {
					if parsed := resolveLink(link, root); parsed != nil {
						page.Feed = parsed.String()
					}
				}
			case "a":
				link := attrOf(node, "href")
				if link == "" {
					return
				}
				if !page.HasArchive {
					mark := strings.ToLower(link + " " + textOf(node))
					page.HasArchive = slices.ContainsFunc([]string{"archive", "归档", "分页", "/page/", "older", "newer"},
						func(hint string) bool { return strings.Contains(mark, hint) })
				}
				parsed := resolveLink(link, root)
				if parsed != nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
					hostname, ok := hostCache[parsed.Host]
					if !ok {
						hostname = normalizeHost(parsed.Host)
						hostCache[parsed.Host] = hostname
					}
					rel := strings.ToLower(attrOf(node, "rel"))
					style := strings.ToLower(attrOf(node, "class") + " " + attrOf(node, "id"))
					entry := textOf(node)
					if section == "nav" || section == "footer" || entry == "" ||
						strings.Contains(rel, "nofollow") || strings.Contains(rel, "sponsored") ||
						strings.Contains(style, "social") || strings.Contains(style, "share") {
						break
					}
					if len(page.Links) < 208 {
						page.Links = append(page.Links, hostname)
					}
					if len(page.Friends) < 16 && registrableOf(hostname) == originRegistrable {
						lower := strings.ToLower(entry + " " + parsed.Path)
						if strings.Contains(rel, "friend") || strings.Contains(rel, "blogroll") ||
							slices.ContainsFunc([]string{"友链", "友情链接", "链接", "friend"},
								func(word string) bool { return strings.Contains(lower, word) }) {
							page.Friends = append(page.Friends, parsed.String())
						}
					}
				}
			default:
				if domAuthor == "" && tag != "article" && tag != "h1" && tag != "h2" {
					style := strings.ToLower(attrOf(node, "class") + " " + attrOf(node, "id"))
					if slices.ContainsFunc([]string{"author", "byline", "vcard"},
						func(mark string) bool { return strings.Contains(style, mark) }) {
						domAuthor = pickFirst(domAuthor, textOf(node))
					}
				}
			}
		}
		if node.Type == html.TextNode && length < 4096 {
			written, _ := texts.WriteString(node.Data)
			length += written
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child, section)
		}
	}
	walk(document, "")
	jsonAuthor, jsonTitle := jsonldFields(jsonlds)
	page.Name = tidyTitle(pickFirst(ogSite, titleTag, ogTitle, twitterTitle, applicationName, jsonTitle))
	page.Language = pickFirst(htmlLang, cleanLang(locale), cleanLang(contentLang))
	page.Framework = detectFramework(generator, body)
	host := normalizeHost(origin)
	accept := func(candidate string) string {
		if word := collapse(candidate); credible(word, host, page.Name) {
			return word
		}
		return ""
	}
	page.Author = pickFirst(accept(stated), accept(jsonAuthor), accept(domAuthor), accept(address))
	if page.Author == "" {
		page.Author = accept(copyrightName(collapse(texts.String())))
	}
	if match := regexp.MustCompile(`[\p{Han}]?ICP备\d+号|公网安备\d+号`).Find(body); match != nil {
		page.ICP = string(match)
	}
	if description != "" {
		page.Summary = clip(collapse(description), 256)
	}
	return page
}

func unmarshalFeed(body []byte, target any) error {
	if failure := xml.Unmarshal(body, target); failure == nil {
		return nil
	}
	closed := body
	for _, row := range [][2]string{{"</item>", "</channel></rss>"}, {"</entry>", "</feed>"}} {
		index := bytes.LastIndex(body, []byte(row[0]))
		if index > 0 {
			closed = append(append([]byte{}, body[:index+len(row[0])]...), row[1]...)
			break
		}
	}
	return xml.Unmarshal(closed, target)
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

func (feed *Feed) pushAuthor(author string) {
	word := collapse(author)
	if credible(word) && !slices.ContainsFunc(feed.Authors,
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
		subscription = Feed{Parsed: true, Name: tidyTitle(decoded.Title)}
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
		subscription = Feed{Parsed: true, Name: tidyTitle(channel.Channel.Title), Language: cleanLang(channel.Channel.Language)}
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
		subscription = Feed{Parsed: true, Name: tidyTitle(document.Title), Language: cleanLang(document.Language)}
		subscription.pushAuthor(document.Author.Name)
		for _, entry := range document.Entries {
			for _, person := range entry.Authors {
				subscription.pushAuthor(person.Name)
			}
		}
	}
	return subscription
}
