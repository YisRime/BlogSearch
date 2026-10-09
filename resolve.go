package main

// 收录规则与内容解析
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

// 占位作者集
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

// 是否占位
func isPlaceholder(text string) bool {
	word := strings.ToLower(strings.TrimSpace(text))
	if word == "" {
		return true
	}
	return placeholderSet[word] || placeholderSet[strings.ReplaceAll(word, "_", " ")]
}

// 域名规范化
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

// 注册域推算
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

// 域名收录判定
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

// 页面抽取值
type Page struct {
	Name      string
	Author    string
	Framework string
	Language  string
	Feed      string
	Links     []string
	Friends   []string
}

// 订阅抽取值
type Subscription struct {
	Parsed   bool
	Name     string
	Authors  []string
	Language string
}

// 友链路径表
var friendPaths = []string{"/links", "/friends", "/link"}

// 版权标记式
var copyrightPattern = regexp.MustCompile(`(?i)(?:©|\(c\)|copyright)\s*(?:\d{4}(?:\s*[-–~至]\s*\d{4})?)?\s*`)

// 空白归一
func collapse(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// 语言归一
func normalizeLang(text string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(text), "_", "-"))
}

// JSON-LD取名
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

// 读节点属性
func attribute(node *html.Node, key string) string {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, key) {
			return attr.Val
		}
	}
	return ""
}

// 链接绝对化
func absoluteURL(link, origin string) *url.URL {
	if parsed, failure := url.Parse(link); failure == nil && parsed.IsAbs() {
		return parsed
	}
	root, failure := url.Parse(origin)
	if failure != nil {
		return nil
	}
	return root.ResolveReference(&url.URL{Path: link})
}

// 链接绝对化取串
func absoluteLink(link, origin string) string {
	if parsed := absoluteURL(link, origin); parsed != nil {
		return parsed.String()
	}
	return ""
}

// 取节点文本
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

// 抽取页面字段
func ParsePage(body []byte, origin string) Page {
	var page Page
	document, failure := html.Parse(bytes.NewReader(body))
	if failure != nil {
		return page
	}
	var title, stated, generator, language string
	originRegistrable := registrableOf(normalizeHost(origin))
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
					kind := strings.ToLower(attribute(node, "type"))
					link := attribute(node, "href")
					if link != "" && (strings.Contains(kind, "atom") || strings.Contains(kind, "rss") || strings.Contains(kind, "feed+json")) {
						page.Feed = absoluteLink(link, origin)
					}
				}
			case "a":
				link := attribute(node, "href")
				if link == "" {
					return
				}
				parsed := absoluteURL(link, origin)
				if parsed != nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
					hostname := normalizeHost(parsed.Host)
					if len(page.Links) < 200 {
						page.Links = append(page.Links, hostname)
					}
					if len(page.Friends) < 10 && registrableOf(hostname) == originRegistrable {
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
	page.Name = collapse(title)
	for _, cut := range []string{" - ", " _ ", " | ", " ｜ ", " · ", " — ", " – ", " » "} {
		if index := strings.Index(page.Name, cut); index > 0 {
			if head := strings.TrimSpace(page.Name[:index]); head != "" && len([]rune(head)) <= 30 {
				page.Name = head
				break
			}
		}
	}
	if runes := []rune(page.Name); len(runes) > 50 {
		page.Name = string(runes[:50])
	}
	page.Language = language
	page.Framework = detectFramework(generator, body)
	author := strings.TrimSpace(stated)
	if isPlaceholder(author) {
		if name := jsonldAuthor(jsonlds); name != "" {
			author = name
		}
	}
	if isPlaceholder(author) {
		author = copyrightName(collapse(texts.String()))
	}
	page.Author = author
	return page
}

// 版权行取作者
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
	if len(runes) < 2 || len(runes) > 40 || strings.ContainsAny(phrase, "0123456789") ||
		len(strings.Fields(phrase)) > 4 || isPlaceholder(phrase) {
		return ""
	}
	return phrase
}

// 识别托管框架
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
		if index := strings.Index(stated, " "); index > 0 {
			return stated[:index]
		}
		return stated
	}
	lower := strings.ToLower(string(body))
	for _, row := range rows {
		fingerprint := row[1]
		if fingerprint == "" {
			fingerprint = row[0]
		}
		if strings.Contains(lower, fingerprint) {
			return row[2]
		}
	}
	return ""
}

// 站点地图抽取
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

// 简易订阅报文
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

// 聚合订阅报文
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

// 字典订阅报文
type JsonDoc struct {
	Title string `json:"title"`
	Items []struct {
		Authors []struct {
			Name string `json:"name"`
		} `json:"authors"`
	} `json:"items"`
}

// 抽取订阅字段
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
		subscription = Subscription{Parsed: true, Name: collapse(decoded.Title)}
		for _, item := range decoded.Items {
			for _, person := range item.Authors {
				subscription.addAuthor(person.Name)
			}
		}
	case bytes.Contains(trimmed[:min(len(trimmed), 512)], []byte("<rss")):
		var channel RssDoc
		if xml.Unmarshal(trimmed, &channel) != nil {
			return Subscription{}
		}
		subscription = Subscription{Parsed: true, Name: collapse(channel.Channel.Title), Language: normalizeLang(channel.Channel.Language)}
		for _, item := range channel.Channel.Items {
			subscription.addAuthor(item.Creator)
			subscription.addAuthor(item.Author)
		}
	default:
		var document AtomDoc
		if xml.Unmarshal(trimmed, &document) != nil {
			return Subscription{}
		}
		subscription = Subscription{Parsed: true, Name: collapse(document.Title), Language: normalizeLang(document.Language)}
		for _, entry := range document.Entries {
			for _, person := range entry.Authors {
				subscription.addAuthor(person.Name)
			}
		}
	}
	return subscription
}

// 去重收作者
func (subscription *Subscription) addAuthor(author string) {
	word := collapse(author)
	if isPlaceholder(word) {
		return
	}
	for _, existing := range subscription.Authors {
		if strings.EqualFold(existing, word) {
			return
		}
	}
	subscription.Authors = append(subscription.Authors, word)
}
