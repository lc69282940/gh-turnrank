# turnrank — TURN 中继海选

600+ 台中继的海选在 GitHub 上跑，VPS 只负责复核冠军和正式服务。

## 文件

- `turnbridge.go` — TURN→SOCKS5 桥（测试引擎本体）
- `sweep.sh` — 海选脚本：存活 → 延迟 → Google/Gemini 探测 → 并发突发 → 带宽 → IP 纯净度
- `candidates.txt` — 候选名单（把你的 600+ 条粘进来）
- `.github/workflows/sweep.yml` — 每周一自动跑 + 支持手动触发

## candidates.txt 格式

```
turn://1.2.3.4:3478                  # 无密码
turn://user:pass@1.2.3.4:3478        # 带账号密码
1.2.3.4:3478                         # 裸地址也认
1.2.3.4:3478|user|pass
```

## rank.csv 列说明（纯净度看这些）

| 列 | 含义 | 判定 |
|---|---|---|
| exit_ip | 通过中继看到的出口 IP | DEAD/AUTH_REQUIRED/UNREACHABLE = 淘汰 |
| latency_s | 出口请求延迟 | 越低越好 |
| google204 | Google 可达性 | 204 = 通 |
| google_redirect | google.com 跳转 | **CN_REDIRECT = 被判中国大陆（淘汰）** |
| gemini_code / gemini_blocked | Gemini 探测 | 200 + no = Gemini 可用 |
| burst_ok | 2 并发突发成功数 | 2 = 能扛并发 |
| bw_Bps | 带宽 B/s | 越大越好，<100000 淘汰 |
| country / city / isp / as | 归属地 | country 必须在 Gemini 支持区（US/BR/JP…），CN/HK/MO/RU 淘汰 |
| proxy_flag / hosting_flag | ip-api 风控标记 | true = 机房/代理段，纯净度扣分项 |
| verdict | 综合判定 | **PASS = Google 通 + Gemini 通**；PARTIAL = 只通 Google |

**选型规则**：verdict=PASS 里面，按 bw 排序， country 支持区， proxy_flag=false 优先。

## 注意
测出的延迟与 VPS 视角不同——**带宽、存活、纯净度可信，延迟仅做横向参考**。
