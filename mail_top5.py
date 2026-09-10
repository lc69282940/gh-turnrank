#!/usr/bin/env python3
# mail_top5.py — 海选结束后把 Top5 表格 + 全量 rank.csv 附件发到邮箱（中文列版）
# 凭据来自 GitHub Secrets: SMTP_HOST / SMTP_PORT / SMTP_USER / SMTP_PASS / MAIL_TO
# 列布局对应 sweep.sh v3.1（18 列，全中文表头）
import csv, os, smtplib, ssl, sys
from datetime import datetime, timedelta, timezone
from email.mime.application import MIMEApplication
from email.mime.multipart import MIMEMultipart
from email.mime.text import MIMEText

RANK = sys.argv[1] if len(sys.argv) > 1 else "rank.csv"
host = os.environ.get("SMTP_HOST", "")
port = int(os.environ.get("SMTP_PORT", "465"))
user = os.environ.get("SMTP_USER", "")
pwd = os.environ.get("SMTP_PASS", "")
to = os.environ.get("MAIL_TO", user)

if not (host and user and pwd):
    print("SMTP secrets not configured (SMTP_HOST/SMTP_USER/SMTP_PASS); skip email")
    sys.exit(0)

tz = timezone(timedelta(hours=8))
now = datetime.now(tz).strftime("%Y-%m-%d %H:%M")


def send(subject, html):
    msg = MIMEMultipart()
    msg["Subject"] = subject
    msg["From"] = user
    msg["To"] = to
    msg.attach(MIMEText(html, "html", "utf-8"))
    if os.path.exists(RANK):
        with open(RANK, "rb") as f:
            att = MIMEApplication(f.read())
            att.add_header("Content-Disposition", "attachment", filename="rank.csv")
            msg.attach(att)
    ctx = ssl.create_default_context()
    if port == 465:
        srv = smtplib.SMTP_SSL(host, port, context=ctx)
    else:
        srv = smtplib.SMTP(host, port)
        srv.starttls(context=ctx)
    srv.login(user, pwd)
    srv.sendmail(user, [to], msg.as_string())
    srv.quit()
    print(f"email sent to {to}: {subject}")


def fnum(s):
    try:
        return float(s)
    except (TypeError, ValueError):
        return 0.0


if not os.path.exists(RANK):
    send("【TURN海选】运行失败，无 rank.csv",
         "<p>sweep 未产生 rank.csv，请到 Actions 页面查看日志。</p>")
    sys.exit(0)

with open(RANK, newline="") as f:
    rows = [x for x in csv.reader(f) if x]

alive = len([x for x in rows if len(x) > 1 and x[1] not in ("不通", "需要账密", "不可达")])
pass_rows = [x for x in rows if len(x) >= 18 and x[-1] == "正常"]
pass_rows.sort(key=lambda x: fnum(x[6]), reverse=True)
top5 = pass_rows[:5]
cn = len([x for x in rows if len(x) > 4 and x[4] == "大陆判定"])

subject = f"【TURN海选】Top{len(top5)} + 全量排名 {now}（正常 {len(pass_rows)} / 存活 {alive} / 总 {len(rows)} / 大陆判定 {cn}）"

trs = ""
for i, x in enumerate(top5, 1):
    bw_mb = fnum(x[6]) / 1048576
    trs += (f"<tr><td>{i}</td><td><code>{x[0]}</code></td><td>{x[1]}</td>"
            f"<td>{x[10]}</td><td>{x[11]}</td><td>{x[12]}</td>"
            f"<td>{x[2]}</td><td>{bw_mb:.2f}</td><td>{x[7]}</td><td>{x[3]} / {x[4]}</td>"
            f"<td>{'✅' if x[-1] == '正常' else x[-1]}</td></tr>")

html = f"""
<h3>TURN 中继海选 Top{len(top5)}</h3>
<p>总候选 {len(rows)} · 存活 {alive} · 正常 {len(pass_rows)} · 判定大陆 {cn} · 生成时间 {now}（北京时间）</p>
<table border="1" cellpadding="6" style="border-collapse:collapse;font-family:monospace">
<tr><th>#</th><th>中继</th><th>出口IP</th><th>国家</th><th>城市</th><th>运营商</th>
<th>延迟s</th><th>带宽MB/s</th><th>风控值</th><th>Google(码/跳转)</th><th>判定</th></tr>
{trs}
</table>
<p>附件为全量 rank.csv（含纯净度/是否家宽/是否原生/代理标记）。VPS 侧复核：把 top5 粘进
<code>/root/turn-candidates.txt</code> 跑 <code>turnrank.sh</code>，达标后 <code>turn-swap &lt;冠军&gt;</code>。</p>
"""

if os.path.exists("pruned_today.txt"):
    with open("pruned_today.txt") as f:
        html += "<h4>淘汰榜（连续 3 天不合格，已直接移除）</h4><p>" + f.read() + "</p>"

send(subject, html)
