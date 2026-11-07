# mwx-go-url-shortener

A URL shortener API written in Go. It uses only the standard library `net/http` plus [pgx](https://github.com/jackc/pgx) for Postgres.

## Storage

| Mode | When | How |
|------|------|-----|
| In-memory | unit tests, quick local runs | default when `DATABASE_URL` is unset |
| Postgres | local runs that should match prod, AWS RDS/Aurora | set `DATABASE_URL` |

The schema lives in `internal/store/migrations` and is applied when the server starts.

## Run

```sh
make run        # in-memory, http://localhost:8080
make run-pg     # starts Postgres in Docker, then runs against it
make test
```

## API

```sh
# Create a link (custom_code is optional)
curl -s -XPOST localhost:8080/api/v1/links -d '{"url":"https://go.dev","custom_code":"golang"}'
# -> {"code":"golang","url":"https://go.dev","clicks":0,"created_at":"...","short_url":"http://localhost:8080/golang"}

curl -i localhost:8080/golang                     # 302 redirect, counts a click
curl -s localhost:8080/api/v1/links/golang        # link details and click count
curl -XDELETE localhost:8080/api/v1/links/golang  # 204
curl -s localhost:8080/healthz
```

## Config (env)

- `ADDR`: listen address (default `:8080`)
- `BASE_URL`: public base URL for building short links (default `http://localhost$ADDR`)
- `DATABASE_URL`: Postgres DSN. For RDS, use `sslmode=require` or a stricter mode.

### Abuse protection

Every limit applies **per client IP** (IPv6 is grouped by /64) and **per instance**. Requests over a limit get `429` with a `Retry-After` header.

| Env | Default | Meaning |
|-----|---------|---------|
| `TRUSTED_PROXY_HOPS` | `0` | Number of proxies that append to `X-Forwarded-For`. **Set to `1` behind an ALB or API Gateway, `2` behind CloudFront plus an ALB.** If this is 0 behind a proxy, all clients share the proxy's IP. If it's set too high, clients can spoof their IP. |
| `RATE_REDIRECT_RPS` / `_BURST` | `10` / `30` | Limit on `GET /{code}` |
| `RATE_NOTFOUND_RPS` / `_BURST` | `0.2` / `10` | Budget of 404s per IP. Once it's used up, the IP gets 429 on every redirect (blocks code enumeration before it reaches the DB) |
| `RATE_API_RPS` / `_BURST` | `1` / `10` | Limit on `/api/v1/*` |
| `BLOCKED_HOSTS` | (empty) | Comma-separated destination hosts that can't be shortened (subdomains are included). The service's own host is always blocked to prevent redirect loops. |

Malformed codes such as `/favicon.ico` or `/wp-login.php` return 404 without querying the database. `/healthz` has no limit, so load balancer health checks keep working.

App-level limits protect the **database and EC2**. The request is **already billed** by API Gateway or the ALB before it reaches the app. To stop floods at the edge, see the "Abuse protection" section in `COST_ASSESSMENT.md`.
