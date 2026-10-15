# eqoa-reportd — problem reports and the report board

The other end of the client's **F3 Report a Problem** dialog. `POST /report`
stores what a player chose to send; `GET /board` is a public, read-only page of
those reports and what became of each.

It is a port of the problem-report route and report board in Dab's Mod's
`pdghostd` (`tools/pdghostd/pdghostd.py` in the perfect-dark repo), with the same
rules: no account, nothing sent unless the player pressed Send, every field
bounded and stripped of control characters, a report on disk as files read over
ssh, and a board that only ever reads back the public part of each report.
Stdlib only; `go test ./cmd/reportd` ports pdghostd's own report and board tests.

## Behind nginx

The service listens on loopback (`-listen`, default `127.0.0.1:9091`) and trusts
`X-Real-IP` / the rightmost `X-Forwarded-For` for its rate limits, so nothing may
reach it except through the proxy. The prefix is stripped; every link the board
writes is relative, so it works under any prefix.

```nginx
# eqoa-reportd (go-eqoa-client-v1/cmd/reportd): the F3 problem reports and
# their board. ^~ so the static-asset regex location never sees
# /board/shot/<id>.png, and the body limit clears the route's own 8 MiB.
location ^~ /eqoa/reports/ {
    proxy_pass http://127.0.0.1:9091/;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    client_max_body_size 8m;
}
```

It must sit in the same server block as `location /eqoa/`, whose `auth_basic`
it overrides by being the longer prefix. `client_max_body_size` must be at least
8m, or a report with a picture is refused by the proxy with a 413 the service
never sees.

## A report on disk

`POST /report` takes `{report, note, name, version, platform, channel,
screenshot}`, where `screenshot` is a base64 PNG or empty. Each report is two
files in `~/eqoareports/reports`, named for the second it arrived (UTC) plus
8 random hex digits: `<id>.txt` and `<id>.png`. The picture is written first, so
a `.txt` always has its picture. The `.txt` is a header of what the server
knows (`received`, `from`, `version`, `platform`, `channel`, `screenshot`,
`name`, `note` on one line), a blank line, then the client's report.

```sh
ls -t ~/eqoareports/reports | head
head -12 ~/eqoareports/reports/20261015-183002-1a2b3c4d.txt
scp 'sdg@10.8.0.1:~/eqoareports/reports/20261015-183002-1a2b3c4d.*' .
```

There is no route that reads a report back, only the board below.

**Bounds** (pdghostd's): 8 MiB body, 512 KiB of report text, 1000 characters of
note, 64 of name (one line), 64 each of version/platform/channel, 5 MiB of
picture and only a real PNG; 120 reports an hour from one address (429); 10000
reports in the directory (507), counted by the shape of a report's name so
`status.txt` is not one. A report whose text is under 32 characters is refused.
Control characters other than newline and tab are stripped, because a report is
read in a terminal.

## The board

`GET /board` (<https://texturepacks.art/eqoa/reports/board>): the reports, newest
first, 25 a page and grouped by day, and a **Fixed** column. Filters are query
parameters: `?name=<tester>`, `?status=<word>`, `?id=<id or stamp>`,
`?page=<n>`. `GET /board.json` is the same as data, and
`/board/shot/<id>.png` / `/board/thumb/<id>.png` the picture and a 320-wide
copy (made once, kept in `~/eqoareports/reportthumbs/`). Full pictures are
limited to 300 an hour an address.

What it shows of a report is the time, the name (`-` or none is "anonymous"),
the note, the picture, the build and the OS from `platform` — never `from:`, the
dump, the settings or the log. Every string is escaped and the page carries a
`Content-Security-Policy` that runs no script. Headers are re-read when the
directory's mtime moves and `status.txt` when its mtime or size does, so an edit
shows on the next request with no restart.

### status.txt

`~/eqoareports/reports/status.txt`, edited by hand at each pass over the
reports. No file, or no line for a report, means *Received*. One report a line:

    <report id or stamp> <status> [<when>] <public comment>

    20261015-183002-1a2b3c4d working Looking at it - Freeport gate collision
    20261015-183510-9f8e7d6c fixed 2026-10-18 Fixed in dev-abc1234 - the gate no longer traps you
    20261015-190244-0011aabb duplicate 20261015-183002-1a2b3c4d - same gate
    20261016-101500 needinfo Which zone was this?

Words: `received`, `working` (*In progress*), `fixed`, `feature` (*Feature
queue*), `needinfo`, `notabug`, `wontfix`, `duplicate` (comment starts with the
other report's id; it joins that report's fix), and `hidden` (off the board
entirely; the report is kept). A stamp without the hex names every report sent
that second; a full id's line beats a stamp's, a later line beats an earlier
one, a line with a bad id or unknown word is skipped, and `#` starts a comment.
The comment is public: plain words, never an address or path. Report ids in a
comment become links.

`<when>` is for `fixed`: `YYYY-MM-DD`, the day it shipped. Reports fixed by the
same change get the identical date and comment, and the Fixed column shows them
as one fix crediting each reporter's name once.

## Deploying

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o eqoa-reportd ./cmd/reportd
scp eqoa-reportd cmd/reportd/eqoa-reportd.service sdg@10.8.0.1:
ssh sdg@10.8.0.1 'mkdir -p -m 700 ~/eqoareports &&
  sudo cp eqoa-reportd.service /etc/systemd/system/ && sudo systemctl daemon-reload &&
  sudo systemctl enable --now eqoa-reportd && sudo systemctl restart eqoa-reportd'
curl -s https://texturepacks.art/eqoa/reports/ping
```

The unit carries pdghostd's walls (`ProtectHome=tmpfs` with only the binary and
`~/eqoareports` bound in), so `~/eqoareports` must exist before it starts.

## Left out, against pdghostd

- **Patch notes.** pdghostd's Fixed column can take its words and credits from
  the game's `patchnotes.txt` (`fixed 1.17 …`). The client has no such file; a
  `<batch>.<line>` here is read as a fix with no date.
- **The monthly archive** (`--archive`, a systemd timer tarring reports older
  than 30 days). Not needed until the directory is large; the 10000 cap is the
  backstop until then.
