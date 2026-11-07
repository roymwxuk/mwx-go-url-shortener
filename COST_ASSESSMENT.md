# Traffic Cost Assessment: mwx-go-url-shortener

_Prepared 2026-11-02. These are estimates, not a quote._

## Scope

This covers **costs that grow with traffic only**. Fixed costs are excluded because the infrastructure already exists and is paid for:

- EC2 instance hours, RDS/Aurora instance hours, and RDS storage GB-month
- API Gateway setup, VPC, and any existing load balancer or NAT hourly charges

The cost lines that grow with each request are:

| # | Cost line | What drives it |
|---|-----------|----------------|
| 1 | API Gateway requests | every request (redirects + API calls) |
| 2 | Data transfer out (DTO) to the internet | response bytes, mostly small 302 redirects |
| 3 | Database I/O | the click-counter `UPDATE` on every redirect, plus inserts on create |
| 4 | CloudWatch Logs ingestion | the app logs one line per request (`logRequests` middleware); API Gateway access logs, if enabled |
| 5 | Cross-AZ transfer between EC2 and RDS | each DB round trip, if EC2 and RDS are in different AZs |

## Pricing used (us-east-1)

| Item | Rate |
|------|------|
| API Gateway **HTTP API** | $1.00 / million requests for the first 300M, then $0.90 / million |
| API Gateway **REST API** | $3.50 / million for the first 333M, $2.80 / million for the next 667M, then $2.38 / million |
| DTO to the internet | $0.09 / GB for the first 10 TB (100 GB/month free per account) |
| Aurora **Standard** I/O | $0.20 / million I/Os |
| Aurora **I/O-Optimized** or RDS PostgreSQL on gp3 | $0 per I/O (already covered by fixed cost) |
| CloudWatch Logs ingestion (Standard) | $0.50 / GB (5 GB/month free) |
| Cross-AZ transfer | $0.01 / GB in each direction |

> **Check these against your region.** If you run in an APAC region such as Tokyo, Singapore, or Seoul, request and DTO rates are usually **about 10–30% higher** than us-east-1. Also confirm which API Gateway type you use (HTTP or REST) and which database you use (Aurora Standard, Aurora I/O-Optimized, or plain RDS). Those two choices change the result the most.

## Assumptions per request

| Parameter | Value | Notes |
|-----------|-------|-------|
| Mix | 90% redirects (`GET /{code}`), 10% API calls (create/get) | Typical for a shortener: links get many more reads than creates |
| Average response on the wire | ~1.2 KB | A 302 with headers is about 0.4 KB; amortized TLS handshakes and TCP overhead make up the rest |
| DB I/O per request | ~1 billed I/O | Reads are mostly served from the buffer pool. Each redirect writes one row (`clicks = clicks + 1`) |
| App log line | ~150 bytes | One slog text line per request |
| EC2↔RDS payload | ~1 KB per round trip | Counted as if the round trip always crosses AZs (upper bound) |

**Free tiers are ignored.** The 100 GB DTO allowance and the 5 GB log allowance apply to the whole account, so your existing workloads have most likely used them already.

## Unit cost: per 1 million requests

| Cost line | HTTP API | REST API |
|-----------|---------:|---------:|
| API Gateway requests | $1.00 | $3.50 |
| DTO (1.2 GB × $0.09) | $0.11 | $0.11 |
| Aurora Standard I/O (1M × $0.20/M) | $0.20 | $0.20 |
| CloudWatch Logs (0.15 GB × $0.50) | $0.08 | $0.08 |
| Cross-AZ (≈2 GB × $0.01) | $0.02 | $0.02 |
| **Total per 1M requests** | **≈ $1.40** | **≈ $3.90** |
| …with RDS gp3 or Aurora I/O-Optimized | ≈ $1.20 | ≈ $3.70 |

**API Gateway is about 70–90% of the traffic cost.** Everything else adds up to roughly $0.40 per million requests.

## Monthly scenarios

Average RPS is the monthly total divided by about 2.6M seconds. Peak traffic is often 5–10× the average.

| Monthly requests | Avg RPS | HTTP API + Aurora Std | REST API + Aurora Std | HTTP API + RDS gp3 |
|-----------------:|--------:|----------------------:|----------------------:|-------------------:|
| 1M | 0.4 | $1.40 | $3.90 | $1.20 |
| 10M | 4 | $14 | $39 | $12 |
| 100M | 39 | $140 | $390 | $120 |
| 1B | 386 | $1,333 | $3,436 | $1,133 |

How the 1B / HTTP API row is calculated:
- API Gateway: 300M × $1.00/M + 700M × $0.90/M = $930
- DTO: 1,200 GB × $0.09 = $108
- Aurora I/O: $200
- Logs: 150 GB × $0.50 = $75
- Cross-AZ: $20

Total: **≈ $1,333**.

## Hidden costs to watch

1. **NAT Gateway ($0.045/GB processed).** The app makes no outbound calls, but traffic can still pass through a NAT by accident. For example, EC2 in a private subnet might reach RDS through its *public* endpoint, or API Gateway might reach EC2 through a public IP. Keep EC2→RDS traffic on private endpoints.
2. **API Gateway → EC2 integration path.** With an HTTP API **VPC Link** (to an ALB, NLB, or Cloud Map), traffic stays private. With a public EC2 endpoint, you may pay EC2 data-out on the response leg as well. REST API private integration needs an NLB, which adds per-LCU charges.
3. **Access logs and metrics.** Turning on API Gateway access logs in addition to the app logs roughly **doubles or triples** the CloudWatch line (≈ $0.15–0.25 per million requests). Detailed per-route CloudWatch metrics are billed per metric, not per request, so they stay small.
4. **Bots and abuse.** A shortener's public `GET /{code}` route attracts scanners. Every 404 is still a billed API Gateway request. Use throttling and usage plans, and add WAF if needed. WAF costs $0.60 per million requests, so it only pays off if it blocks a meaningful share of junk.
5. **Database write pressure.** At about 400+ RPS, one `UPDATE` per click becomes the bottleneck before it becomes a cost problem. It can force a bigger instance, which is a fixed-cost step.

## Ways to reduce cost (biggest savings first)

| Change | Effect | Trade-off |
|--------|--------|-----------|
| **Use an HTTP API, not a REST API** | **−64%** of total traffic cost | Loses REST-only features such as API keys/usage plans, request validation, and caching |
| Serve `GET /{code}` through an ALB or CloudFront instead of API Gateway; keep API Gateway only for `/api/v1/*` | Removes about 90% of API Gateway requests | ALB LCU or CloudFront request fees replace them; more moving parts |
| **Batch click counts** in memory and flush every N seconds (`UPDATE ... SET clicks = clicks + $n`) | Cuts DB write I/O by 10–100× | A crash loses up to N seconds of click counts |
| Sample or turn off the per-request app log in production; keep error logs | Removes about $0.08 per million requests | Less request-level visibility. Use API Gateway metrics instead |
| Return `301` + `Cache-Control` for links that never change | Browsers stop repeat requests | Click stats undercount; changing the target later is hard |
| Aurora I/O-Optimized (if on Aurora Standard) | Removes per-I/O charges | Higher instance and storage rates. Only worthwhile if I/O is more than 25% of the Aurora bill |

## Abuse protection (cost defense)

A public shortener will be flooded and scanned. Protection works in layers, and **only the edge layer prevents billing**:

| Layer | What it does | Stops billing? | Cost |
|-------|--------------|----------------|------|
| AWS Shield Standard | L3/L4 DDoS protection (SYN floods etc.) | yes, at the network level | free, always on |
| **AWS WAF rate-based rule** | Blocks an IP over N requests per 1–10 minute window. Managed rule groups add bot and known-bad-IP lists | Mostly. WAF still charges $0.60 per million requests inspected, but that's less than API Gateway's $1.00 per million, and the blocked requests never reach EC2 or the DB | $5 per web ACL per month + $1 per rule per month + $0.60 per million requests |
| API Gateway throttling | Caps requests per second for a stage or route | Limited help. HTTP API throttling is **not per client**, so one attacker can use up the cap for everyone | free |
| **App-level limits (built in)** | Per-IP limits on redirect and API routes, a penalty for repeated 404s, malformed codes rejected without a DB query, blocked destination hosts | **No**, the request is already billed upstream | free |
| AWS Budgets + CloudWatch alarm on request count | Alerts you when spend or traffic spikes | No, but it catches problems within hours instead of at month end | about free |

Notes:
- **WAF attaches to an ALB, CloudFront, or a REST API, but not to an HTTP API.** Another reason to serve the redirect path through an ALB if you need edge rate limiting.
- With WAF in place, the worst case from a single-source flood is about **$0.60 per million blocked requests** plus small LCU charges, instead of an unbounded API Gateway, EC2, and DB bill.
- App limits are **per instance**. With N instances the effective per-IP limit is N times higher. That's fine as a backstop; if you need exact global limits, use WAF.
- Users behind shared NAT (offices, mobile carriers) share one IP. Raise `RATE_REDIRECT_*` if real users get 429s.

## Bottom line

- With the most likely setup (**HTTP API** plus RDS/Aurora), traffic costs **about $1.20–1.40 per million requests**. That is **under $15/month at 10M requests** and about **$140/month at 100M**.
- With a **REST API**, costs are about **2.8× higher**. If you're on REST now, switching the redirect path is the most effective single change.
- DTO, logs, and DB I/O together stay **below $0.40 per million requests**. They matter only at billion-request scale or when a hidden-cost item above applies.

## Sources

- [AWS WAF Pricing](https://aws.amazon.com/waf/pricing/)

- [Amazon API Gateway Pricing](https://aws.amazon.com/api-gateway/pricing/)
- [EC2 On-Demand Pricing (Data Transfer)](https://aws.amazon.com/ec2/pricing/on-demand/)
- [Amazon VPC Pricing (NAT Gateway, cross-AZ)](https://aws.amazon.com/vpc/pricing/)
- [Amazon Aurora Pricing](https://aws.amazon.com/rds/aurora/pricing/)
- [Planning I/O in Amazon Aurora](https://aws.amazon.com/blogs/database/planning-i-o-in-amazon-aurora/)
- [Amazon CloudWatch Pricing](https://aws.amazon.com/cloudwatch/pricing/)
