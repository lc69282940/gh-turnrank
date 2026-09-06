# turnrank — TURN 中继海选（GitHub Actions 版）

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

## 使用

1. 建一个**私有** GitHub 仓库，把这些文件传上去（保持 .github/workflows 目录结构）。
2. 把 600+ 条粘进 candidates.txt。
3. Actions 页面 → turnrank-sweep → Run workflow（手动跑一次）。
4. 跑完两个地方拿结果：Actions 的 Artifacts（rank.csv）和仓库 `rank/rank-latest.csv`（自动提交）。
5. VPS 侧复核冠军（延迟+耐受度必须以 VPS 为起点测）：把 top5 拷进 /root/turn-candidates.txt，跑 `bash /root/turnrank.sh`，达标后 `turn-swap <冠军>`。

## 注意

- 每周跑一次足够；600 条 = 600 次对第三方服务器的探测，别调更高频率，避免被 GitHub 判滥用。
- Actions 的机器在 Azure（美东），测出的延迟与 VPS 视角不同——**带宽、存活、纯净度可信，延迟仅做横向参考**。
- 带账密的中继如果密码错，结果会标 AUTH_REQUIRED。
