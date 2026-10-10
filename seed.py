import ipaddress
import json
import os
import sqlite3
import sys
import time
import urllib.request
from datetime import datetime, timezone


def normalize_host(raw):
    word = raw.strip()
    if word == "":
        return ""
    index = word.find("://")
    if index >= 0:
        word = word[index + 3:]
    elif word.startswith("//"):
        word = word[2:]
    index = word.rfind("@")
    if index >= 0:
        word = word[index + 1:]
    cuts = [word.find(mark) for mark in "/?#\\"]
    cuts = [value for value in cuts if value >= 0]
    if cuts:
        word = word[:min(cuts)]
    if word.startswith("["):
        end = word.find("]")
        if end >= 0:
            word = word[:end + 1]
    else:
        index = word.rfind(":")
        if index >= 0 and "." not in word[index + 1:]:
            word = word[:index]
    word = word.strip(".")
    if word == "":
        return ""
    if is_address(word):
        return word
    lower = word.lower()
    try:
        lower = ".".join(label.encode("idna").decode("ascii") for label in lower.split("."))
    except (UnicodeError, ValueError):
        pass
    labels = lower.split(".")
    if len(labels) > 2 and labels[0] == "www":
        return ".".join(labels[1:])
    return lower


def is_address(word):
    try:
        ipaddress.ip_address(word)
        return True
    except ValueError:
        return False


def encode_tags(value):
    if value is not None and not isinstance(value, list):
        raise ValueError("Not List: %r" % value)
    text = json.dumps(value, ensure_ascii=False, separators=(",", ":"))
    for code in (0x3c, 0x3e, 0x26, 0x2028, 0x2029):
        text = text.replace(chr(code), "\\u%04x" % code)
    return text


def fetch_text():
    last = RuntimeError("下载失败")
    for attempt in range(3):
        try:
            request = urllib.request.Request(
                "https://cdn.jsdelivr.net/gh/timqian/chinese-independent-blogs@master/data/blogs.json",
                headers={"User-Agent": "blogsearch"})
            with urllib.request.urlopen(request, timeout=60) as response:
                if response.status != 200:
                    raise ValueError("下载失败：%d" % response.status)
                return response.read(8 << 20).decode("utf-8")
        except Exception as failure:
            last = failure
            if attempt < 2:
                time.sleep(2)
    raise last


def write_rows(table, rows, stamp):
    inserted, existing = 0, 0
    with table:
        cursor = table.cursor()
        for host, name, feed, tag in rows:
            cursor.execute("""INSERT INTO blog (host, state, create_at, check_at, name, feed, tag)
                VALUES (?,?,?,?,?,?,?) ON CONFLICT(host) DO NOTHING""",
                           (host, "pend", stamp, stamp, name, feed, tag))
            if cursor.rowcount > 0:
                inserted += 1
                continue
            existing += 1
            cursor.execute("""UPDATE blog SET
                name = CASE WHEN COALESCE(name, '') = '' THEN ? ELSE name END,
                feed = CASE WHEN COALESCE(feed, '') = '' THEN ? ELSE feed END,
                tag = CASE WHEN COALESCE(tag, '') = '' THEN ? ELSE tag END
                WHERE host = ?""", (name, feed, tag, host))
    return inserted, existing


def report(failure):
    print("写入失败：%s" % failure)


def main():
    try:
        seen, pairs, skipped = set(), [], 0
        for item in json.loads(fetch_text())["blogs"]:
            host = normalize_host(item.get("url") or "")
            if host == "" or host in seen or is_address(host):
                skipped += 1
                continue
            seen.add(host)
            pairs.append((host, " ".join((item.get("name") or "").split()),
                          (item.get("feed") or "").strip(), encode_tags(item.get("tags") or [])))
    except Exception as failure:
        report(failure)
        return 1
    os.makedirs("data", exist_ok=True)
    table = sqlite3.connect("data/blogsearch.db")
    try:
        table.executescript("""
CREATE TABLE IF NOT EXISTS blog (
 host      TEXT PRIMARY KEY,
 feed      TEXT NOT NULL DEFAULT '',
 lang      TEXT NOT NULL DEFAULT '',
 name      TEXT NOT NULL DEFAULT '',
 author    TEXT NOT NULL DEFAULT '',
 icp       TEXT NOT NULL DEFAULT '',
 summary   TEXT NOT NULL DEFAULT '',
 tag       TEXT NOT NULL DEFAULT '',
 friend    TEXT NOT NULL DEFAULT '',
 framework TEXT NOT NULL DEFAULT '',
 state     TEXT NOT NULL DEFAULT 'pend',
 cert      INTEGER NOT NULL DEFAULT 0,
 create_at TEXT NOT NULL DEFAULT '',
 check_at  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS blog_queue ON blog (check_at, create_at);
""")
        inserted, existing = write_rows(table, pairs, datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"))
    except Exception as failure:
        report(failure)
        return 1
    finally:
        table.close()
    print("写入成功：新增%d行、已有%d行、跳过%d行" % (inserted, existing, skipped))
    return 0


if __name__ == "__main__":
    sys.exit(main())
