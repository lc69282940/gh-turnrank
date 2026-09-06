#!/usr/bin/env python3
# prune_state.py — 连续 3 天不通的中继自动移出 candidates.txt（归档到 retired.txt）
# 不通的定义 = 拨测拿不到出口（DEAD / AUTH_REQUIRED / UNREACHABLE）或带宽为 0
# 状态文件 failed_days.json 记录每个中继的连续失败天数，成功一次即清零
import csv, json, os, sys
from datetime import datetime, timedelta, timezone

CAND = sys.argv[1] if len(sys.argv) > 1 else "candidates.txt"
RANK = sys.argv[2] if len(sys.argv) > 2 else "rank.csv"
STATE = sys.argv[3] if len(sys.argv) > 3 else "failed_days.json"
RETIRED = sys.argv[4] if len(sys.argv) > 4 else "retired.txt"
TODAY = sys.argv[5] if len(sys.argv) > 5 else "pruned_today.txt"
THRESHOLD = 3
BAD = ("DEAD", "AUTH_REQUIRED", "UNREACHABLE")


def normalize(line):
    """与 sweep.sh 的归一化逻辑保持一致（含去掉行内注释）"""
    raw = line.rstrip("\n")
    line = raw.split("#", 1)[0].strip()
    if not line:
        return None, raw
    if line.startswith("turn://"):
        return line, raw
    if "|" in line:
        hp, up = line.split("|", 1)
        u, _, p = up.partition(":")
        return f"turn://{u}:{p}@{hp}", raw
    return f"turn://{line}", raw


def unusable(row):
    if len(row) < 2:
        return True
    if row[1] in BAD:
        return True
    if len(row) > 8:
        try:
            if float(row[8]) == 0:
                return True
        except ValueError:
            return True
    return False


state = {}
if os.path.exists(STATE):
    try:
        state = json.load(open(STATE))
    except Exception:
        state = {}

results = {}
if os.path.exists(RANK):
    for row in csv.reader(open(RANK)):
        if row and row[0] != "relay":
            results[row[0]] = row

active, removed, new_state = [], [], {}
for raw in open(CAND):
    n, raw = normalize(raw)
    if n is None:
        active.append(raw)
        continue
    row = results.get(n)
    if row is None:
        cnt = state.get(n, 0)          # 本轮没有数据（如 sweep 失败）→ 计数保持不变
    elif unusable(row):
        cnt = state.get(n, 0) + 1      # 不通 → 连续失败 +1
    else:
        cnt = 0                        # 通了 → 清零
    if cnt >= THRESHOLD:
        removed.append((raw, cnt))
        continue
    if cnt > 0:
        new_state[n] = cnt
    active.append(raw)

if removed:
    stamp = datetime.now(timezone(timedelta(hours=8))).strftime("%Y-%m-%d")
    with open(RETIRED, "a") as f:
        for raw, cnt in removed:
            f.write(f"{raw}\t# retired {stamp} ({cnt} consecutive failed days)\n")

with open(CAND, "w") as f:
    f.write("\n".join(active).rstrip("\n") + "\n")
json.dump(new_state, open(STATE, "w"), indent=0, sort_keys=True)

with open(TODAY, "w") as f:
    if removed:
        f.write(f"本次移除 {len(removed)} 个连续 {THRESHOLD} 天不通的中继（已归档到 retired.txt）：<br>")
        for raw, cnt in removed:
            f.write(f"<code>{raw}</code>（连续失败 {cnt} 天）<br>")
    else:
        f.write("本次没有中继触发 3 天淘汰规则。")

n_active = sum(1 for l in active if l.strip() and not l.strip().startswith("#"))
print(f"candidates active: {n_active}; pruned today: {len(removed)}; state tracked: {len(new_state)}")
