package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"golang.org/x/net/html"
	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
)

var placeholderSet = func() map[string]bool {
	rows := []string{
		"admin",
		"administrator",
		"站长",
		"管理员",
		"作者",
		"匿名",
		"guest",
		"your name",
		"yourname",
		"your-site",
		"your_site",
		"site author",
		"the author",
		"example",
		"sample",
		"john doe",
		"jane doe",
		"joel gath",
		"hexo",
		"hugo",
		"jekyll",
		"author",
		"你的名字",
		"示例作者",
		"占位",
		"未命名",
		"name",
		"username",
		"user",
		"email",
		"foo bar",
		"foobar",
		"baz",
		"lorem",
		"ipsum",
		"tbd",
		"todo",
		"todo author",
		"null",
		"undefined",
	}
	set := make(map[string]bool, len(rows))
	for _, raw := range rows {
		set[strings.ToLower(strings.TrimSpace(raw))] = true
	}
	return set
}()

func isPlaceholder(text string) bool {
	word := strings.ToLower(strings.TrimSpace(text))
	if word == "" {
		return true
	}
	return placeholderSet[word] || placeholderSet[strings.ReplaceAll(word, "_", " ")]
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

func Judge(raw string) string {
	host := normalizeHost(raw)
	if host == "" {
		return ""
	}
	if _, failure := netip.ParseAddr(host); failure == nil {
		return ""
	}
	registrable := registrableOf(host)
	if registrable == "" {
		return ""
	}
	if host != registrable && host != "blog."+registrable {
		return ""
	}
	return host
}

type Page struct {
	Name      string
	Author    string
	Framework string
	Language  string
	Feed      string
	Links     []string
	Friends   []string
}

type Subscription struct {
	Parsed   bool
	Name     string
	Authors  []string
	Language string
}

var friendPaths = []string{"/links", "/friends", "/link"}

var copyrightPattern = regexp.MustCompile(`(?i)(?:©|\(c\)|copyright)\s*(?:\d{4}(?:\s*[-–~至]\s*\d{4})?)?\s*`)

func collapse(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func normalizeLang(text string) string {
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

func cleanTitle(text string) string {
	name := collapse(text)
	for _, cut := range []string{" - ", " _ ", " | ", " ｜ ", " · ", " — ", " – ", " » "} {
		if index := strings.Index(name, cut); index > 0 {
			if head := strings.TrimSpace(name[:index]); head != "" && len([]rune(head)) <= 32 {
				name = head
				break
			}
		}
	}
	if runes := []rune(name); len(runes) > 64 {
		name = string(runes[:64])
	}
	return name
}

func isContact(text string) bool {
	return strings.Contains(text, "@") || strings.Contains(text, "://")
}

func jsonldAuthor(texts []string) string {
	var person struct {
		Graph []struct {
			Type interface{} `json:"@type"`
			Name string      `json:"name"`
		} `json:"@graph"`
	}
	for _, raw := range texts {
		if json.Unmarshal([]byte(raw), &person) != nil {
			continue
		}
		for _, node := range person.Graph {
			isPerson := false
			switch value := node.Type.(type) {
			case string:
				isPerson = strings.Contains(value, "Person")
			case []interface{}:
				for _, item := range value {
					if text, ok := item.(string); ok && strings.Contains(text, "Person") {
						isPerson = true
					}
				}
			}
			if isPerson && node.Name != "" {
				return node.Name
			}
		}
	}
	return ""
}

func attribute(node *html.Node, key string) string {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, key) {
			return attr.Val
		}
	}
	return ""
}

func absoluteURL(link, origin string) *url.URL {
	root, failure := url.Parse(origin)
	if failure != nil {
		return nil
	}
	parsed, failure := url.Parse(link)
	if failure != nil {
		return nil
	}
	if parsed.IsAbs() {
		return parsed
	}
	return root.ResolveReference(parsed)
}

func nodeText(node *html.Node) string {
	var buffer bytes.Buffer
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.TextNode {
			buffer.WriteString(child.Data)
			buffer.WriteByte(' ')
		}
	}
	return collapse(buffer.String())
}

func ParsePage(body []byte, origin string) Page {
	var page Page
	document, failure := html.Parse(bytes.NewReader(body))
	if failure != nil {
		return page
	}
	var title, stated, generator, language string
	originRegistrable := registrableOf(normalizeHost(origin))
	hostCache := map[string]string{}
	var texts strings.Builder
	var jsonlds []string
	length := 0
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode {
			switch strings.ToLower(node.Data) {
			case "script":
				if strings.Contains(strings.ToLower(attribute(node, "type")), "ld+json") {
					if text := nodeText(node); text != "" {
						jsonlds = append(jsonlds, text)
					}
				}
				return
			case "style", "noscript", "svg":
				return
			case "title":
				title = nodeText(node)
			case "meta":
				name := strings.ToLower(attribute(node, "name"))
				if name == "generator" && generator == "" {
					generator = attribute(node, "content")
				}
				if name == "author" && stated == "" {
					stated = attribute(node, "content")
				}
			case "html":
				language = normalizeLang(attribute(node, "lang"))
			case "link":
				if page.Feed == "" && strings.Contains(strings.ToLower(attribute(node, "rel")), "alternate") {
					kind, link := strings.ToLower(attribute(node, "type")), attribute(node, "href")
					if link != "" && (strings.Contains(kind, "atom") || strings.Contains(kind, "rss") ||
						strings.Contains(kind, "feed+json")) {
						if parsed := absoluteURL(link, origin); parsed != nil {
							page.Feed = parsed.String()
						}
					}
				}
			case "a":
				link := attribute(node, "href")
				if link == "" {
					return
				}
				parsed := absoluteURL(link, origin)
				if parsed != nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
					hostname, ok := hostCache[parsed.Host]
					if !ok {
						hostname = normalizeHost(parsed.Host)
						hostCache[parsed.Host] = hostname
					}
					if len(page.Links) < 208 {
						page.Links = append(page.Links, hostname)
					}
					if len(page.Friends) < 16 && registrableOf(hostname) == originRegistrable {
						entry := strings.ToLower(nodeText(node) + " " + parsed.Path)
						for _, word := range []string{"友链", "友情链接", "链接", "links", "friend", "blogroll"} {
							if strings.Contains(entry, word) {
								page.Friends = append(page.Friends, parsed.String())
								break
							}
						}
					}
				}
			}
		}
		if node.Type == html.TextNode && length < 4096 {
			written, _ := texts.WriteString(node.Data)
			length += written
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(document)
	page.Name = cleanTitle(title)
	page.Language = language
	page.Framework = detectFramework(generator, body)
	author := strings.TrimSpace(stated)
	if isPlaceholder(author) || isContact(author) {
		author = ""
	}
	if author == "" {
		if name := jsonldAuthor(jsonlds); name != "" && !isContact(name) {
			author = name
		}
	}
	if author == "" {
		author = copyrightName(collapse(texts.String()))
	}
	page.Author = author
	return page
}

func copyrightName(text string) string {
	match := copyrightPattern.FindStringIndex(text)
	if match == nil {
		return ""
	}
	phrase := text[match[1]:]
	for _, stop := range []string{"保留", "All rights", "all rights", "。", "备案", "ICP"} {
		if index := strings.Index(phrase, stop); index >= 0 {
			phrase = phrase[:index]
		}
	}
	phrase = collapse(strings.Trim(phrase, " \t:：-—、,.。;；"))
	for _, noise := range []string{"·", "•", "|", "｜", "/", "访客", "评论", "登录", "搜索", "RSS", "rss", "Design", "Theme"} {
		if strings.Contains(phrase, noise) {
			return ""
		}
	}
	runes := []rune(phrase)
	if len(runes) < 2 || len(runes) > 48 || strings.ContainsAny(phrase, "0123456789") ||
		len(strings.Fields(phrase)) > 16 || isPlaceholder(phrase) {
		return ""
	}
	return phrase
}

func generatorName(stated string) string {
	word := collapse(stated)
	for _, noise := range []string{"(", ")", "seo", "plugin", "rocket", "cache", "minify", "compress"} {
		if strings.Contains(strings.ToLower(word), noise) {
			return ""
		}
	}
	fields := strings.Fields(word)
	for len(fields) > 1 && strings.ContainsAny(fields[len(fields)-1], "0123456789") {
		fields = fields[:len(fields)-1]
	}
	word = collapse(strings.Join(fields, " "))
	if word == "" || len([]rune(word)) > 32 {
		return ""
	}
	if strings.Contains(word, "'s ") || strings.Contains(word, "’s ") {
		return ""
	}
	return word
}

func softMissing(title string) bool {
	word := collapse(title)
	for _, mark := range []string{"404", "Not Found", "not found", "页面不存在", "找不到页面", "Access denied"} {
		if strings.Contains(word, mark) {
			return true
		}
	}
	return false
}

func lowerByte(value byte) byte {
	if value >= 'A' && value <= 'Z' {
		return value + ('a' - 'A')
	}
	return value
}

func containsFold(haystack []byte, needle string) bool {
	if len(needle) == 0 || len(haystack) < len(needle) {
		return false
	}
	first := needle[0]
	last := len(haystack) - len(needle)
	for start := 0; start <= last; start++ {
		if lowerByte(haystack[start]) != first {
			continue
		}
		match := true
		for offset := 1; offset < len(needle); offset++ {
			if lowerByte(haystack[start+offset]) != needle[offset] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func detectFramework(stated string, body []byte) string {
	rows := [][3]string{
		{"wordpress", "wp-content", "WordPress"},
		{"hexo", "hexo-theme", "Hexo"},
		{"hugo", "", "Hugo"},
		{"jekyll", "", "Jekyll"},
		{"gridea", "", "Gridea"},
		{"vitepress", "", "VitePress"},
		{"astro", "", "Astro"},
		{"typecho", "", "Typecho"},
	}
	if stated != "" {
		lower := strings.ToLower(stated)
		for _, row := range rows {
			if strings.Contains(lower, row[0]) {
				return row[2]
			}
		}
		if name := generatorName(stated); name != "" {
			return name
		}
	}
	for _, row := range rows {
		fingerprint := row[1]
		if fingerprint == "" {
			fingerprint = row[0]
		}
		if containsFold(body, fingerprint) {
			return row[2]
		}
	}
	return ""
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

type RssDoc struct {
	Channel struct {
		Title    string `xml:"title"`
		Language string `xml:"language"`
		Items    []struct {
			Creator string `xml:"http://purl.org/dc/elements/1.1/ creator"`
			Author  string `xml:"author"`
		} `xml:"item"`
	} `xml:"channel"`
}

type AtomDoc struct {
	XMLName  xml.Name `xml:"feed"`
	Title    string   `xml:"title"`
	Language string   `xml:"http://www.w3.org/XML/1998/namespace lang,attr"`
	Entries  []struct {
		Authors []struct {
			Name string `xml:"name"`
		} `xml:"author"`
	} `xml:"entry"`
}

type JsonDoc struct {
	Title string `json:"title"`
	Items []struct {
		Authors []struct {
			Name string `json:"name"`
		} `json:"authors"`
	} `json:"items"`
}

func feedHead(body []byte) []byte {
	limit := len(body)
	if limit > 512 {
		limit = 512
	}
	for index := 0; index < limit; index++ {
		if body[index] == '>' {
			return body[:index+1]
		}
	}
	return body[:limit]
}

func closeTruncated(body []byte) []byte {
	for _, row := range [][2]string{{"</item>", "</channel></rss>"}, {"</entry>", "</feed>"}} {
		index := bytes.LastIndex(body, []byte(row[0]))
		if index > 0 {
			return append(append([]byte{}, body[:index+len(row[0])]...), row[1]...)
		}
	}
	return body
}

func decodeFeed(body []byte, target any) error {
	if failure := xml.Unmarshal(body, target); failure == nil {
		return nil
	}
	return xml.Unmarshal(closeTruncated(body), target)
}

func ParseSubscription(body []byte) Subscription {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return Subscription{}
	}
	var subscription Subscription
	switch {
	case bytes.HasPrefix(trimmed, []byte("{")):
		var decoded JsonDoc
		if json.Unmarshal(trimmed, &decoded) != nil {
			return Subscription{}
		}
		subscription = Subscription{Parsed: true, Name: cleanTitle(decoded.Title)}
		for _, item := range decoded.Items {
			for _, person := range item.Authors {
				subscription.addAuthor(person.Name)
			}
		}
	case bytes.Contains(feedHead(trimmed), []byte("<rss")):
		var channel RssDoc
		if decodeFeed(trimmed, &channel) != nil {
			return Subscription{}
		}
		subscription = Subscription{Parsed: true, Name: cleanTitle(channel.Channel.Title), Language: normalizeLang(channel.Channel.Language)}
		for _, item := range channel.Channel.Items {
			subscription.addAuthor(item.Creator)
			subscription.addAuthor(item.Author)
		}
	default:
		var document AtomDoc
		if decodeFeed(trimmed, &document) != nil {
			return Subscription{}
		}
		subscription = Subscription{Parsed: true, Name: cleanTitle(document.Title), Language: normalizeLang(document.Language)}
		for _, entry := range document.Entries {
			for _, person := range entry.Authors {
				subscription.addAuthor(person.Name)
			}
		}
	}
	return subscription
}

func (subscription *Subscription) addAuthor(author string) {
	word := collapse(author)
	if isPlaceholder(word) || isContact(word) {
		return
	}
	for _, existing := range subscription.Authors {
		if strings.EqualFold(existing, word) {
			return
		}
	}
	subscription.Authors = append(subscription.Authors, word)
}
