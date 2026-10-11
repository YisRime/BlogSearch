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
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

// 平台型非博客
var hubHosts = map[string]bool{
	"github.com": true, "gitlab.com": true, "gitee.com": true, "bitbucket.org": true,
	"stackoverflow.com": true, "stackexchange.com": true, "segmentfault.com": true, "quora.com": true,
	"zhihu.com": true, "juejin.cn": true, "csdn.net": true, "cnblogs.com": true, "oschina.net": true,
	"jianshu.com": true, "wanandroid.com": true, "v2ex.com": true, "hostloc.com": true,
	"nodeseek.com": true, "linux.do": true, "52pojie.cn": true, "ituring.com.cn": true,
	"weibo.com": true, "facebook.com": true, "twitter.com": true, "x.com": true, "youtube.com": true,
	"instagram.com": true, "pinterest.com": true, "reddit.com": true, "tumblr.com": true,
	"linkedin.com": true, "douban.com": true, "bilibili.com": true, "xiaohongshu.com": true,
	"douyin.com": true, "kuaishou.com": true, "toutiao.com": true, "t.me": true, "telegram.org": true,
	"discord.com": true, "discord.gg": true, "medium.com": true,
	"baidu.com": true, "qq.com": true, "weixin.qq.com": true, "mp.weixin.qq.com": true,
	"sina.com.cn": true, "sina.com": true, "sohu.com": true, "163.com": true, "ifeng.com": true,
	"bing.com": true, "google.com": true, "msn.com": true, "yahoo.com": true, "yandex.com": true,
	"microsoft.com": true, "apple.com": true, "amazon.com": true, "wikipedia.org": true,
	"mozilla.org": true, "adobe.com": true, "oracle.com": true, "ibm.com": true, "intel.com": true,
	"nvidia.com": true, "samsung.com": true, "huawei.com": true, "xiaomi.com": true,
	"alibaba.com": true, "taobao.com": true, "tmall.com": true, "jd.com": true,
	"pinduoduo.com": true, "meituan.com": true, "tencent.com": true, "netease.com": true,
	"chromium.org": true, "android.com": true, "python.org": true, "golang.org": true,
	"rust-lang.org": true, "nodejs.org": true, "npmjs.com": true, "docker.com": true,
	"kubernetes.io": true, "apache.org": true, "gnu.org": true, "sourceforge.net": true,
	"vercel.com": true, "netlify.com": true, "akamai.com": true, "fastly.com": true,
	"aliyun.com": true, "feedly.com": true, "slashdot.org": true, "caniuse.com": true,
	"mit.edu": true, "sdf.org": true, "mastodon.social": true,
	"wordpress.com": true, "blogspot.com": true, "foreverblog.cn": true, "storeweb.cn": true,
	"listbox.app": true, "urschool.org": true, "cafenomad.tw": true,
}

// 资源统计类域名
var noiseHosts = map[string]bool{
	"gstatic.com": true, "googleapis.com": true, "googleusercontent.com": true, "ggpht.com": true,
	"gravatar.com": true, "wp.com": true, "w.org": true, "typekit.net": true, "fontawesome.com": true,
	"jsdelivr.net": true, "unpkg.com": true, "cdnjs.com": true, "bootcdn.net": true, "bootcss.com": true,
	"staticfile.org": true, "statically.io": true, "bootstrapcdn.com": true, "aspnetcdn.com": true,
	"cloudflare.com": true, "cloudfront.net": true, "fastly.net": true, "akamaihd.net": true,
	"akamai.net": true, "azureedge.net": true, "bunnycdn.com": true, "gcorelabs.com": true,
	"alicdn.com": true, "aliyuncs.com": true, "myqcloud.com": true, "upyun.com": true,
	"qiniucdn.com": true, "qiniudn.com": true, "bdstatic.com": true, "126.net": true, "127.net": true,
	"xhscdn.com": true, "dusays.com": true, "sm.ms": true,
	"google-analytics.com": true, "googletagmanager.com": true, "doubleclick.net": true,
	"sentry.io": true, "hotjar.com": true, "mixpanel.com": true, "segment.io": true,
	"amplitude.com": true, "clarity.ms": true, "umami.is": true, "plausible.io": true, "matomo.cloud": true,
	"cnzz.com": true, "umeng.com": true, "umengcloud.com": true, "ibruce.info": true, "51.la": true,
	"cloudflareinsights.com": true, "vercount.one": true,
	"disqus.com": true, "addthis.com": true, "sharethis.com": true, "addtoany.com": true,
	"giscus.app": true, "utterances.es": true, "twikoo.com": true, "waline.js.org": true,
	"valine.js.org": true, "artalk.js.org": true, "livere.com": true, "changyan.com": true,
	"geetest.com": true, "vaptcha.com": true, "hcaptcha.com": true, "recaptcha.net": true,
	"imgur.com": true, "imgbb.com": true, "sinaimg.cn": true, "zhimg.com": true,
	"gtimg.cn": true, "hdslb.com": true, "doubanio.com": true, "byteimg.com": true, "pstatp.com": true,
	"fbcdn.net": true, "twimg.com": true, "ytimg.com": true, "cdninstagram.com": true, "pinimg.com": true,
	"licdn.com": true, "zstatic.com": true, "githubassets.com": true, "githubusercontent.com": true,
}

// 禁用域名后缀
var blockedSuffixes = []string{".gov", ".mil", ".local", ".localhost", ".test", ".invalid",
	".example", ".internal", ".home", ".lan", ".onion"}

// 无效名称词
var junkNames = []string{"404", "not found", "页面不存在", "找不到页面", "页面未找到", "access denied",
	"welcome to nginx", "index of", "default web site page", "站点未找到", "网站未找到",
	"域名出售", "域名停放", "域名已过期", "已到期", "请续费", "该域名正在出售",
	"domain for sale", "this domain is for sale", "buy this domain", "domain expired",
	"account suspended", "site not found", "under construction",
	"网站维护中", "建站中", "维护中", "coming soon", "敬请期待", "网址导航", "搜索引擎",
	"无法访问", "暂时无法", "will go into a meta tag", "recent content on ",
	"买球", "赌球", "棋牌", "娱乐城", "博彩", "下注", "彩票", "球盘", "让球", "体育投注",
	"开户", "万博", "manbext", "slot", "judi", "bandar", "prediksi", "gacor",
	"免费观看", "在线观看", "影院"}

// 通用标题词
var genericNames = map[string]bool{
	"首页": true, "主页": true, "网站首页": true, "home": true, "homepage": true,
	"blog": true, "index": true, "untitled": true, "start": true, "welcome": true, "欢迎": true,
	"description": true, "posts": true, "just a moment": true,
}

var archiveHints = []string{"archive", "归档", "分页", "/page/", "older", "newer"}

// 订阅源标记
var feedMarks = []string{"atom", "rss", "feed", "feed+json"}

var feedClosers = [][2]string{{"</item>", "</channel></rss>"}, {"</entry>", "</feed>"}}

// 友链判定标记
var friendMarks = []string{"友链", "友情链接", "友邻", "朋友", "friend", "blogroll",
	"links", "link", "友情", "blogmate", "blogger"}

// 名称节点标记
var nameMarks = []string{"name", "title", "sitename", "site-name", "nickname"}

// 噪声容器标记
var friendSkipMarks = []string{"nav", "menu", "footer", "header", "social", "share", "comment",
	"breadcrumb", "pagination", "copyright", "credit", "powered", "widget", "sidebar", "toolbar",
	"privacy", "policy", "agreement", "cookie", "legal", "disclaimer", "terms", "third-party", "thirdparty"}

// 强噪声标记
var friendHardSkips = []string{"footer", "header", "navbar", "menu", "privacy", "policy", "copyright",
	"breadcrumb", "pagination", "social", "share", "comment"}

// 友链名噪声词
var friendNameNoise = []string{"申请友链", "友链申请", "交换友链", "申请", "添加", "提交", "首页", "home",
	"更多", "more", "返回", "back", "上一页", "下一页", "next", "prev", "阅读全文", "read more",
	"评论", "留言", "关于", "about", "归档", "archive", "标签", "tag", "分类", "category",
	"订阅", "rss", "登录", "注册", "搜索", "search", "友情链接", "友链"}

var authorMarks = []string{"author", "byline", "vcard"}

var authorNoise = map[string]bool{
	"by": true, "posted by": true, "admin": true, "administrator": true, "author": true,
	"unknown": true, "anonymous": true, "none": true, "n/a": true, "na": true,
	"互动": true, "评论": true, "登录": true, "注册": true, "分享": true, "首页": true,
	"投稿": true, "作者": true, "博主": true, "日期": true, "时间": true, "張貼者": true, "张贴者": true,
}

// 作者截断词
var authorStops = []string{"powered by", "built with", "generated by", "©", "copyright", "all rights", "备案"}

// 作者前缀
var authorPrefixes = []string{"作者是", "作者：", "作者:", "by ", "posted by ", "written by ", "文/", "文："}

// 作者截断符
var authorCuts = []string{"，", "。", "！", "？", "；", "：", "、", "…", "“", "”", "—", "–", ",", ";", "<", "（", "("}

var copyrightStops = []string{"保留", "all rights", "。", "备案", "icp"}

var copyrightNoise = []string{"·", "•", "|", "｜", "/", "访客", "评论", "登录", "搜索", "rss", "design", "theme"}

var copyrightPattern = regexp.MustCompile(`(?i)(?:©|\(c\)|copyright)\s*(?:\d{4}(?:\s*[-–~至]\s*\d{4})?)?\s*`)

var frameworkNoise = map[string]bool{
	"by": true, "with": true, "using": true, "powered": true, "and": true, "the": true,
	"for": true, "on": true, "in": true, "a": true, "an": true, "built": true, "version": true,
}

// 框架特征表
var frameworkMarks = [][3]string{
	{"wordpress", "wp-content", "WordPress"},
	{"typecho", "/usr/themes/", "Typecho"},
	{"hexo", "hexo-theme", "Hexo"},
	{"hugo", "", "Hugo"},
	{"jekyll", "", "Jekyll"},
	{"astro", "/_astro/", "Astro"},
	{"next.js", "_next/", "Next.js"},
	{"nuxt", "/_nuxt/", "Nuxt"},
	{"gatsby", "___gatsby", "Gatsby"},
	{"ghost", "/assets/built/", "Ghost"},
	{"vuepress", "vuepress", "VuePress"},
	{"vitepress", "vitepress", "VitePress"},
	{"docusaurus", "docusaurus", "Docusaurus"},
	{"gridea", "gridea", "Gridea"},
	{"halo", "halo-theme", "Halo"},
	{"zblog", "zblog", "Z-Blog"},
	{"emlog", "emlog", "Emlog"},
	{"docsify", "docsify", "Docsify"},
	{"mkdocs", "mkdocs", "MkDocs"},
	{"eleventy", "eleventy", "Eleventy"},
	{"sveltekit", "__sveltekit", "SvelteKit"},
	{"remix", "__remix", "Remix"},
	{"pelican", "", "Pelican"},
	{"zola", "", "Zola"},
}

var icpPattern = regexp.MustCompile(`[\p{Han}]?ICP备\d+号|公网安备\d+号`)

// 标题字符集
const (
	separatorChars = "-–—_|｜/·»•・~"
	titleEdges     = " \t\\" + separatorChars
	titleMarks     = "｜·»•・"
)

// 首页字段
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

// 订阅源
type Feed struct {
	Parsed    bool
	Name      string
	Authors   []string
	Language  string
	Summary   string
	Generator string
}

// 作者名
type personName struct {
	Name string `json:"name" xml:"name"`
}

// RSS 结构
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

// Atom 结构
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

// JSON Feed
type jsonFeed struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Items       []struct {
		Authors []personName `json:"authors"`
	} `json:"items"`
}

// meta 汇总
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

// 链接扫描
type linkScan struct {
	root        *url.URL
	registrable string
	cache       map[string]string
}

// 规范化主机
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

// 取注册域
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

// 判资源域
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

// 取合法主机
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
	if noiseHost(host) {
		return ""
	}
	return host
}

// 判错误页
func errorPage(name string) bool {
	return containsAny(strings.ToLower(collapse(name)), junkNames)
}

// 判非博客
func notBlog(site *BlogSite, page Home, result *Result) bool {
	if result.Feed != "" || site.Feed != "" || result.Framework != "" || result.ICP != "" ||
		page.HasArchive || len(result.Friends) > 0 {
		return false
	}
	label := collapse(page.Name)
	return hubHosts[registrableOf(site.Host)] || label == "" || genericName(label) || errorPage(label)
}

// 判名称合理
func nameOK(word string, maxRunes, maxWords int) bool {
	size := utf8.RuneCountInString(word)
	return size >= 2 && size <= maxRunes && !strings.Contains(word, "://") &&
		len(strings.Fields(word)) <= maxWords
}

// 按停止词截断
func cutAt(text string, stops []string) string {
	lower := strings.ToLower(text)
	for _, stop := range stops {
		if index := strings.Index(lower, stop); index >= 0 {
			text, lower = text[:index], lower[:index]
		}
	}
	return strings.TrimSpace(text)
}

// 去首尾装饰符
func trimSymbols(text string) string {
	return strings.TrimFunc(text, func(symbol rune) bool {
		return !unicode.IsLetter(symbol) && !unicode.IsDigit(symbol) && !unicode.IsSpace(symbol)
	})
}

// 取 by 后名字
func afterBy(text string) string {
	index := strings.LastIndex(strings.ToLower(text), " by ")
	if index < 0 {
		return text
	}
	tail := strings.TrimSpace(text[index+4:])
	if nameOK(tail, 16, 3) && !strings.ContainsAny(tail, "0123456789") {
		return tail
	}
	return text
}

// 清洗语言
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

// 找标题切点
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

// 判通用词
func genericName(name string) bool {
	return genericNames[strings.ToLower(strings.TrimSpace(name))]
}

// 取首个合格分段
func pickSegment(title string) string {
	for _, part := range strings.FieldsFunc(title, func(symbol rune) bool { return strings.ContainsRune(separatorChars, symbol) }) {
		part = trimSymbols(strings.TrimSpace(part))
		size := utf8.RuneCountInString(part)
		if size < 2 || size > 32 || genericName(part) || strings.ContainsAny(part, "0123456789") {
			continue
		}
		return part
	}
	return ""
}

// 清洗站名
func tidyTitle(text string) string {
	full := collapse(html.UnescapeString(text))
	name := full
	if head := strings.TrimSpace(name[:titleCut(name)]); head != "" && utf8.RuneCountInString(head) <= 32 {
		name = head
	}
	name = trimSymbols(strings.TrimSpace(name))
	if genericName(name) || utf8.RuneCountInString(name) > 40 {
		if better := pickSegment(full); better != "" {
			name = better
		}
	}
	return clip(name, 32)
}

// 清洗摘要
func summaryText(text string) string {
	word := plainText(text)
	lower := strings.ToLower(word)
	if utf8.RuneCountInString(word) < 4 || containsAny(lower, junkNames) || genericName(lower) {
		return ""
	}
	return clip(word, 256)
}

// 清洗作者
func authorName(text string, refs ...string) string {
	word := trimSymbols(collapse(text))
	if utf8.RuneCountInString(word) > 20 && containsAny(word, authorCuts) {
		return ""
	}
	word = cutAt(cutAt(word, authorCuts), authorStops)
	for _, prefix := range authorPrefixes {
		word = strings.TrimSpace(strings.TrimPrefix(word, prefix))
	}
	word = trimSymbols(strings.TrimPrefix(afterBy(word), "@"))
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

// 解析作者
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

// 判 Person
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

// 取 JSON-LD
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

// 取版权作者
func copyrightName(text string) string {
	match := copyrightPattern.FindStringIndex(text)
	if match == nil {
		return ""
	}
	phrase := collapse(cutAt(text[match[1]:], copyrightStops))
	phrase = strings.Trim(phrase, " \t:：-—、,.。;；")
	if containsAny(strings.ToLower(phrase), copyrightNoise) || !nameOK(phrase, 32, 6) ||
		strings.ContainsAny(phrase, "0123456789@©") {
		return ""
	}
	return phrase
}

// 清洗框架名
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
	lower := strings.ToLower(name)
	if size < 2 || size > 32 || strings.ContainsAny(name, "0123456789@") ||
		strings.Contains(name, "'s ") || strings.Contains(name, "’s ") ||
		containsAny(lower, feedMarks) || strings.Contains(lower, "generator") {
		return ""
	}
	return name
}

// 识别框架
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
		if row[1] != "" && bytes.Contains(lowered, []byte(row[1])) {
			return row[2]
		}
	}
	return ""
}

// 扫 meta 标签
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

// 扫链接
func (scan *linkScan) visit(node *html.Node, link, section string, page *Home) {
	entry := deepText(node)
	if !page.HasArchive {
		page.HasArchive = containsAny(strings.ToLower(link+" "+entry), archiveHints)
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
		containsAny(strings.ToLower(parsed.Path+"?"+parsed.RawQuery), feedMarks) {
		page.Feed = parsed.String()
	}
	rel := strings.ToLower(attrOf(node, "rel"))
	style := strings.ToLower(attrOf(node, "class") + " " + attrOf(node, "id"))
	if section == "nav" || section == "footer" || entry == "" ||
		strings.Contains(rel, "nofollow") || strings.Contains(rel, "sponsored") ||
		strings.Contains(style, "social") || strings.Contains(style, "share") {
		return
	}
	page.HasLinks = true
	if registrableOf(hostname) == scan.registrable {
		lower := strings.ToLower(entry + " " + parsed.Path)
		if strings.Contains(rel, "friend") || strings.Contains(rel, "blogroll") || containsAny(lower, friendMarks) {
			page.Friends = append(page.Friends, parsed.String())
		}
	}
}

// 友链条目
type Friend struct {
	Name string
	URL  string
}

// 取子树文本
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

// 取首个匹配
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

// 取标记文本
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

// 取首个 alt
func firstAlt(root *html.Node) string {
	node := findNode(root, func(node *html.Node) bool {
		return strings.EqualFold(node.Data, "img") && collapse(attrOf(node, "alt")) != ""
	})
	if node == nil {
		return ""
	}
	return collapse(attrOf(node, "alt"))
}

// 取条目名称
func friendName(anchor *html.Node) string {
	for _, candidate := range []string{
		markedText(anchor, nameMarks),
		firstAlt(anchor),
		attrOf(anchor, "title"),
		deepText(anchor),
	} {
		name := strings.Trim(collapse(html.UnescapeString(candidate)), titleEdges)
		size := utf8.RuneCountInString(name)
		if size < 2 || size > 32 || strings.Contains(name, "://") || strings.ContainsAny(name, "©@") {
			continue
		}
		if containsAny(strings.ToLower(name), friendNameNoise) {
			continue
		}
		return name
	}
	return ""
}

// 判友链锚点
func friendEntry(anchor *html.Node, section string, blocked bool, root *url.URL, site string) (Friend, bool) {
	if blocked || section == "nav" || section == "footer" || section == "header" {
		return Friend{}, false
	}
	rel := strings.ToLower(attrOf(anchor, "rel"))
	if strings.Contains(rel, "nofollow") || strings.Contains(rel, "sponsored") {
		return Friend{}, false
	}
	if style := strings.ToLower(attrOf(anchor, "class") + " " + attrOf(anchor, "id")); containsAny(style, friendSkipMarks) {
		return Friend{}, false
	}
	parsed := resolveLink(attrOf(anchor, "href"), root)
	if parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return Friend{}, false
	}
	host := normalizeHost(parsed.Host)
	neighbor := registrableOf(host)
	if neighbor == "" || neighbor == site || hubHosts[neighbor] || noiseHost(host) {
		return Friend{}, false
	}
	name := friendName(anchor)
	if name == "" {
		return Friend{}, false
	}
	return Friend{Name: name, URL: parsed.String()}, true
}

// 提取友链条目
func readFriends(body []byte, origin string) []Friend {
	document, failure := html.Parse(bytes.NewReader(body))
	if failure != nil {
		return nil
	}
	root, _ := url.Parse(origin)
	site := registrableOf(normalizeHost(origin))
	var friends []Friend
	seen := map[string]bool{}
	var walk func(*html.Node, string, bool)
	walk = func(node *html.Node, section string, blocked bool) {
		if node.Type == html.ElementNode {
			tag := strings.ToLower(node.Data)
			if tag == "nav" || tag == "footer" || tag == "header" {
				section = tag
			}
			if style := strings.ToLower(attrOf(node, "class") + " " + attrOf(node, "id")); style != "" {
				switch {
				case containsAny(style, friendMarks) && !containsAny(style, friendHardSkips):
					blocked = false
				case containsAny(style, friendSkipMarks):
					blocked = true
				}
			}
			if tag == "a" {
				if friend, ok := friendEntry(node, section, blocked, root, site); ok && !seen[friend.URL] {
					seen[friend.URL] = true
					friends = append(friends, friend)
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child, section, blocked)
		}
	}
	walk(document, "", false)
	return friends
}

// 解析首页
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
					containsAny(kind, feedMarks) {
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
						info.domAuthor = pickFirst(markedText(node, nameMarks), deepText(node))
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

// 解析订阅体
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

// 追加作者
func (feed *Feed) pushAuthor(author string) {
	word := authorName(author)
	if word != "" && !slices.ContainsFunc(feed.Authors,
		func(existing string) bool { return strings.EqualFold(existing, word) }) {
		feed.Authors = append(feed.Authors, word)
	}
}

// 解析订阅源
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

// 解析 sitemap
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
