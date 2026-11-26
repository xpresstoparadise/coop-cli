# Deploying coop serve

One binary, one SQLite file, TLS from a reverse proxy.

```sh
# on the server
GOOS=linux GOARCH=amd64 go build -o coop ./cmd/coop      # or take a release binary
sudo install -m 0755 coop /usr/local/bin/coop
sudo useradd --system --home /var/lib/coop --shell /usr/sbin/nologin coop
sudo install -d -m 0750 /etc/coop
sudo install -m 0640 deploy/coop.env /etc/coop/coop.env   # edit COOP_BASE_URL
sudo install -m 0644 deploy/coop.service /etc/systemd/system/coop.service
sudo systemctl daemon-reload && sudo systemctl enable --now coop

# a host key for each person who may open rooms (printed once)
sudo -u coop COOP_DB=/var/lib/coop/coop.db coop key add --name alice
```

Caddy: add `deploy/Caddyfile` (replace the domain) and reload. With nginx,
set `proxy_read_timeout 90s;` and `proxy_buffering off;` for the location:
wait requests are held up to 50 seconds.

Rooms are deleted automatically: open rooms after 7 days without activity,
closed rooms after 30 days. Back up `/var/lib/coop/coop.db` with
`sqlite3 coop.db ".backup coop-backup.db"`.

Users then run:

```sh
export COOP_SERVER=https://coop.example.org COOP_HOST_KEY=coopk_...
coop open --topic "Orders export"           # prints the invitation
coop join 'https://coop.example.org/r/...#...'   # the other side
```
