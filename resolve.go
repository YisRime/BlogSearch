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

func vagueName(text string) bool {
	word := collapse(text)
	runes := []rune(word)
	if len(runes) < 2 || len(runes) > 40 {
		return true
	}
	if strings.ContainsAny(word, "0123456789@©") || strings.Contains(word, "://") {
		return true
	}
	return len(strings.Fields(word)) > 6
}

func sameName(text, other string) bool {
	word := strings.ToLower(collapse(text))
	return word != "" && word == strings.ToLower(collapse(other))
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
	ICP       string
	Summary   string
	Links     []string
	Friends   []string
}

type Subscription struct {
	Parsed   bool
	Name     string
	Authors  []string
	Language string
}

var friendPaths = []string{"/links", "/friends"}

var copyrightPattern = regexp.MustCompile(`(?i)(?:©|\(c\)|copyright)\s*(?:\d{4}(?:\s*[-–~至]\s*\d{4})?)?\s*`)

var icpPattern = regexp.MustCompile(`[\p{Han}]?ICP备\d+号|公网安备\d+号`)

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
	if index := firstSeparatorIndex(name); index > 0 {
		if head := strings.TrimSpace(name[:index]); head != "" && len([]rune(head)) <= 32 {
			name = head
		}
	}
	if runes := []rune(name); len(runes) > 64 {
		name = string(runes[:64])
	}
	return name
}

func firstSeparatorIndex(name string) int {
	offset := 0
	for _, field := range strings.Fields(name) {
		position := strings.Index(name[offset:], field)
		if position < 0 {
			break
		}
		position += offset
		offset = position + len(field)
		if position > 0 && len([]rune(field)) <= 2 && strings.Trim(field, "-–—_|｜/·»•・~") == "" {
			return position
		}
	}
	if index := strings.IndexAny(name, "｜·»•・"); index > 0 {
		return index
	}
	return 0
}

func isContact(text string) bool {
	return strings.Contains(text, "@") || strings.Contains(text, "://")
}

func jsonldFields(texts []string) (author, name string) {
	var document struct {
		Name     string `json:"name"`
		Headline string `json:"headline"`
		Graph    []struct {
			Type interface{} `json:"@type"`
			Name string      `json:"name"`
		} `json:"@graph"`
	}
	for _, raw := range texts {
		if json.Unmarshal([]byte(raw), &document) != nil {
			continue
		}
		if name == "" {
			name = firstNonEmpty(document.Name, document.Headline)
		}
		if author == "" {
			for _, node := range document.Graph {
				if isPersonType(node.Type) && node.Name != "" {
					author = node.Name
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
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
	var titleTag, stated, generator, htmlLang, locale string
	var ogSite, ogTitle, twitterTitle, applicationName, description, address string
	originRegistrable := registrableOf(normalizeHost(origin))
	hostCache := map[string]string{}
	var texts strings.Builder
	var jsonlds []string
	length := 0
	var walk func(*html.Node, string)
	walk = func(node *html.Node, section string) {
		if node.Type == html.ElementNode {
			tag := strings.ToLower(node.Data)
			switch tag {
			case "nav":
				section = "nav"
			case "footer":
				section = "footer"
			}
			switch tag {
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
				titleTag = nodeText(node)
			case "address":
				if address == "" {
					address = nodeText(node)
				}
			case "meta":
				name := strings.ToLower(attribute(node, "name"))
				property := strings.ToLower(attribute(node, "property"))
				content := attribute(node, "content")
				switch {
				case name == "generator" && generator == "":
					generator = content
				case name == "author" && stated == "":
					stated = content
				case (name == "article:author" || property == "article:author") && stated == "":
					stated = content
				case name == "twitter:creator" && stated == "":
					stated = content
				case (name == "og:site_name" || property == "og:site_name") && ogSite == "":
					ogSite = content
				case (name == "og:title" || property == "og:title") && ogTitle == "":
					ogTitle = content
				case name == "twitter:title" && twitterTitle == "":
					twitterTitle = content
				case name == "application-name" && applicationName == "":
					applicationName = content
				case (name == "description" || name == "og:description" || property == "og:description") && description == "":
					description = content
				case (name == "og:locale" || property == "og:locale") && locale == "":
					locale = content
				}
			case "html":
				htmlLang = normalizeLang(attribute(node, "lang"))
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
					rel := strings.ToLower(attribute(node, "rel"))
					style := strings.ToLower(attribute(node, "class") + " " + attribute(node, "id"))
					entry := nodeText(node)
					blocked := section == "nav" || section == "footer" ||
						strings.Contains(rel, "nofollow") || strings.Contains(rel, "sponsored") ||
						strings.Contains(style, "social") || strings.Contains(style, "share") || entry == ""
					if blocked {
						break
					}
					if len(page.Links) < 208 {
						page.Links = append(page.Links, hostname)
					}
					if len(page.Friends) < 16 && registrableOf(hostname) == originRegistrable {
						lower := strings.ToLower(entry + " " + parsed.Path)
						if strings.Contains(rel, "friend") || strings.Contains(rel, "blogroll") {
							page.Friends = append(page.Friends, parsed.String())
						} else {
							for _, word := range []string{"友链", "友情链接", "链接", "friend"} {
								if strings.Contains(lower, word) {
									page.Friends = append(page.Friends, parsed.String())
									break
								}
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
			walk(child, section)
		}
	}
	walk(document, "")
	jsonAuthor, jsonTitle := jsonldFields(jsonlds)
	page.Name = cleanTitle(firstNonEmpty(ogSite, titleTag, ogTitle, twitterTitle, applicationName, jsonTitle))
	page.Language = htmlLang
	if page.Language == "" {
		page.Language = normalizeLang(locale)
	}
	page.Framework = detectFramework(generator, body)
	host := normalizeHost(origin)
	accept := func(candidate string) string {
		if word := collapse(candidate); word != "" && !vagueName(word) && !isContact(word) &&
			!sameName(word, host) && !sameName(word, page.Name) {
			return word
		}
		return ""
	}
	author := accept(stated)
	if author == "" {
		author = accept(jsonAuthor)
	}
	if author == "" {
		author = accept(address)
	}
	if author == "" {
		author = accept(copyrightName(collapse(texts.String())))
	}
	page.Author = author
	if match := icpPattern.FindString(string(body)); match != "" {
		page.ICP = match
	}
	if description != "" {
		page.Summary = truncateRunes(collapse(description), 256)
	}
	return page
}

func copyrightName(text string) string {
	match := copyrightPattern.FindStringIndex(text)
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
	for _, noise := range []string{"·", "•", "|", "｜", "/", "访客", "评论", "登录", "搜索", "rss", "design", "theme"} {
		if strings.Contains(lower, noise) {
			return ""
		}
	}
	if vagueName(phrase) {
		return ""
	}
	return phrase
}

func softMissing(title string) bool {
	word := strings.ToLower(collapse(title))
	for _, mark := range []string{"404", "not found", "页面不存在", "找不到页面", "access denied",
		"welcome to nginx", "index of /", "default web site page", "站点未找到"} {
		if strings.Contains(word, mark) {
			return true
		}
	}
	return false
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
	if len([]rune(name)) < 2 || len([]rune(name)) > 32 || strings.ContainsAny(name, "0123456789@") {
		return ""
	}
	if strings.Contains(name, "'s ") || strings.Contains(name, "’s ") {
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
	}
	if stated != "" {
		lower := strings.ToLower(stated)
		for _, row := range rows {
			if strings.Contains(lower, row[0]) {
				return row[2]
			}
		}
		if name := frameworkName(stated); name != "" {
			return name
		}
	}
	lower := bytes.ToLower(body)
	for _, row := range rows {
		if bytes.Contains(lower, []byte(row[1])) {
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
	if index := bytes.IndexByte(body[:limit], '>'); index >= 0 {
		return body[:index+1]
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
	if vagueName(word) || isContact(word) {
		return
	}
	for _, existing := range subscription.Authors {
		if strings.EqualFold(existing, word) {
			return
		}
	}
	subscription.Authors = append(subscription.Authors, word)
}
