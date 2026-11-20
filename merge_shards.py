#!/usr/bin/env python3
"""merge_shards.py — 合并 matrix 分片产出的 rank-*.csv / clean-*.csv。

用法: python3 merge_shards.py <shard_dir> <rank.csv> <rank-latest.csv>

每个分片的 sweep.sh 已经做过 geo/判定/片内去重；这里只做拼表 + 跨片兜底去重，
干净版按带宽 MB/s 降序，与单 job 模式的输出保持一致。
"""
import csv
import glob
import os
import sys

HDR = ["中继", "出口IP", "延迟秒", "Google状态码", "Google跳转", "并发", "带宽MB/s",
       "风控值", "纯净度", "是否家宽", "是否原生", "国家", "城市", "运营商", "AS",
       "代理标记", "机房标记", "判定"]


def header_of(path):
    with open(path, newline="", encoding="utf-8-sig") as f:
        for r in csv.reader(f):
            if r and r[0] in ("中继", "relay"):
                return r
    return None


def rows_of(path):
    with open(path, newline="", encoding="utf-8-sig") as f:
        for r in csv.reader(f):
            if r and r[0] not in ("中继", "relay"):
                yield r


def bw(r):
    try:
        return float(r[6])
    except (IndexError, TypeError, ValueError):
        return 0.0


def main():
    src, out, clean_out = sys.argv[1], sys.argv[2], sys.argv[3]
    rank_files = sorted(glob.glob(os.path.join(src, "rank-*.csv")))
    clean_files = sorted(glob.glob(os.path.join(src, "clean-*.csv")))

    rows = []
    hdr = HDR
    for f in rank_files:
        found = header_of(f)
        if found:
            hdr = found
        rows.extend(rows_of(f))

    clean = {}
    for f in clean_files:
        for r in rows_of(f):
            if len(r) < 18:
                continue
            old = clean.get(r[0])
            if old is None or bw(r) > bw(old):
                clean[r[0]] = r

    if not rows:
        print("no shard rows found", file=sys.stderr)
        return 1

    with open(out, "w", newline="", encoding="utf-8-sig") as f:
        w = csv.writer(f)
        w.writerow(hdr)
        w.writerows(rows)

    clean_rows = sorted(clean.values(), key=bw, reverse=True)
    with open(clean_out, "w", newline="", encoding="utf-8-sig") as f:
        w = csv.writer(f)
        w.writerow(hdr)
        w.writerows(clean_rows)

    print(f"merged {len(rank_files)} shards: rank={len(rows)}, clean={len(clean_rows)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
