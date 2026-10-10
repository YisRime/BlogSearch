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
	"slices"
	"strings"
	"unicode/utf8"
)

func truncateRunes(text string, limit int) string {
	if utf8.RuneCountInString(text) > limit {
		return string([]rune(text)[:limit])
	}
	return text
}

func vagueName(text string) bool {
	word := collapse(text)
	size := utf8.RuneCountInString(word)
	return size < 2 || size > 40 || strings.ContainsAny(word, "0123456789@©") ||
		strings.Contains(word, "://") || len(strings.Fields(word)) > 6
}

func plausible(text string, refs ...string) bool {
	word := collapse(text)
	if word == "" || vagueName(word) {
		return false
	}
	lower := strings.ToLower(word)
	return !slices.ContainsFunc(refs, func(ref string) bool { return lower == strings.ToLower(collapse(ref)) })
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

func HostOf(raw string) string {
	host := normalizeHost(raw)
	if host == "" {
		return ""
	}
	if _, failure := netip.ParseAddr(host); failure == nil {
		return ""
	}
	for _, suffix := range []string{".gov", ".mil"} {
		if strings.HasSuffix(host, suffix) || strings.Contains(host, suffix+".") {
			return ""
		}
	}
	return host
}

func Judge(raw string) string {
	host := HostOf(raw)
	if host == "" {
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

type Subscription struct {
	Parsed   bool
	Name     string
	Authors  []string
	Language string
}

type named struct {
	Name string `json:"name" xml:"name"`
}

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
	if head := strings.TrimSpace(name[:firstSeparatorIndex(name)]); head != "" && utf8.RuneCountInString(head) <= 32 {
		name = head
	}
	return truncateRunes(name, 64)
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

func jsonAuthorName(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil && text != "" {
		return text
	}
	var one named
	if json.Unmarshal(raw, &one) == nil && one.Name != "" {
		return one.Name
	}
	var many []named
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
			name = firstNonEmpty(document.Name, document.Headline)
		}
		if author == "" {
			author = jsonAuthorName(document.Author)
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
				if candidate := jsonAuthorName(node.Author); candidate != "" {
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

func attribute(node *html.Node, key string) string {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, key) {
			return attr.Val
		}
	}
	return ""
}

func absoluteURL(link string, root *url.URL) *url.URL {
	if root == nil {
		return nil
	}
	parsed, failure := url.Parse(strings.TrimSpace(link))
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
				name, property := strings.ToLower(attribute(node, "name")), strings.ToLower(attribute(node, "property"))
				httpequiv, content := strings.ToLower(attribute(node, "http-equiv")), attribute(node, "content")
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
				htmlLang = normalizeLang(attribute(node, "lang"))
			case "body":
				page.SinglePost = slices.ContainsFunc(strings.Fields(strings.ToLower(attribute(node, "class"))),
					func(token string) bool { return strings.HasPrefix(token, "single") })
			case "link":
				kind, link := strings.ToLower(attribute(node, "type")), attribute(node, "href")
				if page.Feed == "" && link != "" && strings.Contains(strings.ToLower(attribute(node, "rel")), "alternate") &&
					slices.ContainsFunc([]string{"atom", "rss", "feed+json"},
						func(mark string) bool { return strings.Contains(kind, mark) }) {
					if parsed := absoluteURL(link, root); parsed != nil {
						page.Feed = parsed.String()
					}
				}
			case "a":
				link := attribute(node, "href")
				if link == "" {
					return
				}
				if !page.HasArchive {
					mark := strings.ToLower(link + " " + nodeText(node))
					page.HasArchive = slices.ContainsFunc([]string{"archive", "归档", "分页", "/page/", "older", "newer"},
						func(hint string) bool { return strings.Contains(mark, hint) })
				}
				parsed := absoluteURL(link, root)
				if parsed != nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
					hostname, ok := hostCache[parsed.Host]
					if !ok {
						hostname = normalizeHost(parsed.Host)
						hostCache[parsed.Host] = hostname
					}
					rel := strings.ToLower(attribute(node, "rel"))
					style := strings.ToLower(attribute(node, "class") + " " + attribute(node, "id"))
					entry := nodeText(node)
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
					style := strings.ToLower(attribute(node, "class") + " " + attribute(node, "id"))
					if slices.ContainsFunc([]string{"author", "byline", "vcard"},
						func(mark string) bool { return strings.Contains(style, mark) }) {
						domAuthor = firstNonEmpty(domAuthor, nodeText(node))
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
	page.Language = firstNonEmpty(htmlLang, normalizeLang(locale), normalizeLang(contentLang))
	page.Framework = detectFramework(generator, body)
	host := normalizeHost(origin)
	accept := func(candidate string) string {
		if word := collapse(candidate); plausible(word, host, page.Name) {
			return word
		}
		return ""
	}
	page.Author = firstNonEmpty(accept(stated), accept(jsonAuthor), accept(domAuthor), accept(address))
	if page.Author == "" {
		page.Author = accept(copyrightName(collapse(texts.String())))
	}
	if match := regexp.MustCompile(`[\p{Han}]?ICP备\d+号|公网安备\d+号`).Find(body); match != nil {
		page.ICP = string(match)
	}
	if description != "" {
		page.Summary = truncateRunes(collapse(description), 256)
	}
	return page
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
		func(noise string) bool { return strings.Contains(lower, noise) }) || vagueName(phrase) {
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

type AtomDoc struct {
	XMLName  xml.Name `xml:"feed"`
	Title    string   `xml:"title"`
	Language string   `xml:"http://www.w3.org/XML/1998/namespace lang,attr"`
	Author   named    `xml:"author"`
	Entries  []struct {
		Authors []named `xml:"author"`
	} `xml:"entry"`
}

type JsonDoc struct {
	Title string `json:"title"`
	Items []struct {
		Authors []named `json:"authors"`
	} `json:"items"`
}

func decodeFeed(body []byte, target any) error {
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

func ParseSubscription(body []byte) Subscription {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return Subscription{}
	}
	var subscription Subscription
	head := bytes.ToLower(trimmed[:min(len(trimmed), 1024)])
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
	case bytes.Contains(head, []byte("<rss")) || bytes.Contains(head, []byte("<rdf")):
		var channel RssDoc
		if decodeFeed(trimmed, &channel) != nil {
			return Subscription{}
		}
		subscription = Subscription{Parsed: true, Name: cleanTitle(channel.Channel.Title), Language: normalizeLang(channel.Channel.Language)}
		subscription.addAuthor(channel.Channel.Creator)
		subscription.addAuthor(channel.Channel.ManagingEditor)
		subscription.addAuthor(channel.Channel.Author)
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
		subscription.addAuthor(document.Author.Name)
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
	if plausible(word) && !slices.ContainsFunc(subscription.Authors,
		func(existing string) bool { return strings.EqualFold(existing, word) }) {
		subscription.Authors = append(subscription.Authors, word)
	}
}
