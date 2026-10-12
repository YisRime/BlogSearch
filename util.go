package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// 生成占位符
func slots(count int) string {
	return "(?" + strings.Repeat(",?", count-1) + ")"
}

// 判断超时
func isTimeout(failure error) bool {
	var timeout interface{ Timeout() bool }
	return errors.Is(failure, context.DeadlineExceeded) || (errors.As(failure, &timeout) && timeout.Timeout())
}

// 判断跳转状态
func redirectStatus(status int) bool {
	return status >= http.StatusMultipleChoices && status < http.StatusBadRequest
}

// 过滤关系标记
func blockedRelation(rel string) bool {
	return strings.Contains(rel, "nofollow") || strings.Contains(rel, "sponsored")
}

// 合并空白符
func collapse(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// 裁剪文本内容
func clip(text string, limit int) string {
	if utf8.RuneCountInString(text) > limit {
		return string([]rune(text)[:limit])
	}
	return text
}

// 检查任一标记
func containsAny(text string, marks []string) bool {
	return slices.ContainsFunc(marks, func(mark string) bool { return strings.Contains(text, mark) })
}

// 寻找首个命中
func firstMark(text string, marks []string) (string, bool) {
	for _, mark := range marks {
		if strings.Contains(text, mark) {
			return mark, true
		}
	}
	return "", false
}

// 选择首个非空
func pickFirst(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// 读取节点属性
func attrOf(node *html.Node, key string) string {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, key) {
			return attr.Val
		}
	}
	return ""
}

// 读取直系文本
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

// 解析网页链接
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

// 判断通用名称
func genericName(name string) bool {
	return genericNames[strings.ToLower(strings.TrimSpace(name))]
}
