#!/usr/bin/env python3
"""shard_candidates.py — 把 candidates.txt 均分成 N 份，供 Actions matrix 并行海选。

同一个中继的重复/不同写法（turn://、裸地址、user|pass）会归一化后分到同一片，
这样每片内部 sweep.sh 的去重仍然有效。

用法: python3 shard_candidates.py INDEX TOTAL [candidates.txt] [out.txt]
"""
import sys


def norm(line):
    """与 sweep.sh 的归一化逻辑保持一致。"""
    line = line.split("#", 1)[0].strip()
    if not line:
        return None
    if line.startswith("turn://"):
        return line
    if "|" in line:
        hp, up = line.split("|", 1)
        u, _, p = up.partition(":")
        return f"turn://{u}:{p}@{hp}"
    return f"turn://{line}"


def main():
    idx, total = int(sys.argv[1]), int(sys.argv[2])
    src = sys.argv[3] if len(sys.argv) > 3 else "candidates.txt"
    out = sys.argv[4] if len(sys.argv) > 4 else "/tmp/cands.txt"

    groups = {}
    for line in open(src, encoding="utf-8"):
        n = norm(line)
        if n is None:
            continue
        groups.setdefault(n, []).append(line if line.endswith("\n") else line + "\n")

    # 贪心装到当前最空的一片：行数均衡，重复项整组落同一片
    buckets = [[] for _ in range(total)]
    loads = [0] * total
    for lines in sorted(groups.values(), key=len, reverse=True):
        i = min(range(total), key=loads.__getitem__)
        buckets[i].extend(lines)
        loads[i] += len(lines)

    with open(out, "w", encoding="utf-8", newline="") as f:
        f.writelines(buckets[idx])
    print(f"shard {idx + 1}/{total}: {loads[idx]} candidates")


if __name__ == "__main__":
    main()
