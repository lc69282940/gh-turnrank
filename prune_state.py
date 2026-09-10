#!/usr/bin/env python3
# prune_state.py — 连续 3 天不合格的中继自动移出 candidates.txt（直接移除）
# 不合格的定义 = 拨测拿不到出口（DEAD / AUTH_REQUIRED / UNREACHABLE）、带宽为 0、
#                verdict 非 PASS——连续 3 天即淘汰
# 例外：Google 判定为中国大陆（CN_REDIRECT）的中继一经发现立即移除，不等 3 天
# 状态文件 failed_days.json 记录每个中继的连续失败天数，成功一次即清零
import csv, json, os, sys

CAND = sys.argv[1] if len(sys.argv) > 1 else "candidates.txt"
RANK = sys.argv[2] if len(sys.argv) > 2 else "rank.csv"
STATE = sys.argv[3] if len(sys.argv) > 3 else "failed_days.json"
TODAY = sys.argv[4] if len(sys.argv) > 4 else "pruned_today.txt"
THRESHOLD = 3
BAD = ("不通", "需要账密", "不可达")


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
    if len(row) > 4 and row[4] == "大陆判定":
        return True  # 被 Google 判定中国大陆，直接淘汰
    if len(row) > 6:
        try:
            if float(row[6]) == 0:
                return True
        except ValueError:
            return True
    if len(row) >= 18 and row[-1] != "正常":
        return True  # Google 不通或异常跳转
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
        if row and row[0] not in ("relay", "中继"):
            results[row[0]] = row

active, removed, new_state = [], [], {}
for raw in open(CAND):
    n, raw = normalize(raw)
    if n is None:
        active.append(raw)
        continue
    row = results.get(n)
    if row is not None and len(row) > 4 and row[4] == "大陆判定":
        removed.append((raw, "CN"))    # 被判中国大陆：一经发现立即移除，不等 3 天
        continue
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

with open(CAND, "w") as f:
    f.write("\n".join(active).rstrip("\n") + "\n")
json.dump(new_state, open(STATE, "w"), indent=0, sort_keys=True)

with open(TODAY, "w") as f:
    if removed:
        f.write(f"本次移除 {len(removed)} 个连续 {THRESHOLD} 天不通的中继（已直接移除）：<br>")
        for raw, cnt in removed:
            f.write(f"<code>{raw}</code>（连续失败 {cnt} 天）<br>")
    else:
        f.write("本次没有中继触发 3 天淘汰规则。")

n_active = sum(1 for l in active if l.strip() and not l.strip().startswith("#"))
print(f"candidates active: {n_active}; pruned today: {len(removed)}; state tracked: {len(new_state)}")
