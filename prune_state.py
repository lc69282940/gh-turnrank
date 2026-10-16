#!/usr/bin/env python3
# prune_state.py — 中继淘汰规则
#   即时淘汰：出口IP列 = 不通 / 需要账密 / 不可达（拨测拿不到出口）
#             或 Google 判定中国大陆（大陆判定）
#   连续 3 天淘汰：其余不合格 —— 带宽为 0、Google 不通或异常跳转
# 状态文件 failed_days.json 只跟踪进入“连续 3 天”计数的中继，成功一次即清零
import csv, json, os, sys

CAND = sys.argv[1] if len(sys.argv) > 1 else "candidates.txt"
RANK = sys.argv[2] if len(sys.argv) > 2 else "rank.csv"
STATE = sys.argv[3] if len(sys.argv) > 3 else "failed_days.json"
TODAY = sys.argv[4] if len(sys.argv) > 4 else "pruned_today.txt"
THRESHOLD = 3
BAD = ("不通", "需要账密", "不可达")
REGION = "大陆判定"
MAX_LIST = 20


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


def instant_reason(row):
    """一票淘汰：返回原因字符串；不满足即时淘汰返回 None"""
    if len(row) < 2:
        return None
    if row[1] in BAD:
        return row[1]
    if len(row) > 4 and row[4] == REGION:
        return REGION
    return None


def unusable(row):
    if len(row) < 2:
        return True
    if row[1] in BAD:
        return True
    if len(row) > 4 and row[4] == REGION:
        return True
    if len(row) > 6:
        try:
            if float(row[6]) == 0:
                return True
        except ValueError:
            return True
    if len(row) >= 18 and row[-1] != "正常":
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
    for row in csv.reader(open(RANK, encoding="utf-8-sig")):
        if row and row[0] not in ("relay", "中继"):
            results[row[0]] = row

active, removed, new_state = [], [], {}
for raw in open(CAND):
    n, raw = normalize(raw)
    if n is None:
        active.append(raw)
        continue
    row = results.get(n)
    if row is not None:
        why = instant_reason(row)
        if why:
            removed.append((raw, why, None))
            continue
    if row is None:
        cnt = state.get(n, 0)          # 本轮没有数据（如 sweep 失败）→ 计数保持不变
    elif unusable(row):
        cnt = state.get(n, 0) + 1      # 连续不合格 +1
    else:
        cnt = 0                        # 通了 → 清零
    if cnt >= THRESHOLD:
        removed.append((raw, "连续不合格", cnt))
        continue
    if cnt > 0:
        new_state[n] = cnt
    active.append(raw)

with open(CAND, "w") as f:
    f.write("\n".join(active).rstrip("\n") + "\n")
json.dump(new_state, open(STATE, "w"), indent=0, sort_keys=True)

with open(TODAY, "w") as f:
    if removed:
        f.write(f"本次移除 {len(removed)} 个中继：<br>")
        for raw, why, cnt in removed[:MAX_LIST]:
            if cnt is None:
                f.write(f"<code>{raw}</code>（{why}，即时淘汰）<br>")
            else:
                f.write(f"<code>{raw}</code>（{why}，连续 {cnt} 天）<br>")
        if len(removed) > MAX_LIST:
            f.write(f"……其余 {len(removed) - MAX_LIST} 个不再逐一列出，完整列表见附件 rank.csv。<br>")
    else:
        f.write("本次没有中继被淘汰。")

n_active = sum(1 for l in active if l.strip() and not l.strip().startswith("#"))
print(f"candidates active: {n_active}; pruned today: {len(removed)}; state tracked: {len(new_state)}")
