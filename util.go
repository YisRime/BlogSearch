package main

import (
	"bytes"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// 拼占位符
func slots(count int) string {
	return "(?" + strings.Repeat(",?", count-1) + ")"
}

// 折叠空白
func collapse(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// 截断文本
func clip(text string, limit int) string {
	if utf8.RuneCountInString(text) > limit {
		return string([]rune(text)[:limit])
	}
	return text
}

// 判命中任一
func containsAny(text string, marks []string) bool {
	return slices.ContainsFunc(marks, func(mark string) bool { return strings.Contains(text, mark) })
}

// 取首个非空
func pickFirst(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// 取属性
func attrOf(node *html.Node, key string) string {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, key) {
			return attr.Val
		}
	}
	return ""
}

// 取直系文本
func textOf(node *html.Node) string {
	var buffer bytes.Buffer
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.TextNode {
			buffer.WriteString(child.Data)
			buffer.WriteByte(' ')
		}
	}
	return collapse(buffer.String())
}

// 解析链接
func resolveLink(link string, root *url.URL) *url.URL {
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

// 判通用词
func genericName(name string) bool {
	return genericNames[strings.ToLower(strings.TrimSpace(name))]
}
