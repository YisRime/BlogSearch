package main

import (
	"bytes"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

var htmlTag = regexp.MustCompile(`<[^>]*>`)

func slots(count int) string {
	return "(?" + strings.Repeat(",?", count-1) + ")"
}

func collapse(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func clip(text string, limit int) string {
	if utf8.RuneCountInString(text) > limit {
		return string([]rune(text)[:limit])
	}
	return text
}

func containsAny(text string, marks []string) bool {
	return slices.ContainsFunc(marks, func(mark string) bool { return strings.Contains(text, mark) })
}

func plainText(text string) string {
	return collapse(htmlTag.ReplaceAllString(html.UnescapeString(text), " "))
}

func pickFirst(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func attrOf(node *html.Node, key string) string {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, key) {
			return attr.Val
		}
	}
	return ""
}

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
