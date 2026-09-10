#!/bin/bash
# sweep.sh v3.1 — TURN 中继海选（GitHub Actions / 任意 Linux）中文输出版
# 每台中继测：出口IP+纯净度(IPPure) → Google 单请求（可达性+大陆跳转判定）→ 并发突发 → 带宽
# 判定: 正常 = Google 200 且无大陆跳转；大陆判定 = 被 Google 判定中国大陆（当天直接移除候选）
# 凭据：仅使用 candidates.txt 中显式书写的账密（turn://user:pass@ 格式），无任何内置字典
# 用法: bash sweep.sh [candidates.txt] [rank.csv] [workers]
set -u
CAND=${1:-candidates.txt}
OUT=${2:-rank.csv}
W=${3:-16}
BASE=9400
WORK=$(mktemp -d)
mkdir -p "$WORK"
trap 'pkill -f "turnbridge -listen 127.0.0.1:94" 2>/dev/null' EXIT

# ---------- 归一化候选 ----------
while IFS= read -r line; do
  line=$(printf '%s' "$line" | tr -d '\r' | sed 's/#.*//' | xargs 2>/dev/null)
  [ -z "$line" ] && continue
  case "$line" in
    turn://*) printf '%s\n' "$line" ;;
    *\|*)
      hp=${line%%|*}; up=${line#*|}; u=${up%%:*}; p=${up#*:}
      printf 'turn://%s:%s@%s\n' "$u" "$p" "$hp" ;;
    *) printf 'turn://%s\n' "$line" ;;
  esac
done < "$CAND" > "$WORK/cands.txt"
TOTAL=$(wc -l < "$WORK/cands.txt")
echo "candidates: $TOTAL"
[ "$TOTAL" -eq 0 ] && { echo "no candidates"; exit 1; }
split -n "l/$W" -d "$WORK/cands.txt" "$WORK/shard_"

test_one() {
  local URI=$1 LP=$2 OUTW=$3
  local rest creds U P hostport host port
  rest=${URI#turn://}
  if [[ "$rest" == *@* ]]; then
    creds=${rest%%@*}; hostport=${rest#*@}
    U=${creds%%:*}; P=${creds#*:}
  else
    hostport=$rest; U=""; P=""
  fi
  host=${hostport%%:*}; port=${hostport##*:}

  # TCP 预检，死中继快速跳过
  if ! timeout 3 bash -c "</dev/tcp/$host/$port" 2>/dev/null; then
    echo "$URI,不通,,,,,,,,," >> "$OUTW"
    return
  fi

  local args=(-listen "127.0.0.1:$LP" -server "$hostport" -allocs 1 -max 8)
  [ -n "$U" ] && args+=(-u "$U" -p "$P")
  ./turnbridge "${args[@]}" > "$WORK/tb_$LP.log" 2>&1 &
  local TPID=$!
  sleep 0.8

  # 出口 IP + 延迟 + 纯净度（IPPure 公开接口，无需 key；失败回退 api.ip.sb）
  local R EXITIP LAT FSCORE RESI BCAST
  R=$(curl -s --max-time 8 -x "socks5h://127.0.0.1:$LP" -w '|%{time_total}' https://my.ippure.com/v1/info 2>/dev/null | tr -d '\n')
  EXITIP=$(printf '%s' "$R" | sed -nE 's/.*"ip":[ ]?"([^"]+)".*/\1/p')
  FSCORE=$(printf '%s' "$R" | sed -nE 's/.*"fraudScore":[ ]?([0-9]+).*/\1/p')
  RESI=$(printf '%s' "$R" | sed -nE 's/.*"isResidential":[ ]?(true|false).*/\1/p')
  BCAST=$(printf '%s' "$R" | sed -nE 's/.*"isBroadcast":[ ]?(true|false).*/\1/p')
  LAT=${R##*|}
  if [ -z "$EXITIP" ]; then
    R=$(curl -s --max-time 6 -x "socks5h://127.0.0.1:$LP" -w '|%{time_total}' https://api.ip.sb/ip 2>/dev/null | tr -d '\n')
    EXITIP=${R%%|*}; LAT=${R##*|}
  fi
  if [ -z "$EXITIP" ]; then
    local ST=需要账密
    grep -q "relay returned 401" "$WORK/tb_$LP.log" 2>/dev/null && ST=需要账密
    grep -q "dial relay" "$WORK/tb_$LP.log" 2>/dev/null && ST=不通
    echo "$URI,$ST,,,,,,,,," >> "$OUTW"
    kill $TPID 2>/dev/null; wait $TPID 2>/dev/null
    return
  fi

  # 单请求同时测：Google 可达性(http_code) + 大陆判定(redirect_url 跳 HK)
  local GRAW GCODE GLOC
  GRAW=$(curl -s --max-time 8 -x "socks5h://127.0.0.1:$LP" -o /dev/null -w '%{http_code}|%{redirect_url}' https://www.google.com/ 2>/dev/null)
  GCODE=${GRAW%%|*}
  GLOC=${GRAW#*|}
  case "$GLOC" in
    *google.com.hk*) GLOC=大陆判定 ;;
    "") GLOC=无 ;;
  esac

  # 被判中国大陆 → 当场淘汰，不再测带宽/并发
  if [ "$GLOC" = "大陆判定" ]; then
    echo "$URI,$EXITIP,$LAT,$GCODE,大陆判定,,0,${FSCORE:-},${RESI:-},${BCAST:-}" >> "$OUTW"
    echo "[$(date +%T)] 大陆判定 $hostport (直接淘汰)"
    kill $TPID 2>/dev/null; wait $TPID 2>/dev/null
    return
  fi

  # Google 不通 → 判死
  if [ "$GCODE" != "200" ]; then
    echo "$URI,$EXITIP,$LAT,$GCODE,$GLOC,,0,${FSCORE:-},${RESI:-},${BCAST:-}" >> "$OUTW"
    echo "[$(date +%T)] Google不可达 $hostport"
    kill $TPID 2>/dev/null; wait $TPID 2>/dev/null
    return
  fi

  # 2 并发突发耐受
  local i P_OK=0
  for i in 1 2; do
    curl -s --max-time 6 -x "socks5h://127.0.0.1:$LP" -o /dev/null -w '%{http_code}' https://www.google.com/generate_204 2>/dev/null > "/tmp/burst_${LP}_$i" &
  done
  local p1=$! p2=$!
  wait $p1 $p2
  for i in 1 2; do [ "$(cat "/tmp/burst_${LP}_$i" 2>/dev/null)" = "204" ] && P_OK=$((P_OK+1)); done

  # 带宽（512KB）
  local BW
  BW=$(curl -s --max-time 10 -x "socks5h://127.0.0.1:$LP" -o /dev/null -w '%{speed_download}' "https://speed.cloudflare.com/__down?bytes=500000" 2>/dev/null)
  BW=${BW%%.*}

  echo "$URI,$EXITIP,$LAT,$GCODE,$GLOC,$P_OK,${BW:-0},${FSCORE:-},${RESI:-},${BCAST:-}" >> "$OUTW"
  echo "[$(date +%T)] 正常 $hostport bw=${BW:-0}"
  kill $TPID 2>/dev/null
  wait $TPID 2>/dev/null
}

worker() {
  local SHARD=$1 WID=$2
  local LP=$((BASE + WID))
  local OUTW="$WORK/out.$WID.csv"
  : > "$OUTW"
  while IFS= read -r URI; do
    [ -z "$URI" ] && continue
    test_one "$URI" "$LP" "$OUTW"
  done < "$SHARD"
}

for w in $(seq 0 $((W-1))); do
  sf=$(printf '%s/shard_%02d' "$WORK" "$w")
  [ -f "$sf" ] || continue
  worker "$sf" "$w" &
done
wait

# ---------- 合并 ----------
HDR="中继,出口IP,延迟秒,Google状态码,Google跳转,并发,带宽Bps,风控值,纯净度,是否家宽,是否原生,国家,城市,运营商,AS,代理标记,机房标记,判定"
cat "$WORK"/out.*.csv > "$WORK/merged.csv"
ALIVE=$(tail -n +2 "$WORK/merged.csv" | grep -cvE ',(不通|需要账密|不可达|大陆判定),' || true)
echo "存活: $ALIVE / $TOTAL"

# ---------- IP 纯净度补全（ip-api 批量：归属/运营商/代理标记） ----------
python3 - "$WORK/merged.csv" "$OUT" "$HDR" <<'PYEOF'
import csv, json, sys, time, urllib.request
inp, outp, hdr = sys.argv[1], sys.argv[2], sys.argv[3].split(",")
rows = [r for r in csv.reader(open(inp)) if r]
ips = sorted({r[1] for r in rows if len(r) > 1 and r[1].replace(".", "").isdigit()})
geo = {}
for i in range(0, len(ips), 15):
    chunk = ips[i:i+15]
    req = urllib.request.Request(
        "http://ip-api.com/batch?fields=status,countryCode,city,isp,org,as,proxy,hosting,query",
        data=json.dumps(chunk).encode(),
        headers={"Content-Type": "application/json"})
    try:
        for r in json.load(urllib.request.urlopen(req, timeout=20)):
            geo[r.get("query")] = r
    except Exception as e:
        print("geo batch failed:", e, file=sys.stderr)
    time.sleep(1.2)

def flag(v):
    return "是" if v in (True, "true", "True") else "否"

w = csv.writer(open(outp, "w", newline="", encoding="utf-8-sig"))
w.writerow(hdr)
for r in rows:
    g = geo.get(r[1], {}) if len(r) > 1 else {}
    code = r[3] if len(r) > 3 else ""
    loc = r[4] if len(r) > 4 else ""
    if loc == "大陆判定":
        verdict = "大陆判定"
    elif code == "200" and loc == "无":
        verdict = "正常"
    elif code == "200":
        verdict = "跳转异常"
    else:
        verdict = "不通"
    w.writerow(r + [g.get("countryCode", ""), g.get("city", ""), g.get("isp", ""),
                    g.get("as", ""), flag(g.get("proxy")), flag(g.get("hosting")), verdict])
PYEOF

echo "=== 判定统计 ==="
awk -F, 'NR>1 {c[$NF]++} END {for (v in c) print v, c[v]}' "$OUT" | sort -rn
echo "=== TOP 15（仅判定=正常，按带宽） ==="
head -1 "$OUT"
awk -F, 'NR>1 && $NF=="正常"' "$OUT" | sort -t, -k7,7nr | head -15
echo "=== DONE ==="
