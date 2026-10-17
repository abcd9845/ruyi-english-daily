#!/usr/bin/env bash
# End-to-end acceptance run for the practice-site server against a MOCK brain.
# Covers the 9 acceptance criteria of Issue #1 plus hardening extras.
# All secrets (family code, token, session secret, ntfy topic) are random
# per-run values; nothing secret is printed or written to disk outside $WORK.
#
#   server/test/acceptance.sh            (after `make build mockbrain`)
#   SHOTS=/some/dir server/test/acceptance.sh   (browser screenshots go there)
#   MAIN_REF=origin/main                  (ref the old pages are compared with)
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"
SRV="$(dirname "$HERE")"
ROOT="$(git -C "$SRV" rev-parse --show-toplevel)"
BIN="$SRV/dist/ruyi-site-linux-amd64"
MOCK="$SRV/dist/mockbrain"
MAIN_REF="${MAIN_REF:-origin/main}"
SHOTS="${SHOTS:-}"
WORK="$(mktemp -d)"
RESULTS="${RESULTS:-$SRV/dist/acceptance-results.txt}"
PORT_MOCK=18190 PORT_TLS=18191 PORT_A=18192 PORT_B=18193 PORT_C=18194 PORT_D=18195
PIDS=()
cleanup() { for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done; rm -rf "$WORK"; }
trap cleanup EXIT
[ -x "$BIN" ] && [ -x "$MOCK" ] || { echo "build first: make build mockbrain"; exit 2; }
mkdir -p "$(dirname "$RESULTS")"; : > "$RESULTS"

rnd() { head -c 18 /dev/urandom | od -An -tx1 | tr -d ' \n'; }
CODE="fc-$(rnd)"; CODE2="fc-$(rnd)"; SECRET="$(rnd)$(rnd)"; TOKEN="tok-$(rnd)"; TOPIC="tp-$(rnd)"
SOURCE="math-intake.zz-yuekao-numberline-4y"
PASS=0; FAIL=0
say() { echo "$*" | tee -a "$RESULTS"; }
ok() { PASS=$((PASS+1)); say "PASS  $1"; }
bad() { FAIL=$((FAIL+1)); say "FAIL  $1${2:+ — $2}"; }
expect() { # expect "name" actual wanted
  if [ "$2" = "$3" ]; then ok "$1 (=$2)"; else bad "$1" "got '$2' want '$3'"; fi
}
section() { say ""; say "== $* =="; }

# ---- fixtures -----------------------------------------------------------------
STATIC="$WORK/static"; mkdir -p "$STATIC"
git -C "$ROOT" archive "$MAIN_REF" | tar -x -C "$STATIC"
mkdir -p "$STATIC/m/numberline-4y" "$STATIC/.git"
cp "$ROOT/m/numberline-4y/index.html" "$STATIC/m/numberline-4y/index.html"
echo "JUNK-LOG-MARKER" > "$STATIC/server.log"; echo 1234 > "$STATIC/server.pid"
printf '#!/bin/sh\necho JUNK-SH-MARKER\n' > "$STATIC/start-server.sh"; cp "$STATIC/start-server.sh" "$STATIC/stop-server.sh"
echo "JUNK-ENV-MARKER=1" > "$STATIC/.env"; printf '[core]\n\tJUNK-GIT-MARKER\n' > "$STATIC/.git/config"
# the "old pages": everything on main that the static allowlist serves, except the m/ dynamic pages
# (server code, docs and git housekeeping are not served; m/numberline-4y has its own checks)
MAIN_FILES="$(git -C "$ROOT" ls-tree -r --name-only "$MAIN_REF" | grep -v -E '^(server/|docs/|\.gitignore$|\.github/|m/)')"

start_mock() { # name port [env...]
  local name=$1 port=$2; shift 2
  ( export MOCK_TOKEN="$TOKEN" MOCK_LISTEN="127.0.0.1:$port"; for kv in "$@"; do export "$kv"; done; exec "$MOCK" ) >"$WORK/$name.log" 2>&1 &
  PIDS+=($!); eval "PID_$name=$!"
}
start_site() { # name port brain-url [env...]
  local name=$1 port=$2 brain=$3; shift 3
  ( export RUYI_LISTEN="127.0.0.1:$port" RUYI_STATIC_DIR="$STATIC" RUYI_FAMILY_CODE="${SITE_CODE:-$CODE}" RUYI_SESSION_SECRET="${SITE_SECRET:-$SECRET}" \
           RUYI_COOKIE_SECURE=0 RUYI_BRAIN_BASE_URL="$brain" RUYI_BRAIN_TOKEN="$TOKEN" RUYI_LOG_LEVEL=debug RUYI_NTFY_TOPIC="$TOPIC"
    for kv in "$@"; do export "$kv"; done; exec "$BIN" ) >"$WORK/$name.log" 2>&1 &
  PIDS+=($!); eval "PID_$name=$!"
}
wait_http() { for _ in $(seq 1 60); do curl -s -o /dev/null "$1" 2>/dev/null && return 0; sleep 0.1; done; return 1; }

mkdir -p "$WORK/resp"
get() { # get NAME URL [curl args]  -> sets $CODE_HTTP, files resp/NAME.{h,b}
  local n=$1 u=$2; shift 2
  CODE_HTTP=$(curl -sS --path-as-is -D "$WORK/resp/$n.h" -o "$WORK/resp/$n.b" -w '%{http_code}' "$@" "$u" 2>/dev/null)
}
post_login() { # post_login NAME BASE CODE [jar]
  local n=$1 b=$2 c=$3; shift 3
  CODE_HTTP=$(curl -sS -D "$WORK/resp/$n.h" -o "$WORK/resp/$n.b" -w '%{http_code}' -H 'Content-Type: application/json' \
      -d "{\"code\":\"$c\"}" "$@" "$b/api/login")
}

# ---- start services -------------------------------------------------------------
start_mock mock "$PORT_MOCK"
wait_http "http://127.0.0.1:$PORT_MOCK/_mock/stats" || { echo "mock did not start"; cat "$WORK/mock.log"; exit 2; }
start_site A "$PORT_A" "http://127.0.0.1:$PORT_MOCK"
A="http://127.0.0.1:$PORT_A"
wait_http "$A/healthz" || { echo "site did not start"; cat "$WORK/A.log"; exit 2; }
MSTAT() { curl -s "http://127.0.0.1:$PORT_MOCK/_mock/stats"; }

section "A. Static allowlist / hardening"
get h "$A/healthz"; expect "A1 /healthz 200" "$CODE_HTTP" 200
get root "$A/"; expect "A2 / serves index.html" "$CODE_HTTP" 200
cmp -s "$WORK/resp/root.b" "$ROOT/index.html" && ok "A3 / body == index.html (bytes)" || bad "A3 / body == index.html"
for p in /server.log /server.pid /start-server.sh /stop-server.sh /.env /.git/config /.git/ /.git /../server.log /%2e%2e/server.log /%2E%2E/server.log \
         /..%2fserver.log /units/x/../../server.log /units/ /units /m/ /m /days/../server.log //server.log /%00 /nonexistent; do
  get t "$A$p"; [ "$CODE_HTTP" = 404 ] || { [ "$p" = "/units" ] && false; } && ok "A4 GET $p -> 404" || bad "A4 GET $p" "got $CODE_HTTP"
  grep -q 'JUNK-' "$WORK/resp/t.b" && bad "A4 $p leaked junk marker"
done
get t "$A/days"; expect "A5 /days -> 301" "$CODE_HTTP" 301
get t "$A/days/"; expect "A6 /days/ -> 200 (index)" "$CODE_HTTP" 200
get t "$A/m/numberline-4y/"; expect "A7 dynamic page served" "$CODE_HTTP" 200
get t "$A/index.html" -X POST; expect "A8 POST static -> 405" "$CODE_HTTP" 405
get t "$A/units/u2-2026-09-20/listening.mp3" -H 'Range: bytes=0-99'; expect "A9 mp3 Range -> 206" "$CODE_HTTP" 206

section "B. Criterion 4 — old pages are byte-identical to $MAIN_REF (sha256 of each of the 17 files)"
n=0; badn=0
while IFS= read -r f; do
  want=$(git -C "$ROOT" show "$MAIN_REF:$f" | sha256sum | cut -d' ' -f1)
  get f "$A/$f"; got=$(sha256sum < "$WORK/resp/f.b" | cut -d' ' -f1)
  n=$((n+1))
  if [ "$CODE_HTTP" = 200 ] && [ "$got" = "$want" ]; then say "  ok   $f  ${want:0:16}…"; else badn=$((badn+1)); say "  DIFF $f (status $CODE_HTTP)"; fi
done <<< "$MAIN_FILES"
[ "$n" = 17 ] && [ "$badn" = 0 ] && ok "B1 all $n files served byte-identical" || bad "B1 byte comparison" "files=$n diff=$badn"
get f "$A/units/u2-2026-09-20/"; expect "B2 directory URL with index works (old links)" "$CODE_HTTP" 200

section "C. Criteria 3 & 6 — auth"
get s "$A/api/sets/$SOURCE"; expect "C1 sets without cookie -> 401" "$CODE_HTTP" 401
get s "$A/api/config"; expect "C2 config without cookie -> 401" "$CODE_HTTP" 401
SHA7=$(curl -s "http://127.0.0.1:$PORT_MOCK/_mock/shas?n=7")
get s "$A/api/media/$SOURCE/$SHA7"; expect "C3 media without cookie -> 401" "$CODE_HTTP" 401
get s "$A/api/media/$SOURCE/$SHA7?ticket=forged.ticket.value"; expect "C4 media with forged ticket param, no cookie -> 401" "$CODE_HTTP" 401
get s "$A/api/media/$SOURCE/$SHA7" -H 'Cookie: ruyi_sess=v1.9999999999.00'; expect "C5 media with forged cookie -> 401" "$CODE_HTTP" 401
expect "C6 brain not contacted by any unauthenticated request" "$(MSTAT | jq -r '(.list // 0) + (.ticket // 0) + (.media // 0)')" 0
post_login w1 "$A" "wrong-$(rnd)"; expect "C7 wrong code -> 401" "$CODE_HTTP" 401
grep -qi '^set-cookie:' "$WORK/resp/w1.h" && bad "C8 wrong code must not send Set-Cookie" || ok "C8 wrong code: no Set-Cookie header"
JAR="$WORK/jar"; post_login ok1 "$A" "$CODE" -c "$JAR"; expect "C9 right code -> 200" "$CODE_HTTP" 200
SC=$(grep -i '^set-cookie:' "$WORK/resp/ok1.h" | tr -d '\r')
SC_ATTR=$(echo "$SC" | sed -E 's/^[Ss]et-[Cc]ookie: ruyi_sess=[^;]*/ruyi_sess=<value>/')
say "     Set-Cookie (value elided): $SC_ATTR"
echo "$SC" | grep -q '^[Ss]et-[Cc]ookie: ruyi_sess=' && ok "C10 cookie name ruyi_sess" || bad "C10 cookie name"
echo "$SC" | grep -qi '; HttpOnly' && ok "C11 HttpOnly" || bad "C11 HttpOnly"
echo "$SC" | grep -qi '; SameSite=Lax' && ok "C12 SameSite=Lax" || bad "C12 SameSite=Lax"
echo "$SC" | grep -qi '; Path=/\(;\|$\)' && ok "C13 Path=/" || bad "C13 Path=/"
echo "$SC" | grep -qi '; Secure' && bad "C14 Secure must be off when RUYI_COOKIE_SECURE=0" || ok "C14 no Secure when RUYI_COOKIE_SECURE=0"
echo "$SC" | grep -qi 'Max-Age=2592000' && ok "C15 Max-Age = 720h (30 days)" || bad "C15 Max-Age"
COOKIE=$(awk '$6=="ruyi_sess"{print $7}' "$JAR")
get s "$A/api/session" -b "$JAR"; grep -q '"authenticated":true' "$WORK/resp/s.b" && ok "C16 /api/session authenticated with cookie" || bad "C16 session"
get s "$A/api/session"; grep -q '"authenticated":false' "$WORK/resp/s.b" && ok "C17 /api/session anonymous = false" || bad "C17 session anon"
# tampered cookie
T="${COOKIE%?}$( [ "${COOKIE: -1}" = a ] && echo b || echo a )"
get s "$A/api/config" -H "Cookie: ruyi_sess=$T"; expect "C18 tampered cookie -> 401" "$CODE_HTTP" 401
get s "$A/api/config" -b "$JAR"; expect "C19 genuine cookie -> 200" "$CODE_HTTP" 200
CONFIG_BODY="$(cat "$WORK/resp/s.b")"
echo "$CONFIG_BODY" | grep -q "https://ntfy.sh/$TOPIC" && ok "C20 /api/config hands the ntfy url to a logged-in browser" || bad "C20 config ntfy"
# logout
get s "$A/api/logout" -X POST -b "$JAR" -c "$WORK/jar-out"; expect "C21 logout -> 200" "$CODE_HTTP" 200
grep -qi 'Max-Age=0\|max-age=0\|01 Jan 1970' "$WORK/resp/s.h" && ok "C22 logout clears the cookie" || bad "C22 logout clears cookie"

section "D. Criteria 1, 5 — data/images through the proxy"
get sets "$A/api/sets/$SOURCE" -b "$JAR"; expect "D1 /api/sets 200" "$CODE_HTTP" 200
SETS="$WORK/resp/sets.b"
expect "D2 60 questions" "$(jq '.items|length' "$SETS")" 60
expect "D3 ordinals 1..60 in order" "$(jq -r '[.items[].ordinal]==[range(1;61)]' "$SETS")" true
# Real numbers look like "1(1)", "11(2)③": the site must sort by (major, minor, suffix), not trust list order.
echo "Authorization: Bearer $TOKEN" > "$WORK/mockhdr"
curl -s -H @"$WORK/mockhdr" "http://127.0.0.1:$PORT_MOCK/v1/bank/questions?source=$SOURCE&limit=500" > "$WORK/rawlist.json"
RAWKEYS='[.items[].number|capture("^(?<a>\\d+)\\((?<b>\\d+)\\)(?<c>.*)$")|[(.a|tonumber),(.b|tonumber),.c]]'
expect "D4a mock list is NOT in number order (scrambled like updated_at DESC)" "$(jq -r "$RAWKEYS"' as $k | $k != ($k|sort)' "$WORK/rawlist.json")" true
expect "D4b site output IS sorted by (major, minor, suffix)" "$(jq -r "$RAWKEYS"' as $k | $k == ($k|sort)' "$SETS")" true
expect "D4c 60 distinct numbers, majors 1..18, includes 11(2)③" "$(jq -r '([.items[].number]|unique|length)==60 and ([.items[].number|capture("^(?<a>\\d+)").a|tonumber]|unique|min==1 and max==18) and ([.items[].number]|index("11(2)③") != null)' "$SETS")" true
expect "D4d first/last numbers" "$(jq -c '[.items[0].number,.items[59].number]' "$SETS")" '["1(1)","18(3)"]'
expect "D5 questions with no media = ordinals [17,25,26,27,28]" "$(jq -c '[.items[]|select(.media|length==0)|.ordinal]' "$SETS")" '[17,25,26,27,28]'
expect "D6 questions with >=1 image" "$(jq '[.items[]|select(.media|length>0)]|length' "$SETS")" 55
expect "D6b figure_status passed through (absent key -> unknown)" "$(jq -c '[.items[]|select(.figure_status!="ok")|[.ordinal,.figure_status]]' "$SETS")" '[[17,"missing"],[25,"none"],[26,"unknown"],[27,"unknown"],[29,"none"],[30,"unknown"]]'
expect "D6c figure_status is never empty" "$(jq -r '[.items[]|.figure_status|length>0]|all' "$SETS")" true
expect "D7 image urls are rewritten to /api/media/{source}/{sha}" "$(jq -r '[.items[].media[].url|startswith("/api/media/'"$SOURCE"'/")]|all' "$SETS")" true
expect "D8a items without options omit the key (fill_blank)" "$(jq -r '[.items[30:][]|has("options")]|any' "$SETS")" false
expect "D8b no answer/explanation fields in output" "$(jq -r '[.items[]|keys[]]|map(select(.=="answer" or .=="explanation"))|length' "$SETS")" 0
expect "D8 each item carries a qref" "$(jq -r '[.items[].qref|length>0]|all' "$SETS")" true
expect "D9 choice options passed through (Q1 has A-D)" "$(jq -c '[.items[0].options[].key]' "$SETS")" '["A","B","C","D"]'
expect "D10 Q5 has stem image + option-B image" "$(jq -c '[.items[4].media[]|[.slot,(.option_key // "")]]' "$SETS")" '[["stem",""],["option","B"]]'
# fetch every image
okimg=0; badimg=0
for u in $(jq -r '.items[].media[].url' "$SETS"); do
  CODE_HTTP=$(curl -s -b "$JAR" -D "$WORK/resp/img.h" -o "$WORK/resp/img.b" -w '%{http_code}' "$A$u")
  if [ "$CODE_HTTP" = 200 ] && head -c 8 "$WORK/resp/img.b" | od -An -c | tr -d ' ' | grep -q 'PNG' && grep -qi '^content-type: image/png' "$WORK/resp/img.h"; then okimg=$((okimg+1)); else badimg=$((badimg+1)); fi
done
[ "$badimg" = 0 ] && [ "$okimg" = 56 ] && ok "D11 all $okimg images fetched: 200, PNG magic, Content-Type image/png" || bad "D11 images" "ok=$okimg bad=$badimg"
u7=$(jq -r '.items[6].media[0].url' "$SETS"); get img "$A$u7" -b "$JAR"
grep -qi '^cache-control: private' "$WORK/resp/img.h" && ok "D12 image Cache-Control: private" || bad "D12 Cache-Control"
# the site must never ask the brain for answers: the mock rejects unknown query keys, and include_answer would be one
grep -q 'include_answer' "$SRV/internal/brain/brain.go" && bad "D12b include_answer referenced in client code" || ok "D12b client code never references include_answer / status / other list parameters"
# source / sha injection
for s in '..%2f..%2fv1%2fbank%2fsources' 'a%2fb' "$SOURCE%3Finclude_answer%3Dtrue" 'MATH' 'other.valid-source' '%2e%2e'; do
  get inj "$A/api/sets/$s" -b "$JAR"; [ "$CODE_HTTP" = 404 ] && ok "D13 sets source '$s' -> 404" || bad "D13 sets source '$s'" "got $CODE_HTTP"
done
get inj "$A/api/media/$SOURCE/$(printf 'f%.0s' $(seq 64))" -b "$JAR"; expect "D14 well-formed sha not in source -> 404" "$CODE_HTTP" 404
get inj "$A/api/media/$SOURCE/..%2f..%2fx" -b "$JAR"; expect "D15 sha path traversal -> 404" "$CODE_HTTP" 404

section "E. Criterion 7 — expired/invalid ticket does not break images"
S0=$(MSTAT | jq '.ticket'); B0=$(MSTAT | jq '.media_expired_ticket // 0')
curl -s -X POST "http://127.0.0.1:$PORT_MOCK/_mock/advance-clock?d=1h" >/dev/null   # every cached ticket is now expired upstream
get img "$A$u7" -b "$JAR"
expect "E1 image still 200 after tickets expired upstream" "$CODE_HTTP" 200
head -c 8 "$WORK/resp/img.b" | od -An -c | tr -d ' ' | grep -q PNG && ok "E2 bytes are a PNG" || bad "E2 bytes"
B1=$(MSTAT | jq '.media_expired_ticket // 0'); S1=$(MSTAT | jq '.ticket')
[ "$B1" -gt "$B0" ] && ok "E3 brain really answered 401 ticket_expired to the cached ticket" || bad "E3 expired path not exercised"
[ $((S1-S0)) -ge 1 ] && ok "E4 server fetched a fresh ticket and retried once (+$((S1-S0)) ticket)" || bad "E4 retry"
curl -s -X POST "http://127.0.0.1:$PORT_MOCK/_mock/rotate-key" >/dev/null           # signature no longer valid
get img "$A/api/media/$SOURCE/$(jq -r '.items[8].media[0].sha256 // empty' "$SETS" 2>/dev/null || true)" -b "$JAR"
get img "$A$u7" -b "$JAR"; expect "E5 image still 200 after brain rotated its ticket key (ticket_invalid -> refresh)" "$CODE_HTTP" 200
# "reload the page": all images again
badimg=0; for u in $(jq -r '.items[].media[].url' "$SETS"); do c=$(curl -s -b "$JAR" -o /dev/null -w '%{http_code}' "$A$u"); [ "$c" = 200 ] || badimg=$((badimg+1)); done
expect "E6 after expiry, reload fetches all 56 images (no broken)" "$badimg" 0
expect "E7 Authorization never sent on ticketed image GET" "$(MSTAT | jq '.media_with_authorization // 0')" 0

section "F. Criterion 2 — no token / ticket / secrets in anything the browser can see"
# Collect: page source, every response header+body captured so far, plus fresh fetches.
mkdir -p "$WORK/all"; cp "$WORK"/resp/* "$WORK/all"/ 2>/dev/null
get pg "$A/m/numberline-4y/" -b "$JAR"; cp "$WORK/resp/pg.b" "$WORK/all/page.html"
CAP="$WORK/all-fresh"; mkdir -p "$CAP"
for u in / /index.html /api/session /api/config "/api/sets/$SOURCE" "$u7"; do
  curl -s -i -b "$JAR" "$A$u" > "$CAP/$(echo "$u" | tr '/?' '__').raw"; done
curl -s -i -H 'Content-Type: application/json' -d "{\"code\":\"$CODE\"}" "$A/api/login" > "$CAP/login.raw"
grep -rIl -- "$TOKEN" "$WORK/all" "$CAP" >/dev/null 2>&1 && bad "F1 bearer token found in a response" || ok "F1 bearer token not in any response/header/Set-Cookie/page source ($(ls "$WORK/all" "$CAP" | wc -l) captures)"
grep -rIlE -- 'Bearer|Authorization' "$WORK/all" "$CAP" >/dev/null 2>&1 && bad "F2 'Bearer'/'Authorization' string present" || ok "F2 no 'Bearer'/'Authorization' strings in responses or page source"
if cat "$WORK/all/page.html" "$WORK/resp/sets.b" > "$WORK/f3.txt"; for f in "$CAP"/*; do grep -iv '^set-cookie:' "$f" >> "$WORK/f3.txt"; done; grep -qE 'v1\.[0-9]{9,11}\.[0-9a-f]{20,}' "$WORK/f3.txt"; then bad "F3 ticket-shaped string in a response"; else ok "F3 no ticket-shaped string (v1.<exp>.<mac>) in page/JSON/headers/bodies (our own session cookie in Set-Cookie excluded: it is the HMAC cookie, not a brain ticket)"; fi
grep -rIl -- "$SECRET" "$WORK/all" "$CAP" >/dev/null 2>&1 && bad "F4 session secret in a response" || ok "F4 session secret not in any response"
grep -rIl -- "$CODE" "$WORK/all/page.html" "$WORK/resp/sets.b" "$CAP"/*index* 2>/dev/null >/dev/null && bad "F5 family code in page/JSON" || ok "F5 family code not in page source or JSON"
if git -C "$ROOT" grep -n -I -e "$TOKEN" -e "$CODE" -e "$SECRET" -e "$TOPIC" -- . >/dev/null 2>&1; then bad "F6 run secrets found in repo"; else ok "F6 run-time secrets not in the repo (tracked files)"; fi

section "G. Rate limit / code change (Criterion 6)"
start_site B "$PORT_B" "http://127.0.0.1:$PORT_MOCK"; B="http://127.0.0.1:$PORT_B"; wait_http "$B/healthz"
codes=""; for i in 1 2 3 4 5 6 7; do post_login rl "$B" "bad-$i"; codes="$codes $CODE_HTTP"; done
expect "G1 wrong codes x7: 5x401 then 429" "$(echo $codes)" "401 401 401 401 401 429 429"
grep -qi '^retry-after:' "$WORK/resp/rl.h" && ok "G2 429 carries Retry-After" || bad "G2 Retry-After"
post_login rl "$B" "$CODE"; expect "G3 even the right code is refused while limited" "$CODE_HTTP" 429
grep -qi '^set-cookie:' "$WORK/resp/rl.h" && bad "G4 429 must not set cookie" || ok "G4 429 sets no cookie"
kill "$PID_B" 2>/dev/null
# change family code -> restart -> old cookie invalid
kill -TERM "$PID_A"; wait "$PID_A" 2>/dev/null; RC=$?
grep -q 'shutting down' "$WORK/A.log" && grep -q 'stopped' "$WORK/A.log" && ok "G5 SIGTERM: graceful shutdown logged (exit $RC)" || bad "G5 graceful shutdown"
SITE_CODE="$CODE2" start_site A2 "$PORT_A" "http://127.0.0.1:$PORT_MOCK"; wait_http "$A/healthz"
get s "$A/api/config" -b "$JAR"; expect "G6 old cookie after RUYI_FAMILY_CODE change -> 401" "$CODE_HTTP" 401
post_login ok2 "$A" "$CODE" ; expect "G7 old code no longer accepted" "$CODE_HTTP" 401
post_login ok2 "$A" "$CODE2" -c "$WORK/jar2"; expect "G8 new code accepted" "$CODE_HTTP" 200
kill "$PID_A2" 2>/dev/null; wait "$PID_A2" 2>/dev/null
SITE_SECRET="$(rnd)$(rnd)" SITE_CODE="$CODE2" start_site A3 "$PORT_A" "http://127.0.0.1:$PORT_MOCK"; wait_http "$A/healthz"
get s "$A/api/config" -b "$WORK/jar2"; expect "G9 cookie after RUYI_SESSION_SECRET change -> 401" "$CODE_HTTP" 401
kill "$PID_A3" 2>/dev/null; wait "$PID_A3" 2>/dev/null
start_site A4 "$PORT_A" "http://127.0.0.1:$PORT_MOCK" "RUYI_FAMILY_CODE=$CODE2"; wait_http "$A/healthz"
get s "$A/api/config" -b "$WORK/jar2"; expect "G10 cookie survives restart with identical config" "$CODE_HTTP" 200
# Secure flag
start_site S "$PORT_D" "http://127.0.0.1:$PORT_MOCK" RUYI_COOKIE_SECURE=1; wait_http "http://127.0.0.1:$PORT_D/healthz"
post_login sec "http://127.0.0.1:$PORT_D" "$CODE"
grep -i '^set-cookie:' "$WORK/resp/sec.h" | grep -qi '; Secure' && ok "G11 RUYI_COOKIE_SECURE=1 adds Secure" || bad "G11 Secure flag"
kill "$PID_S" 2>/dev/null

section "H. Criterion 9 — brain unreachable"
start_site C "$PORT_C" "http://127.0.0.1:1"; C="http://127.0.0.1:$PORT_C"; wait_http "$C/healthz"
post_login c1 "$C" "$CODE" -c "$WORK/jarC"; expect "H1 login still works" "$CODE_HTTP" 200
get s "$C/api/sets/$SOURCE" -b "$WORK/jarC"; expect "H2 /api/sets -> 502" "$CODE_HTTP" 502
grep -q 'brain_unavailable' "$WORK/resp/s.b" && grep -q '题库暂时连不上' "$WORK/resp/s.b" && ok "H3 readable Chinese message + code in JSON" || bad "H3 message" "$(cat "$WORK/resp/s.b")"
get s "$C/healthz"; expect "H4 /healthz 200 without brain" "$CODE_HTTP" 200
get s "$C/api/config" -b "$WORK/jarC"; grep -q "ntfy.sh/$TOPIC" "$WORK/resp/s.b" && ok "H5 /api/config (ntfy url for submit) works without brain" || bad "H5 config"
fails=0; while IFS= read -r f; do
  want=$(git -C "$ROOT" show "$MAIN_REF:$f" | sha256sum | cut -d' ' -f1); get f "$C/$f"; got=$(sha256sum < "$WORK/resp/f.b" | cut -d' ' -f1)
  [ "$CODE_HTTP" = 200 ] && [ "$got" = "$want" ] || fails=$((fails+1)); done <<< "$MAIN_FILES"
expect "H6 all 17 old pages still byte-identical while brain is down (diffs)" "$fails" 0
get s "$C/m/numberline-4y/"; expect "H7 dynamic page itself still served" "$CODE_HTTP" 200
grep -q '题库暂时连不上\|连不上练习站服务' "$ROOT/m/numberline-4y/index.html" && ok "H8 page source has readable brain-error handling (see browser test for rendering)" || bad "H8"

section "I. TLS to the brain (self-signed, loopback like production)"
start_mock mockTLS "$PORT_TLS" MOCK_TLS=1 "MOCK_CA_OUT=$WORK/ca.pem"; for _ in $(seq 1 50); do [ -s "$WORK/ca.pem" ] && break; sleep 0.1; done
start_site T1 18196 "https://127.0.0.1:$PORT_TLS"; wait_http "http://127.0.0.1:18196/healthz"
post_login t1 http://127.0.0.1:18196 "$CODE" -c "$WORK/jarT1"
get s "http://127.0.0.1:18196/api/sets/$SOURCE" -b "$WORK/jarT1"; expect "I1 strict verification (default) rejects self-signed -> 502" "$CODE_HTTP" 502
grep -q 'brain tls error' "$WORK/T1.log" && ok "I2 log says 'brain tls error' (no URL/ticket)" || bad "I2 tls log"
start_site T2 18197 "https://127.0.0.1:$PORT_TLS" RUYI_BRAIN_INSECURE_TLS=1; wait_http "http://127.0.0.1:18197/healthz"
post_login t2 http://127.0.0.1:18197 "$CODE" -c "$WORK/jarT2"
get s "http://127.0.0.1:18197/api/sets/$SOURCE" -b "$WORK/jarT2"; expect "I3 RUYI_BRAIN_INSECURE_TLS=1 works with self-signed" "$CODE_HTTP" 200
start_site T3 18198 "https://127.0.0.1:$PORT_TLS" "RUYI_BRAIN_CA_FILE=$WORK/ca.pem"; wait_http "http://127.0.0.1:18198/healthz"
post_login t3 http://127.0.0.1:18198 "$CODE" -c "$WORK/jarT3"
get s "http://127.0.0.1:18198/api/sets/$SOURCE" -b "$WORK/jarT3"; expect "I4 RUYI_BRAIN_CA_FILE works with the mock CA" "$CODE_HTTP" 200
get s "http://127.0.0.1:18198/api/media/$SOURCE/$SHA7" -b "$WORK/jarT3"; expect "I5 image over TLS via CA file" "$CODE_HTTP" 200
for p in "$PID_T1" "$PID_T2" "$PID_T3"; do kill "$p" 2>/dev/null; done

section "J. Startup validation (no values in errors)"
run_bad() { # name envs...
  local n=$1; shift; OUT=$(env -i PATH="$PATH" "$@" "$BIN" 2>&1); RC=$?; }
FULL=(RUYI_STATIC_DIR="$STATIC" RUYI_FAMILY_CODE="$CODE" RUYI_SESSION_SECRET="$SECRET" RUYI_BRAIN_BASE_URL=http://127.0.0.1:1 RUYI_BRAIN_TOKEN="$TOKEN")
for v in RUYI_STATIC_DIR RUYI_FAMILY_CODE RUYI_SESSION_SECRET RUYI_BRAIN_BASE_URL RUYI_BRAIN_TOKEN; do
  args=(); for kv in "${FULL[@]}"; do [ "${kv%%=*}" = "$v" ] || args+=("$kv"); done
  run_bad x "${args[@]}"
  if [ "$RC" != 0 ] && echo "$OUT" | grep -q "$v" && ! echo "$OUT" | grep -qF -e "$CODE" -e "$SECRET" -e "$TOKEN"; then ok "J1 missing $v -> exit $RC, names the variable, prints no values"; else bad "J1 missing $v" "rc=$RC"; fi
done
run_bad x "${FULL[@]}" RUYI_BRAIN_INSECURE_TLS=1 RUYI_BRAIN_BASE_URL=https://brain.example.com
[ "$RC" != 0 ] && ok "J2 INSECURE_TLS refused for a non-loopback brain" || bad "J2"
run_bad x "${FULL[@]}" RUYI_BRAIN_INSECURE_TLS=1 RUYI_BRAIN_CA_FILE="$WORK/ca.pem" RUYI_BRAIN_BASE_URL=https://127.0.0.1:1
[ "$RC" != 0 ] && echo "$OUT" | grep -q 'choose one' && ok "J3 INSECURE_TLS + CA_FILE together refused" || bad "J3"

section "K. Criterion 8 — server logs"
curl -s -o /dev/null "$A/index.html?ticket=QUERYMARK-$$&x=1" -H "Authorization: Bearer HDRMARK-$$" -H "Cookie: ruyi_sess=COOKIEMARK-$$"
curl -s -o /dev/null -b "$WORK/jar2" "$A/api/media/$SOURCE/$SHA7?ticket=QUERYMARK-$$"
sleep 0.3
cat "$WORK"/A.log "$WORK"/A2.log "$WORK"/A3.log "$WORK"/A4.log "$WORK"/B.log "$WORK"/C.log "$WORK"/T1.log "$WORK"/T2.log "$WORK"/T3.log "$WORK"/S.log > "$WORK/alllogs" 2>/dev/null
say "     log lines inspected: $(wc -l < "$WORK/alllogs")"
for pair in "bearer token:$TOKEN" "family code:$CODE" "second family code:$CODE2" "session secret:$SECRET" "ntfy topic:$TOPIC"; do
  name="${pair%%:*}"; val="${pair#*:}"; grep -qF -- "$val" "$WORK/alllogs" && bad "K1 $name appears in logs" || ok "K1 $name not in logs"
done
COOKIE_VAL=$(awk '$6=="ruyi_sess"{print $7}' "$JAR"); grep -qF -- "$COOKIE_VAL" "$WORK/alllogs" && bad "K2 session cookie value in logs" || ok "K2 cookie value not in logs"
grep -qE 'v1\.[0-9]{9,11}\.[0-9a-f]{20,}' "$WORK/alllogs" && bad "K3 ticket-shaped string in logs" || ok "K3 no ticket-shaped strings in logs"
cat "$WORK"/A4.log >> "$WORK/alllogs"
grep -qiE "bearer|authorization|ticket=|cookie:|set-cookie|QUERYMARK|HDRMARK|COOKIEMARK" "$WORK/alllogs" && bad "K4 header/query material in logs" || ok "K4 request with ?ticket=…, Authorization and Cookie headers left no trace in logs (query/headers never logged)"
grep -q 'path=/index.html ' "$WORK/alllogs" && ok "K4b the request itself was logged (path only)" || bad "K4b request line missing"
grep -q 'figure_status ok but media empty qref=math:intake' "$WORK/alllogs" || grep -q 'figure_status ok but media empty' "$WORK/alllogs" && ok "K6 inconsistent figure_status=ok/no media produced a warn log (qref only)" || bad "K6 warn log missing"
grep -q 'login failed' "$WORK/alllogs" && grep -q 'brain request failed' "$WORK/alllogs" && ok "K5 failure events are logged (sanity: logs are not empty)" || bad "K5 sanity"
say "     sample log lines (verbatim):"; { grep 'login failed' "$WORK/alllogs" | head -1; grep 'brain request failed' "$WORK/alllogs" | head -1; grep 'msg=request' "$WORK/alllogs" | head -1; } | sed 's/^/       /' | tee -a "$RESULTS"

section "P. P0 practice page (Issue #10) — sets file, /api/practice, embedded page + KaTeX"
PSRC="math.synthetic.p0-demo"
cat > "$WORK/sets.json" <<JSON
{"sets":[
 {"id":"demo-1","title":"演示套","source":"$PSRC","qrefs":["example.set:math:1","example.set:math:2","example.set:math:3"]},
 {"id":"reorder-2","title":"乱序套","source":"$PSRC","qrefs":["example.set:math:3","example.set:math:99","example.set:math:1"]},
 {"id":"free-3","title":"自由作答","source":"$PSRC","qrefs":["example.set:math:4"]}
]}
JSON
# P0: without RUYI_SETS_FILE (site A4 is still running) nothing changes except /api/practice -> 404
get s "$A/api/practice/demo-1" -b "$WORK/jar2"; expect "P1 no RUYI_SETS_FILE: /api/practice/demo-1 -> 404 (even logged in)" "$CODE_HTTP" 404
get s "$A/api/sets/$SOURCE" -b "$WORK/jar2"; expect "P2 no RUYI_SETS_FILE: old /api/sets still 200" "$CODE_HTTP" 200
start_site P 18210 "http://127.0.0.1:$PORT_MOCK" "RUYI_SETS_FILE=$WORK/sets.json"; PA="http://127.0.0.1:18210"; wait_http "$PA/healthz"
post_login p1 "$PA" "$CODE" -c "$WORK/jarP"
get s "$PA/api/practice/demo-1"; expect "P3 anonymous /api/practice/demo-1 -> 401" "$CODE_HTTP" 401
get s "$PA/api/practice/Bad%20Id" ; expect "P3b anonymous + illegal id -> still 401 (login first)" "$CODE_HTTP" 401
get s "$PA/api/practice/Bad%20Id" -b "$WORK/jarP"; expect "P4 illegal set id -> 400" "$CODE_HTTP" 400
get s "$PA/api/practice/no-such-set" -b "$WORK/jarP"; expect "P5 unknown set id -> 404" "$CODE_HTTP" 404
get s "$PA/api/practice/..%2f..%2fetc%2fpasswd" -b "$WORK/jarP"; [ "$CODE_HTTP" = 400 ] || [ "$CODE_HTTP" = 404 ] && ok "P5b traversal-shaped id refused ($CODE_HTTP)" || bad "P5b" "$CODE_HTTP"
get d1 "$PA/api/practice/demo-1" -b "$WORK/jarP"; expect "P6 demo-1 -> 200" "$CODE_HTTP" 200
expect "P7 whitelist + config order (3 questions of 5 in the brain; alternates excluded)" "$(jq -r '[.items[].qref]|join(",")' "$WORK/resp/d1.b")" "example.set:math:1,example.set:math:2,example.set:math:3"
expect "P7b ordinals 1..N, set_id/title, empty missing_qrefs" "$(jq -c '[[.items[].ordinal],.set_id,.title,.missing_qrefs]' "$WORK/resp/d1.b")" '[[1,2,3],"demo-1","演示套",[]]'
expect "P8 blanks counted by the server (3 / none / 2, full-width included)" "$(jq -c '[.items[]|(.blanks // 0)]' "$WORK/resp/d1.b")" '[3,0,2]'
expect "P8b options delivered as {key,text_md}" "$(jq -c '[.items[1].options[]|.key]' "$WORK/resp/d1.b")" '["A","B","C","D"]'
get d2 "$PA/api/practice/reorder-2" -b "$WORK/jarP"
expect "P9 config order beats brain order; configured-but-absent qref -> missing_qrefs (200, not 500)" "$(jq -c '[[.items[].qref],.missing_qrefs]' "$WORK/resp/d2.b")" '[["example.set:math:3","example.set:math:1"],["example.set:math:99"]]'
for f in d1 d2; do
  if grep -qiE '"(answer|explanation|solution|correct|is_correct|include_answer)"|MOCK-(ANSWER|EXPLANATION|SOLUTION)-MARKER' "$WORK/resp/$f.b"; then bad "P10 $f carries an answer/explanation/solution field"; else ok "P10 $f has no answer/explanation/solution (mock sends all three on every question)"; fi
done
grep -q 'MOCK-ANSWER-MARKER' <(curl -s -H "Authorization: Bearer $TOKEN" "http://127.0.0.1:$PORT_MOCK/v1/bank/questions?source=$PSRC&limit=500") && ok "P10b control: the mock really does send the answer marker upstream" || bad "P10b control"
expect "P11 allowed item keys only" "$(jq -c '[.items[]|keys[]]|unique' "$WORK/resp/d1.b")" '["blanks","figure_status","media","options","ordinal","qref","qtype","rev","stem_md"]'
if grep -qiE 'bearer|'"$TOKEN"'|ticket' "$WORK/resp/d1.b" "$WORK/resp/d1.h"; then bad "P12 token/Bearer/ticket in practice response"; else ok "P12 no token / Bearer / ticket in the practice response or headers"; fi
expect "P13 mock saw the practice source list requests (only source/limit/cursor accepted by the mock, else 400)" "$(MSTAT | jq '.list_practice >= 1')" true

# embedded page + KaTeX: served although the static dir has no copy of them
[ ! -e "$STATIC/m/practice" ] && ok "P14 the static dir has no m/practice (nothing hand-copied to the server)" || bad "P14"
get pg "$PA/m/practice/"; expect "P15 /m/practice/ -> 200 without login (page holds no data)" "$CODE_HTTP" 200
grep -qi '^content-type: text/html' "$WORK/resp/pg.h" && grep -qi "^content-security-policy: default-src 'none'" "$WORK/resp/pg.h" && ok "P15b html content-type + restrictive CSP" || bad "P15b headers"
get s "$PA/m/practice"; expect "P15c /m/practice -> 301" "$CODE_HTTP" 301
for f in katex.min.js katex.min.css LICENSE fonts/KaTeX_Main-Regular.woff2 fonts/KaTeX_Math-Italic.woff2; do get s "$PA/vendor/katex/$f"; expect "P16 /vendor/katex/$f" "$CODE_HTTP" 200; done
for f in "" katex/ katex/fonts/ katex/fonts/KaTeX_Main-Regular.ttf katex/fonts/KaTeX_Main-Regular.woff katex/nope.js katex/../katex/katex.min.js katex/%2e%2e/katex.min.js other/x.js modules.txt; do
  get s "$PA/vendor/$f"; [ "$CODE_HTTP" = 404 ] && ok "P17 /vendor/$f -> 404" || bad "P17 /vendor/$f" "got $CODE_HTTP"; done
# the page as delivered: no external references at all
if grep -oiE '(https?:)?//[a-z0-9][^"'"'"' )<>]*' "$WORK/resp/pg.b" | grep -viE '^//(localhost|127\.0\.0\.1)' | grep -q .; then bad "P18 page references an external URL" "$(grep -oiE '(https?:)?//[a-z0-9][^"'"'"' )<>]*' "$WORK/resp/pg.b" | head -3 | tr '\n' ' ')"; else ok "P18 page source: no http(s):// or protocol-relative references (only site-relative paths)"; fi
get css "$PA/vendor/katex/katex.min.css"
grep -qiE 'https?:|@import' "$WORK/resp/css.b" && bad "P19 css references something external" || ok "P19 katex css: no external references; fonts only /vendor/katex/fonts/*.woff2"
[ "$(grep -o 'url([^)]*)' "$WORK/resp/css.b" | grep -vc '^url(/vendor/katex/fonts/[A-Za-z0-9_-]*\.woff2)$')" = 0 ] && ok "P19b every css url() is an embedded woff2" || bad "P19b css urls"
# regressions: old pages untouched, numberline page + API unchanged
fails=0; while IFS= read -r f; do
  want=$(git -C "$ROOT" show "$MAIN_REF:$f" | sha256sum | cut -d' ' -f1); get f "$PA/$f"; got=$(sha256sum < "$WORK/resp/f.b" | cut -d' ' -f1)
  [ "$CODE_HTTP" = 200 ] && [ "$got" = "$want" ] || fails=$((fails+1)); done <<< "$MAIN_FILES"
expect "P20 all $(echo "$MAIN_FILES" | wc -l) old static files still byte-identical to $MAIN_REF (diffs)" "$fails" 0
get f "$PA/m/numberline-4y/"; expect "P21 numberline page still served" "$CODE_HTTP" 200
[ "$(sha256sum < "$WORK/resp/f.b" | cut -d' ' -f1)" = "$(sha256sum < "$ROOT/m/numberline-4y/index.html" | cut -d' ' -f1)" ] && ok "P21b numberline page byte-identical to the repo file" || bad "P21b"
get f "$PA/api/sets/$SOURCE" -b "$WORK/jarP"; expect "P22 numberline API unchanged: 60 questions" "$(jq '.items|length' "$WORK/resp/f.b")" 60
get f "$PA/api/sets/$PSRC" -b "$WORK/jarP"; expect "P23 a set-only source is NOT listable through the old /api/sets" "$CODE_HTTP" 404
get f "$PA/server.log"; expect "P24 junk files still 404" "$CODE_HTTP" 404
# startup validation of the sets file
printf '{"sets":[{"id":"BAD-ID","title":"t","source":"%s","qrefs":["SECRET-QREF-MARK"]}]}' "$PSRC" > "$WORK/badsets.json"
run_bad x "${FULL[@]}" RUYI_SETS_FILE="$WORK/badsets.json"
[ "$RC" != 0 ] && echo "$OUT" | grep -q RUYI_SETS_FILE && ! echo "$OUT" | grep -q -e SECRET-QREF-MARK -e BAD-ID && ok "P25 invalid sets file -> exit $RC, names RUYI_SETS_FILE, echoes none of its content" || bad "P25" "rc=$RC out=$OUT"
run_bad x "${FULL[@]}" RUYI_SETS_FILE="$WORK/does-not-exist.json"
[ "$RC" != 0 ] && echo "$OUT" | grep -q RUYI_SETS_FILE && ok "P26 missing sets file -> refused" || bad "P26"
OUT=$(env -i PATH="$PATH" "${FULL[@]}" RUYI_SETS_FILE="$SRV/deploy/sets.example.json" RUYI_LISTEN=127.0.0.1:18215 timeout 2 "$BIN" 2>&1); RC=$?
[ "$RC" = 124 ] && ok "P27 deploy/sets.example.json is accepted at startup (kept running until the 2 s timeout)" || bad "P27 example sets file" "rc=$RC $OUT"
sleep 0.3
cat "$WORK/P.log" > "$WORK/plogs"
for pair in "bearer token:$TOKEN" "family code:$CODE" "session secret:$SECRET" "ntfy topic:$TOPIC" "qref:example.set:math" "answer marker:MOCK-ANSWER" "stem text:dfrac"; do
  name="${pair%%:*}"; val="${pair#*:}"; grep -qF -- "$val" "$WORK/plogs" && bad "P28 $name appears in the practice-site log" || ok "P28 $name not in the practice-site log"
done
grep -q 'practice sets loaded' "$WORK/plogs" && grep -q 'practice set has qrefs the brain did not return' "$WORK/plogs" && ok "P29 log has the set count and a count-only warning for the missing qref" || bad "P29"

section "Q. P1–P3 — grading through the brain, client_attempt_id idempotency, archive, ntfy heads-up, dynamic sets"
GTOKEN="grade-$TOKEN"          # what the default mock accepts for POST /v1/bank/grade (it must NOT accept the read token)
NTFY_PORT=18225
cat > "$WORK/ntfy_stub.py" <<'PY'
import http.server, json, sys
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(s):
        n = int(s.headers.get('Content-Length', 0)); b = s.rfile.read(n).decode('utf-8', 'replace')
        with open(sys.argv[2], 'a') as f:
            f.write(json.dumps({"path": s.path, "auth": s.headers.get('Authorization'), "cookie": s.headers.get('Cookie'), "title": s.headers.get('Title'), "body": b}, ensure_ascii=False) + "\n")
        s.send_response(200); s.end_headers(); s.wfile.write(b'{}')
    def log_message(self, *a): pass
http.server.HTTPServer(('127.0.0.1', int(sys.argv[1])), H).serve_forever()
PY
: > "$WORK/ntfy.jsonl"
python3 "$WORK/ntfy_stub.py" $NTFY_PORT "$WORK/ntfy.jsonl" & PIDS+=($!)
NTFYB="http://127.0.0.1:$NTFY_PORT"
for _ in $(seq 1 30); do (echo > /dev/tcp/127.0.0.1/$NTFY_PORT) 2>/dev/null && break; sleep 0.1; done
nextday() { curl -s -X POST "http://127.0.0.1:${1:-$PORT_MOCK}/_mock/advance-clock?d=25h" >/dev/null; } # the brain counts writes per question per (its) day: move its clock on
ntfy_n() { wc -l < "$WORK/ntfy.jsonl" | tr -d ' '; }
mstat() { curl -s "http://127.0.0.1:${2:-$PORT_MOCK}/_mock/stats" | jq -r ".$1 // 0"; }
QENV=("RUYI_SETS_FILE=$WORK/sets.json" "RUYI_BRAIN_GRADE_TOKEN=$GTOKEN" "RUYI_NTFY_BASE_URL=$NTFYB" "RUYI_NTFY_RETRY_DELAY=300ms" "RUYI_ATTEMPTS_DIR=$WORK/att-Q" "RUYI_GRADE_MAX_PER_HOUR=4" "RUYI_AUTO_SOURCES=$PSRC" "RUYI_AUTO_TTL=3s")
start_site Q 18230 "http://127.0.0.1:$PORT_MOCK" "${QENV[@]}"; QA="http://127.0.0.1:18230"; wait_http "$QA/healthz"
post_login q1 "$QA" "$CODE" -c "$WORK/jarQ"
gpost() { # gpost NAME BASE SET BODY [curl args]
  local n=$1 b=$2 s=$3 body=$4; shift 4
  CODE_HTTP=$(curl -sS -D "$WORK/resp/$n.h" -o "$WORK/resp/$n.b" -w '%{http_code}' -H 'Content-Type: application/json' -d "$body" "$@" "$b/api/practice/$s/grade")
}
C1=11111111-1111-4111-8111-111111111111
DEFANS='[{"qref":"example.set:math:1","answer":["1","2","3"]},{"qref":"example.set:math:2","answer":"A"},{"qref":"example.set:math:3","answer":["x","y"]}]'
mkbody() { # mkbody CID [answers-json]   (the default answers: q1 right, q2 wrong, q3 has no key in the mock)
  local a="${2:-$DEFANS}"; printf '{"client_attempt_id":"%s","answers":%s}' "$1" "$a"
}
# --- Q1 graded through the brain
N0=$(ntfy_n); G0=$(mstat grade_new)
gpost g1 "$QA" demo-1 "$(mkbody $C1)" -b "$WORK/jarQ"; expect "Q1 grade demo-1 -> 200" "$CODE_HTTP" 200
expect "Q1b mode graded, brain confirmed, ids echoed" "$(jq -c '[.mode,.brain_recorded,.client_attempt_id,.set_id,(.replayed // false)]' "$WORK/resp/g1.b")" "[\"graded\",true,\"$C1\",\"demo-1\",false]"
expect "Q1c per-question verdicts (wrong key->false is NOT leaked as an answer; 1 right, 1 wrong, 1 pending_key)" "$(jq -c '[.results[]|[.status,.correct]]' "$WORK/resp/g1.b")" '[["graded",true],["graded",false],["pending_key",null]]'
expect "Q1d summary total/graded/correct/pending/unanswered" "$(jq -c '[.summary.total,.summary.graded,.summary.correct,.summary.pending,.summary.unanswered]' "$WORK/resp/g1.b")" '[3,2,1,1,0]'
expect "Q1e archived and notified" "$(jq -c '[.archived,.notify]' "$WORK/resp/g1.b")" '[true,"sent"]'
expect "Q1f the brain was asked exactly once (grade_new +1)" "$(( $(mstat grade_new) - G0 ))" 1
expect "Q1g response has only the documented keys (replayed appears only on a replay)" "$(jq -c 'keys' "$WORK/resp/g1.b")" '["archived","brain_recorded","client_attempt_id","mode","notify","recorded_count","results","set_id","summary"]'
# --- Q2 idempotent replay
gpost g2 "$QA" demo-1 "$(mkbody $C1)" -b "$WORK/jarQ"
expect "Q2 same client_attempt_id again -> 200 replayed:true with the stored result" "$(jq -c '[.replayed,.mode,.summary.correct]' "$WORK/resp/g2.b")" '[true,"graded",1]'
gpost g2b "$QA" demo-1 "$(mkbody $C1 '[{"qref":"example.set:math:2","answer":"B"}]')" -b "$WORK/jarQ"
expect "Q2b same client_attempt_id with different answers -> 409 idempotency_conflict, retry_new_id (nothing re-graded)" "$CODE_HTTP $(jq -c '[.error,.retry_new_id]' "$WORK/resp/g2b.b")" '409 ["idempotency_conflict",true]'
expect "Q2c no second brain write, no second notification, one archive line" "$(( $(mstat grade_new) - G0 )) $(mstat grade_replay) $(( $(ntfy_n) - N0 )) $(wc -l < "$WORK/att-Q/attempts.jsonl" | tr -d ' ')" "1 0 1 1"
gpost g2c "$QA" free-3 "$(mkbody $C1 '[{"qref":"example.set:math:4","answer":"x"}]')" -b "$WORK/jarQ"; expect "Q2d same id on another set -> 409" "$CODE_HTTP" 409
# --- Q3 whitelist, shapes, auth
G1=$(mstat grade)
gpost v "$QA" demo-1 "$(mkbody 22222222-2222-4222-8222-222222222221 '[{"qref":"example.set:math:4","answer":"x"}]')" -b "$WORK/jarQ"; expect "Q3 qref of the same source but outside the set (alternate q4) -> 400" "$CODE_HTTP" 400
gpost v "$QA" demo-1 "$(mkbody 22222222-2222-4222-8222-222222222222 '[{"qref":"example.set:math:5","answer":"B"}]')" -b "$WORK/jarQ"; expect "Q3b qref outside the set (q5) -> 400" "$CODE_HTTP" 400
gpost v "$QA" demo-1 "$(mkbody 22222222-2222-4222-8222-222222222223 '[{"qref":"made:up:1","answer":"A"}]')" -b "$WORK/jarQ"; expect "Q3c invented qref -> 400" "$CODE_HTTP" 400
gpost v "$QA" demo-1 "$(mkbody 22222222-2222-4222-8222-222222222224 '[{"qref":"example.set:math:2","answer":["A"]}]')" -b "$WORK/jarQ"; expect "Q3d choice answered with an array -> 400" "$CODE_HTTP" 400
gpost v "$QA" demo-1 "$(mkbody 22222222-2222-4222-8222-222222222225 '[{"qref":"example.set:math:1","answer":["1","2"]}]')" -b "$WORK/jarQ"; expect "Q3e wrong blank count -> 400" "$CODE_HTTP" 400
gpost v "$QA" demo-1 "$(mkbody 22222222-2222-4222-8222-222222222226 "[{\"qref\":\"example.set:math:3\",\"answer\":[\"$(head -c 300 /dev/zero | tr '\0' x)\",\"\"]}]")" -b "$WORK/jarQ"; expect "Q3f blank over the length cap -> 400" "$CODE_HTTP" 400
gpost v "$QA" demo-1 "$(mkbody not-a-uuid)" -b "$WORK/jarQ"; expect "Q3g client_attempt_id must be a uuid -> 400" "$CODE_HTTP" 400
gpost v "$QA" demo-1 "$(mkbody 22222222-2222-4222-8222-222222222227)" ; expect "Q3h anonymous -> 401" "$CODE_HTTP" 401
gpost v "$QA" no-such-set "$(mkbody 22222222-2222-4222-8222-222222222228)" -b "$WORK/jarQ"; expect "Q3i unknown set -> 404" "$CODE_HTTP" 404
CODE_HTTP=$(curl -sS -o /dev/null -w '%{http_code}' -H 'Content-Type: text/plain' -d "$(mkbody 22222222-2222-4222-8222-222222222229)" -b "$WORK/jarQ" "$QA/api/practice/demo-1/grade"); expect "Q3j wrong content type -> 415" "$CODE_HTTP" 415
CODE_HTTP=$(curl -sS -o /dev/null -w '%{http_code}' -b "$WORK/jarQ" "$QA/api/practice/demo-1/grade"); expect "Q3k GET on the grade URL -> 405" "$CODE_HTTP" 405
expect "Q3l none of the rejected requests reached the brain's grade endpoint" "$(( $(mstat grade) - G1 ))" 0
# --- Q4 the archive
expect "Q4 attempts dir is 700 and the file 600, owner is the site user" "$(stat -c '%a %a %U' "$WORK/att-Q" | head -1 | cut -d' ' -f1) $(stat -c %a "$WORK/att-Q/attempts.jsonl") $(stat -c %U "$WORK/att-Q/attempts.jsonl")" "700 600 $(id -un)"
expect "Q4b record fields (JSON Lines, version 2, client_attempt_id naming, per-item recorded, mode)" "$(head -1 "$WORK/att-Q/attempts.jsonl" | jq -c '[.v,.client_attempt_id,.set_id,.set_title,.source,.mode,([.items[]|select(.recorded)]|length==3),(.items|length),(.attempt_id // "none")]')" "[2,\"$C1\",\"demo-1\",\"演示套\",\"$PSRC\",\"graded\",true,3,\"none\"]"
expect "Q4c answers are archived as given (fill blanks as arrays)" "$(head -1 "$WORK/att-Q/attempts.jsonl" | jq -c '[.items[].answer]')" '[["1","2","3"],"A",["x","y"]]'
for pair in "family code:$CODE" "bearer token:$TOKEN" "grade token:$GTOKEN" "session secret:$SECRET" "ntfy topic:$TOPIC" "cookie name:ruyi_sess" "Authorization header:Authorization"; do
  name="${pair%%:*}"; val="${pair#*:}"; grep -qF -- "$val" "$WORK/att-Q/attempts.jsonl" && bad "Q4d $name found in the attempts archive" || ok "Q4d $name not in the attempts archive"
done
# --- Q5 ntfy heads-up
expect "Q5 ntfy: one POST, to /<topic>, no credentials" "$(jq -rs '[length, (.[0].path == "/'"$TOPIC"'"), (.[0].auth == null), (.[0].cookie == null)]|@json' "$WORK/ntfy.jsonl")" '[1,true,true,true]'
expect "Q5b ntfy summary line (heads-up wording, not an approval request)" "$(jq -rs '.[0].body|split("完成于 ")[0]' "$WORK/ntfy.jsonl")" '如意练习：演示套 3 题，对 1，错 1，待核对 1，已提交。attempt 11111111。'
expect "Q5c ntfy body is one short paragraph: Beijing completion time, no JSON, no qref, no full attempt id" "$(jq -rs '.[0].body|[test("完成于 [0-9]{2}:[0-9]{2}:[0-9]{2}（北京时间）。$"), (contains("{")|not), (contains("\n")|not), (contains("example.set")|not), (contains("'"$C1"'")|not), (length<300)]|@json' "$WORK/ntfy.jsonl")" '[true,true,true,true,true,true]'
for pair in "family code:$CODE" "bearer token:$TOKEN" "grade token:$GTOKEN" "session secret:$SECRET"; do
  name="${pair%%:*}"; val="${pair#*:}"; grep -qF -- "$val" "$WORK/ntfy.jsonl" && bad "Q5d $name found in the ntfy message" || ok "Q5d $name not in the ntfy message"
done
# --- Q6 nothing sensitive in any response
for f in g1 g2 g2b; do
  if grep -qiE 'explanation|solution|"answer"|MOCK-ANSWER|SECRET|brain-attempt|mock-' "$WORK/resp/$f.b"; then bad "Q6 $f leaks an answer/explanation/brain id"; else ok "Q6 grade response $f: no answer, explanation, key or brain attempt id"; fi
done
get s "$QA/api/practice/demo-1" -b "$WORK/jarQ"; grep -qiE 'explanation|"answer"|solution|MOCK-ANSWER' "$WORK/resp/s.b" && bad "Q6b practice response leaks answers" || ok "Q6b practice response still has no answers/explanations"
# --- Q7 the read token can never be used for grading (separate token; mock counts such use)
expect "Q7 the grade call never used the bank:read token (mock saw 0)" "$(mstat grade_with_bank_read_token)" 0
# --- Q8 rate limit (4 / hour / session+IP / set; replays and rejected requests are free)
for i in 2 3 4; do nextday; gpost r$i "$QA" demo-1 "$(mkbody 3333333$i-3333-4333-8333-333333333333)" -b "$WORK/jarQ"; [ "$CODE_HTTP" = 200 ] && ok "Q8 submission $i of 4 accepted" || bad "Q8 submission $i" "$CODE_HTTP"; done
gpost r5 "$QA" demo-1 "$(mkbody 33333335-3333-4333-8333-333333333333)" -b "$WORK/jarQ"; expect "Q8b 5th fresh submission in the hour -> 429" "$CODE_HTTP" 429
grep -qi '^retry-after: [0-9]' "$WORK/resp/r5.h" && ok "Q8c 429 carries Retry-After" || bad "Q8c Retry-After"
gpost r6 "$QA" demo-1 "$(mkbody $C1)" -b "$WORK/jarQ"; expect "Q8d replay of an accepted attempt is still served while limited" "$(jq -r '.replayed' "$WORK/resp/r6.b")" true
post_login q2 "$QA" "$CODE" -c "$WORK/jarQ2"; gpost r7 "$QA" demo-1 "$(mkbody 33333337-3333-4333-8333-333333333333)" -b "$WORK/jarQ2"; expect "Q8e a fresh login from the same address does not reset the limit" "$CODE_HTTP" 429
gpost r8 "$QA" free-3 "$(mkbody 33333338-3333-4333-8333-333333333333 '[{"qref":"example.set:math:4","answer":"x"}]')" -b "$WORK/jarQ"; expect "Q8f another set has its own allowance" "$CODE_HTTP" 200
# --- Q9 restart: idempotency survives (attempts file is reloaded)
kill "$PID_Q" 2>/dev/null; sleep 0.4
N1=$(ntfy_n); G2=$(mstat grade)
start_site Q 18230 "http://127.0.0.1:$PORT_MOCK" "${QENV[@]}"; wait_http "$QA/healthz"
post_login q3 "$QA" "$CODE" -c "$WORK/jarQ"
gpost g3 "$QA" demo-1 "$(mkbody $C1)" -b "$WORK/jarQ"
expect "Q9 after a restart the same client_attempt_id is replayed from the archive" "$(jq -c '[.replayed,.mode,.summary.correct]' "$WORK/resp/g3.b")" '[true,"graded",1]'
expect "Q9b ... without grading, notifying or archiving again" "$(( $(mstat grade) - G2 )) $(( $(ntfy_n) - N1 ))" "0 0"
nextday
# --- Q10 dynamic sets
start_site QN 18231 "http://127.0.0.1:$PORT_MOCK" "RUYI_SETS_FILE=$WORK/sets.json"; wait_http "http://127.0.0.1:18231/healthz"
post_login qn "http://127.0.0.1:18231" "$CODE" -c "$WORK/jarQN"
get s "http://127.0.0.1:18231/api/practice/new?source=$PSRC&n=2" -b "$WORK/jarQN"; expect "Q10 RUYI_AUTO_SOURCES empty (default) -> /api/practice/new is off (404)" "$CODE_HTTP" 404
get s "$QA/api/practice/new?source=$PSRC&n=2"; expect "Q10b dynamic sets require login (401)" "$CODE_HTTP" 401
get s "$QA/api/practice/new?source=$SOURCE&n=2" -b "$WORK/jarQ"; expect "Q10c a source outside RUYI_AUTO_SOURCES -> 400" "$CODE_HTTP" 400
for bad_n in 0 21 abc; do get s "$QA/api/practice/new?source=$PSRC&n=$bad_n" -b "$WORK/jarQ"; expect "Q10d n=$bad_n -> 400" "$CODE_HTTP" 400; done
get dn "$QA/api/practice/new?source=$PSRC&n=2&order=seq" -b "$WORK/jarQ"; expect "Q10e n=2 -> 200" "$CODE_HTTP" 200
DSET=$(jq -r .set_id "$WORK/resp/dn.b")
[[ "$DSET" =~ ^auto-[a-z0-9]{8}$ ]] && ok "Q10f set id looks like auto-<8>" || bad "Q10f set id" "$DSET"
expect "Q10g response: url, count, truncated, expiry present" "$(jq -c '[.url=="/m/practice/?set='"$DSET"'",.count,.truncated,(.expires_at|length>0)]' "$WORK/resp/dn.b")" '[true,2,false,true]'
get d3 "$QA/api/practice/$DSET" -b "$WORK/jarQ"; expect "Q10h the dynamic set is a normal practice set (seq order 1,2)" "$(jq -r '[.items[].qref]|join(",")' "$WORK/resp/d3.b")" "example.set:math:1,example.set:math:2"
gpost dg "$QA" "$DSET" "$(mkbody 44444444-4444-4444-8444-444444444444 '[{"qref":"example.set:math:2","answer":"B"}]')" -b "$WORK/jarQ"
expect "Q10i grading a dynamic set works (graded, 1 right)" "$(jq -c '[.mode,.summary.correct,.set_id=="'"$DSET"'"]' "$WORK/resp/dg.b")" '["graded",1,true]'
get dn2 "$QA/api/practice/new?source=$PSRC&n=20" -b "$WORK/jarQ"
expect "Q10j n larger than the source returns all (5) with truncated + note" "$(jq -c '[.count,.truncated,.requested,(.note|length>0)]' "$WORK/resp/dn2.b")" '[5,true,20,true]'
get dn3 "$QA/api/practice/new?source=$PSRC&n=3&wrong_only=1" -b "$WORK/jarQ"
expect "Q10k legacy wrong_only=1 -> the brain's pick (mode wrong_only, 3 picks, 2 deferred)" "$CODE_HTTP $(jq -c '[.mode,.count,.deferred]' "$WORK/resp/dn3.b")" '200 ["wrong_only",3,2]'
get dn4 "$QA/api/practice/new?source=$PSRC&n=3&seed=7" -b "$WORK/jarQ"; get dn5 "$QA/api/practice/new?source=$PSRC&n=3&seed=7" -b "$WORK/jarQ"
get e1 "$QA/api/practice/$(jq -r .set_id "$WORK/resp/dn4.b")" -b "$WORK/jarQ"; get e2 "$QA/api/practice/$(jq -r .set_id "$WORK/resp/dn5.b")" -b "$WORK/jarQ"
expect "Q10l same seed -> same selection" "$(jq -r '[.items[].qref]|join(",")' "$WORK/resp/e1.b")" "$(jq -r '[.items[].qref]|join(",")' "$WORK/resp/e2.b")"
sleep 3.5
get s "$QA/api/practice/$DSET" -b "$WORK/jarQ"; expect "Q10m after the TTL (3 s) the dynamic set is gone (404)" "$CODE_HTTP" 404
get s "$QA/api/practice/auto-abcdefgh" -b "$WORK/jarQ"; expect "Q10n a made-up auto id -> 404" "$CODE_HTTP" 404
# --- Q11 degraded paths: archive + notify only; the page API never claims a brain write
check_degraded() { # name site-port brain-port-for-grade-count extra-label
  local nm=$1 port=$2 base="http://127.0.0.1:$2"
  post_login dq$nm "$base" "$CODE" -c "$WORK/jar$nm"
  local n0=$(ntfy_n)
  gpost dgr$nm "$base" demo-1 "$(mkbody 5555555$3-5555-4555-8555-555555555555)" -b "$WORK/jar$nm"
  expect "Q11 [$4] -> 200 mode archive_only, brain_recorded false" "$CODE_HTTP $(jq -c '[.mode,.brain_recorded,.archived,.notify]' "$WORK/resp/dgr$nm.b")" '200 ["archive_only",false,true,"sent"]'
  expect "Q11b [$4] every result is pending_key with correct:null, summary pending 3" "$(jq -c '[([.results[]|[.status,.correct]]|unique), .summary.pending, .summary.graded, .summary.correct]' "$WORK/resp/dgr$nm.b")" '[[["pending_key",null]],3,0,0]'
  expect "Q11c [$4] ntfy says 未判分 and never 对/错/大脑" "$(jq -rs '.[-1].body|split("\n")[0]' "$WORK/ntfy.jsonl" | grep -c -E '未判分')$(jq -rs '.[-1].body' "$WORK/ntfy.jsonl" | grep -c -E '，对 |，错 |大脑')" "10"
  expect "Q11d [$4] exactly one more notification" "$(( $(ntfy_n) - n0 ))" 1
  grep -q '大脑' "$WORK/resp/dgr$nm.b" && bad "Q11e [$4] response mentions the brain" || ok "Q11e [$4] response never says the brain recorded anything"
}
start_site QD 18232 "http://127.0.0.1:$PORT_MOCK" "RUYI_SETS_FILE=$WORK/sets.json" "RUYI_NTFY_BASE_URL=$NTFYB" "RUYI_ATTEMPTS_DIR=$WORK/att-QD"; wait_http "http://127.0.0.1:18232/healthz"
G3=$(mstat grade); check_degraded QD 18232 1 "grade token not configured"
expect "Q11f [not configured] the brain grade endpoint was never called" "$(( $(mstat grade) - G3 ))" 0
start_mock mockOff 18221 MOCK_GRADE=off "MOCK_GRADE_TOKEN=$GTOKEN"; wait_http "http://127.0.0.1:18221/_mock/stats"
start_site QO 18233 "http://127.0.0.1:18221" "RUYI_SETS_FILE=$WORK/sets.json" "RUYI_BRAIN_GRADE_TOKEN=$GTOKEN" "RUYI_NTFY_BASE_URL=$NTFYB" "RUYI_ATTEMPTS_DIR=$WORK/att-QO"; wait_http "http://127.0.0.1:18233/healthz"
check_degraded QO 18233 2 "grade endpoint missing (404)"
start_mock mockFail 18222 MOCK_GRADE_FAIL=1 "MOCK_GRADE_TOKEN=$GTOKEN"; wait_http "http://127.0.0.1:18222/_mock/stats"
start_site QF 18234 "http://127.0.0.1:18222" "RUYI_SETS_FILE=$WORK/sets.json" "RUYI_BRAIN_GRADE_TOKEN=$GTOKEN" "RUYI_NTFY_BASE_URL=$NTFYB" "RUYI_ATTEMPTS_DIR=$WORK/att-QF"; wait_http "http://127.0.0.1:18234/healthz"
check_degraded QF 18234 3 "grade endpoint failing (500)"
for d in QD QO QF; do expect "Q11g [$d] one archive line, mode archive_only, 600 perms" "$(jq -rs '[length,.[0].mode]|@json' "$WORK/att-$d/attempts.jsonl") $(stat -c %a "$WORK/att-$d/attempts.jsonl")" '[1,"archive_only"] 600'; done
# --- Q12 the attempts dir cannot be written: the answer is still graded, archived:false, one error log
echo x > "$WORK/afile"
start_site QU 18235 "http://127.0.0.1:$PORT_MOCK" "RUYI_SETS_FILE=$WORK/sets.json" "RUYI_BRAIN_GRADE_TOKEN=$GTOKEN" "RUYI_NTFY_BASE_URL=$NTFYB" "RUYI_ATTEMPTS_DIR=$WORK/afile/att"; wait_http "http://127.0.0.1:18235/healthz"
post_login qu "http://127.0.0.1:18235" "$CODE" -c "$WORK/jarQU"
gpost u1 "http://127.0.0.1:18235" demo-1 "$(mkbody 66666666-6666-4666-8666-666666666661)" -b "$WORK/jarQU"
gpost u2 "http://127.0.0.1:18235" demo-1 "$(mkbody 66666666-6666-4666-8666-666666666662)" -b "$WORK/jarQU"
expect "Q12 unwritable archive dir: still graded, archived:false (both submissions)" "$(jq -c '[.mode,.archived]' "$WORK/resp/u1.b")$(jq -c '[.mode,.archived]' "$WORK/resp/u2.b")" '["graded",false]["graded",false]'
expect "Q12b exactly one error log line for it, without path or content" "$(grep -c 'attempts archive is not writable' "$WORK/QU.log") $(grep -c -e "$WORK/afile" -e 'example.set' "$WORK/QU.log")" "1 0"
# --- Q13 ntfy down: grading result unaffected, one retry, nothing sensitive in the logs
nextday
start_site QX 18236 "http://127.0.0.1:$PORT_MOCK" "RUYI_SETS_FILE=$WORK/sets.json" "RUYI_BRAIN_GRADE_TOKEN=$GTOKEN" "RUYI_NTFY_BASE_URL=http://127.0.0.1:1" "RUYI_NTFY_RETRY_DELAY=300ms" "RUYI_ATTEMPTS_DIR=$WORK/att-QX"; wait_http "http://127.0.0.1:18236/healthz"
post_login qx "http://127.0.0.1:18236" "$CODE" -c "$WORK/jarQX"
gpost x1 "http://127.0.0.1:18236" demo-1 "$(mkbody 77777777-7777-4777-8777-777777777771)" -b "$WORK/jarQX"
expect "Q13 ntfy unreachable: 200, graded, notify:retrying (first attempt refused at once), archived" "$CODE_HTTP $(jq -c '[.mode,.notify,.archived]' "$WORK/resp/x1.b")" '200 ["graded","retrying",true]'
sleep 1.8   # attempts at 0, +300ms, +600ms
expect "Q13b three attempts, each logged with kind=dial and ms, then one 'giving up' warning" "$(grep -c 'ntfy attempt failed.*kind=dial.*ms=' "$WORK/QX.log") $(grep -c 'ntfy notification failed, giving up' "$WORK/QX.log")" "3 1"
grep -qE -e "$TOPIC" -e '127\.0\.0\.1:1([^0-9]|$)' "$WORK/QX.log" && bad "Q13c ntfy topic/address in the log" || ok "Q13c no ntfy topic or address in the log"
# --- Q14 old files untouched, logs clean, config validation
fails=0; while IFS= read -r f; do
  want=$(git -C "$ROOT" show "$MAIN_REF:$f" | sha256sum | cut -d' ' -f1); get f "$QA/$f"; got=$(sha256sum < "$WORK/resp/f.b" | cut -d' ' -f1)
  [ "$CODE_HTTP" = 200 ] && [ "$got" = "$want" ] || fails=$((fails+1)); done <<< "$MAIN_FILES"
expect "Q14 all $(echo "$MAIN_FILES" | wc -l) old static files still byte-identical to $MAIN_REF with grading enabled (diffs)" "$fails" 0
get f "$QA/m/numberline-4y/"; expect "Q14b numberline page unchanged" "$(sha256sum < "$WORK/resp/f.b" | cut -d' ' -f1)" "$(sha256sum < "$ROOT/m/numberline-4y/index.html" | cut -d' ' -f1)"
sleep 0.3
cat "$WORK/Q.log" "$WORK/QN.log" "$WORK/QD.log" "$WORK/QO.log" "$WORK/QF.log" "$WORK/QU.log" "$WORK/QX.log" > "$WORK/qlogs"
for pair in "bearer token:$TOKEN" "grade token:$GTOKEN" "family code:$CODE" "session secret:$SECRET" "ntfy topic:$TOPIC" "qref:example.set:math" "answer text:free text" "stem text:dfrac" "client answer:\"x\",\"y\""; do
  name="${pair%%:*}"; val="${pair#*:}"; grep -qF -- "$val" "$WORK/qlogs" && bad "Q15 $name appears in the logs of the P1–P3 sites" || ok "Q15 $name not in the logs of the P1–P3 sites"
done
grep -q 'grading unavailable; archive-only' "$WORK/qlogs" && ok "Q15b degraded grading is logged (kind only)" || bad "Q15b"
run_bad x "${FULL[@]}" RUYI_BRAIN_GRADE_TOKEN="$TOKEN"
[ "$RC" != 0 ] && echo "$OUT" | grep -q RUYI_BRAIN_GRADE_TOKEN && ! echo "$OUT" | grep -qF "$TOKEN" && ok "Q16 grade token equal to the read token -> refused, names the variable, no value" || bad "Q16" "rc=$RC"
for kv in RUYI_GRADE_MAX_PER_HOUR=0 RUYI_ATTEMPTS_DIR=relative RUYI_AUTO_SOURCES=BAD%20SRC RUYI_NTFY_BASE_URL=ftp://x RUYI_BRAIN_GRADE_PATH=nope; do
  run_bad x "${FULL[@]}" "${kv/\%20/ }"; [ "$RC" != 0 ] && echo "$OUT" | grep -q "${kv%%=*}" && ok "Q16b ${kv%%=*} invalid -> refused, variable named" || bad "Q16b ${kv%%=*}" "rc=$RC"
done
OUT=$(env -i PATH="$PATH" "${FULL[@]}" RUYI_LISTEN=127.0.0.1:18237 RUYI_ATTEMPTS_DIR="$WORK/att-never-created" timeout 2 "$BIN" 2>&1); RC=$?
[ "$RC" = 124 ] && [ ! -e "$WORK/att-never-created" ] && ok "Q17 startup does not touch the attempts dir until the first submission (lazy create)" || bad "Q17" "rc=$RC"
for p in "$PID_Q" "$PID_QN" "$PID_QD" "$PID_QO" "$PID_QF" "$PID_QU" "$PID_QX"; do kill "$p" 2>/dev/null; done


section "R. Brain final contract (#35): statuses, errors, idempotency, token hygiene, degradation — mock brain only"
cat > "$WORK/sets-r.json" <<JSON
{"sets":[
 {"id":"mix-4","title":"四态套","source":"$PSRC","qrefs":["example.set:math:1","example.set:math:2","example.set:math:3","example.set:math:4"]},
 {"id":"ghost-2","title":"缺题套","source":"$PSRC","qrefs":["example.set:math:2","example.set:math:99"]},
 {"id":"one-1","title":"单题套","source":"$PSRC","qrefs":["example.set:math:2"]}
]}
JSON
RM=18240; RS=18241; RB="http://127.0.0.1:$RS"; RMB="http://127.0.0.1:$RM"
start_mock mockR "$RM" "MOCK_GRADE_TOKEN=$GTOKEN"; wait_http "$RMB/_mock/stats"
RENV=("RUYI_SETS_FILE=$WORK/sets-r.json" "RUYI_BRAIN_GRADE_TOKEN=$GTOKEN" "RUYI_NTFY_BASE_URL=$NTFYB" "RUYI_NTFY_RETRY_DELAY=300ms" "RUYI_ATTEMPTS_DIR=$WORK/att-R" "RUYI_GRADE_MAX_PER_HOUR=100" "RUYI_AUTO_SOURCES=$PSRC" "RUYI_AUTO_TTL=60s")
start_site R "$RS" "$RMB" "${RENV[@]}"; wait_http "$RB/healthz"
post_login r1 "$RB" "$CODE" -c "$WORK/jarR"
rcid() { printf '%08d-aaaa-4aaa-8aaa-aaaaaaaaaaaa' "$1"; }
rbody() { printf '{"client_attempt_id":"%s"%s,"answers":%s}' "$1" "${3:-}" "$2"; }
MIXANS='[{"qref":"example.set:math:1","answer":["~review~","2","3"],"rev":1},{"qref":"example.set:math:2","answer":"B"},{"qref":"example.set:math:3","answer":["x","y"]},{"qref":"example.set:math:4","answer":"自由作答"}]'
N0=$(ntfy_n)
gpost m1 "$RB" mix-4 "$(rbody "$(rcid 1)" "$MIXANS" ',"answered_at":"2026-10-17T08:00:00Z"')" -b "$WORK/jarR"
expect "R1 four statuses in one batch -> 200 (needs_review / graded / pending_key / unsupported)" "$CODE_HTTP $(jq -c '[.results[]|.status]' "$WORK/resp/m1.b")" '200 ["needs_review","graded","pending_key","unsupported"]'
expect "R1b recorded per item: only unsupported is recorded:false; correct only on graded" "$(jq -c '[.results[]|[.recorded,.correct]]' "$WORK/resp/m1.b")" '[[true,null],[true,true],[true,null],[false,null]]'
expect "R1c brain_recorded true, recorded_count 3 of 4; summary carries unsupported:1" "$(jq -c '[.brain_recorded,.recorded_count,.summary.unsupported,.summary.recorded]' "$WORK/resp/m1.b")" '[true,3,1,3]'
expect "R1d every recorded item has the brain's integer attempt_id, the unsupported one has none" "$(jq -c '[.results[]|(.attempt_id|type)]' "$WORK/resp/m1.b")" '["number","number","number","null"]'
expect "R1e the brain wrote 3 rows (grade_new_item), not 4" "$(mstat grade_new_item "$RM")" 3
expect "R1f archive says the same: per-item recorded flags" "$(head -1 "$WORK/att-R/attempts.jsonl" | jq -c '[.mode,[.items[]|.recorded]]')" '["graded",[true,true,true,false]]'
expect "R1g ntfy line says 3 of 4 went to the brain, the rest archive-only (never a flat 已提交)" "$(jq -rs '.[-1].body|split("完成于 ")[0]' "$WORK/ntfy.jsonl")" '如意练习：四态套 4 题，对 1，待核对 2，不自动判 1，已提交（3 题记入大脑，其余仅留档）。attempt 00000001。'
# what went over the wire (the mock keeps the last grade body)
LG=$(curl -s "$RMB/_mock/last-grade")
expect "R2 wire format: brain=ruyi, id, items with qref/answer/rev/answered_at/session_ref, no token or family code in the body" "$(echo "$LG" | jq -c '[.brain,.client_attempt_id,(.items|length),(.items[0]|[.qref,.rev,.answered_at,.session_ref]),(.items|map(has("attempt_kind"))|any)]')" "[\"ruyi\",\"$(rcid 1)\",4,[\"example.set:math:1\",1,\"2026-10-17T08:00:00Z\",\"$(rcid 1)\"],false]"
echo "$LG" | grep -qF -e "$GTOKEN" -e "$TOKEN" -e "$CODE" && bad "R2b a token/family code is in the grade body" || ok "R2b grade body carries no token or family code"
expect "R2c the grade call used the grade token only (read token never accepted for grading)" "$(mstat grade_with_bank_read_token "$RM")" 0
# --- errors
G0=$(mstat grade "$RM"); L0=$(wc -l < "$WORK/att-R/attempts.jsonl" | tr -d ' '); N1=$(ntfy_n)
gpost m2 "$RB" mix-4 "$(rbody "$(rcid 1)" "$(echo "$MIXANS" | jq -c '.[1].answer="A"')")" -b "$WORK/jarR"
expect "R3 same client_attempt_id, changed answer -> 409 idempotency_conflict + retry_new_id (site-side check, brain not called)" "$CODE_HTTP $(jq -c '[.error,.retry_new_id]' "$WORK/resp/m2.b") $(( $(mstat grade "$RM") - G0 ))" '409 ["idempotency_conflict",true] 0'
gpost m3 "$RB" mix-4 "$(rbody "$(rcid 2)" "$(echo "$MIXANS" | jq -c '.[1].answer="A"')")" -b "$WORK/jarR"
expect "R3b ... the user's re-submit with changed answers gets a NEW id and is accepted (new write, wrong this time)" "$CODE_HTTP $(jq -c '[.mode,.results[1].correct,.results[1].duplicate]' "$WORK/resp/m3.b")" '200 ["graded",false,null]'
gpost m4 "$RB" mix-4 "$(rbody "$(rcid 3)" "$MIXANS")" -b "$WORK/jarR"
expect "R4 daily cap: 3rd write of the same questions the same day is still fine" "$CODE_HTTP" 200
L1=$(wc -l < "$WORK/att-R/attempts.jsonl" | tr -d ' '); N2=$(ntfy_n)
gpost m5 "$RB" mix-4 "$(rbody "$(rcid 4)" "$MIXANS")" -b "$WORK/jarR"
expect "R4b 4th write the same day -> 429 for the whole batch, with the page copy 今天这题的次数已用完，已留档，未记入大脑" "$CODE_HTTP $(jq -r '[.error,(.message|contains("今天这题的次数已用完，已留档，未记入大脑")),.archived,.brain_recorded,.brain_note,.mode,.recorded_count]|@json' "$WORK/resp/m5.b")" '429 ["daily_cap",true,true,false,"daily_cap","archive_only",0]'
expect "R4c ... but archived (exactly one new line: archive_only/daily_cap, no item recorded) and NOT notified" "$(( $(wc -l < "$WORK/att-R/attempts.jsonl" | tr -d ' ') - L1 )) $(( $(ntfy_n) - N2 )) $(tail -1 "$WORK/att-R/attempts.jsonl" | jq -c '[.mode,.brain_note,([.items[].recorded]|any),([.items[]|select(.capped)]|length>0)]')" '1 0 ["archive_only","daily_cap",false,true]'
G5=$(mstat grade "$RM"); L5=$(wc -l < "$WORK/att-R/attempts.jsonl" | tr -d ' ')
gpost m5c "$RB" mix-4 "$(rbody "$(rcid 4)" "$MIXANS")" -b "$WORK/jarR"
expect "R4c2 replay of the refused id -> 429 again (replayed:true), brain not called, no second line" "$CODE_HTTP $(jq -c '[.error,.replayed]' "$WORK/resp/m5c.b") $(( $(mstat grade "$RM") - G5 )) $(( $(wc -l < "$WORK/att-R/attempts.jsonl" | tr -d ' ') - L5 ))" '429 ["daily_cap",true] 0 0'
get qt "$RB/api/practice/mix-4/quota" -b "$WORK/jarR"
expect "R4c3 quota estimate: limit 3, basis site_archive, the most-used asked question is at 3, the refused attempt not counted" "$CODE_HTTP $(jq -c '[.limit,.basis,(.used|to_entries|map(.value)|max)]' "$WORK/resp/qt.b")" '200 [3,"site_archive",3]'
gpost m5b "$RB" mix-4 "$(rbody "$(rcid 1)" "$MIXANS" ',"answered_at":"2026-10-17T08:00:00Z"')" -b "$WORK/jarR"
expect "R4d replay of an accepted attempt is still served after the cap (site archive, no brain call)" "$(jq -c '[.replayed,.mode]' "$WORK/resp/m5b.b")" '[true,"graded"]'
curl -s -X POST "$RMB/_mock/bump-rev?qref=example.set:math:2" >/dev/null
L2=$(wc -l < "$WORK/att-R/attempts.jsonl" | tr -d ' '); nextday "$RM"
gpost m6 "$RB" one-1 "$(rbody "$(rcid 5)" '[{"qref":"example.set:math:2","answer":"B","rev":1}]')" -b "$WORK/jarR"
expect "R5 stale rev -> 409 rev_mismatch with the page copy 题目已更新，请刷新; nothing archived" "$CODE_HTTP $(jq -r '[.error,(.message|contains("题目已更新，请刷新"))]|@json' "$WORK/resp/m6.b") $(( $(wc -l < "$WORK/att-R/attempts.jsonl" | tr -d ' ') - L2 ))" '409 ["rev_mismatch",true] 0'
get pr "$RB/api/practice/one-1" -b "$WORK/jarR"
expect "R5b the practice API serves the current rev (2) so a reload fixes it" "$(jq -c '[.items[0].rev]' "$WORK/resp/pr.b")" '[2]'
gpost m7 "$RB" one-1 "$(rbody "$(rcid 6)" '[{"qref":"example.set:math:2","answer":"B","rev":2}]')" -b "$WORK/jarR"
expect "R5c ... resubmitting with rev 2 is accepted" "$CODE_HTTP $(jq -r .mode "$WORK/resp/m7.b")" "200 graded"
L3=$(wc -l < "$WORK/att-R/attempts.jsonl" | tr -d ' ')
G1=$(mstat grade "$RM")
gpost m8 "$RB" ghost-2 "$(rbody "$(rcid 7)" '[{"qref":"example.set:math:99","answer":"B"}]')" -b "$WORK/jarR"
expect "R6 a qref the brain does not know (lookup missing) -> 404 unknown_qref, nothing written, grade endpoint not called" "$CODE_HTTP $(jq -r .error "$WORK/resp/m8.b") $(( $(mstat grade "$RM") - G1 )) $(( $(wc -l < "$WORK/att-R/attempts.jsonl" | tr -d ' ') - L3 ))" "404 unknown_qref 0 0"
get gp "$RB/api/practice/ghost-2" -b "$WORK/jarR"
expect "R6b the set page still loads (missing qref listed, no crash)" "$CODE_HTTP $(jq -c '[(.items|length),.missing_qrefs]' "$WORK/resp/gp.b")" '200 [1,["example.set:math:99"]]'
gpost m9 "$RB" mix-4 "$(rbody "$(rcid 8)" '[{"qref":"example.set:math:2","answer":"B"},{"qref":"example.set:math:2","answer":"A"}]')" -b "$WORK/jarR"; expect "R7 duplicate qref in a batch -> 400 (never sent)" "$CODE_HTTP" 400
gpost m10 "$RB" mix-4 "$(rbody "$(rcid 9)" '[{"qref":"example.set:math:2","answer":"B","rev":"x"}]')" -b "$WORK/jarR"; expect "R7b non-integer rev -> 400" "$CODE_HTTP" 400
gpost m11 "$RB" mix-4 "$(rbody "$(rcid 10)" '[{"qref":"example.set:math:2","answer":"B"}]' ',"attempt_kind":"bogus"')" -b "$WORK/jarR"; expect "R7c attempt_kind other than correction|variant -> 400" "$CODE_HTTP" 400
gpost m12 "$RB" mix-4 "$(rbody "$(rcid 11)" '[{"qref":"example.set:math:2","answer":"B"}]' ',"attempt_kind":"correction"')" -b "$WORK/jarR"
expect "R8 attempt_kind=correction is accepted and forwarded to the brain" "$CODE_HTTP $(curl -s "$RMB/_mock/last-grade" | jq -c '[.items[0].attempt_kind]')" '200 ["correction"]'
# --- duplicate: the brain already has this id (site lost its archive) -> same attempt ids, duplicate:true, no new write
cp "$WORK/resp/m1.b" "$WORK/m1.first"
kill "$PID_R" 2>/dev/null; sleep 0.4; cp "$WORK/R.log" "$WORK/R1.log"
start_site R "$RS" "$RMB" "${RENV[@]}" "RUYI_ATTEMPTS_DIR=$WORK/att-R2"; wait_http "$RB/healthz"; post_login r2 "$RB" "$CODE" -c "$WORK/jarR"
W0=$(mstat grade_new_item "$RM"); D0=$(mstat grade_dup_item "$RM")
gpost m13 "$RB" mix-4 "$(rbody "$(rcid 1)" "$MIXANS" ',"answered_at":"2026-10-17T08:00:00Z"')" -b "$WORK/jarR"
expect "R9 network retry with the same id after the site lost its archive: brain answers duplicate:true with the SAME attempt ids" "$CODE_HTTP $(jq -c '[.results[]|.attempt_id]' "$WORK/resp/m13.b") $(jq -c '[.results[]|.duplicate]' "$WORK/resp/m13.b")" "200 $(jq -c '[.results[]|.attempt_id]' "$WORK/m1.first") [true,true,true,null]"
expect "R9b ... no new brain writes (grade_new_item +0; the brain counts all 4 items as duplicates)" "$(( $(mstat grade_new_item "$RM") - W0 )) $(( $(mstat grade_dup_item "$RM") - D0 ))" "0 4"
# --- recorded:false must never become "submitted to the brain"
expect "R10 archive-only submit (no grade token): every result recorded:false, brain_recorded false" "$(jq -c '[.mode,.brain_recorded,.recorded_count,([.results[].recorded]|any)]' "$WORK/resp/dgrQD.b" 2>/dev/null || echo skipped)" '["archive_only",false,0,false]'
# --- the dedicated grade token is server-side only: nothing leaks anywhere
for f in "$WORK"/resp/m*.b "$WORK"/resp/pr.b "$WORK"/resp/gp.b "$WORK"/resp/m*.h; do
  grep -qF -e "$GTOKEN" -e "$TOKEN" "$f" && bad "R11 token in $(basename "$f")" || :
done; ok "R11 neither token appears in any R response (bodies or headers)"
get pg2 "$RB/m/practice/"; grep -qF -e "$GTOKEN" -e "$TOKEN" "$WORK/resp/pg2.b" && bad "R11b token in the practice page" || ok "R11b practice page has no token"
cat "$WORK/R1.log" "$WORK/R.log" > "$WORK/rlogs0"
grep -qF -e "$GTOKEN" -e "$TOKEN" "$WORK/att-R/attempts.jsonl" "$WORK/att-R2/attempts.jsonl" "$WORK/ntfy.jsonl" "$WORK/rlogs0" && bad "R12 token in the archive / ntfy / log" || ok "R12 token marker not in the attempts archive, ntfy content, or the site log (grep)"
grep -qF -- "$GTOKEN" "$WORK"/resp/*.h 2>/dev/null && bad "R12b grade token in some response header" || ok "R12b grade token in no response header of the whole run"
grep -q 'brain refused the batch' "$WORK/rlogs0" && ok "R12c refusals are logged (kind/status/code only)" || bad "R12c"
# --- degradation: endpoints not deployed (404/501) -> graceful, never 500
sw() { curl -s -X POST "$RMB/_mock/switch?name=$1&v=$2" >/dev/null; }
sw lookup 501
get dl "$RB/api/practice/one-1" -b "$WORK/jarR"
expect "R13 lookup endpoint missing (501): the set page falls back to the bank list, 200, no rev" "$CODE_HTTP $(jq -c '[(.items|length),(.items[0].rev // "none")]' "$WORK/resp/dl.b")" '200 [1,"none"]'
nextday "$RM"; gpost m14 "$RB" one-1 "$(rbody "$(rcid 12)" '[{"qref":"example.set:math:2","answer":"B"}]')" -b "$WORK/jarR"
expect "R13b ... and grading still works without rev (graded, recorded)" "$CODE_HTTP $(jq -c '[.mode,.recorded_count]' "$WORK/resp/m14.b")" '200 ["graded",1]'
sw lookup on
sw pick 501
get pk1 "$RB/api/practice/new?source=$PSRC&n=3&mode=wrong_only" -b "$WORK/jarR"
expect "R14 pick endpoint missing (501): explicit 大脑接口未就绪, 501 (not 500)" "$CODE_HTTP $(jq -r '[.error,(.message|contains("大脑接口未就绪"))]|@json' "$WORK/resp/pk1.b")" '501 ["wrong_only_unavailable",true]'
get pk2 "$RB/api/practice/new?source=$PSRC&n=3&mode=due_first" -b "$WORK/jarR"; expect "R14b due_first likewise" "$CODE_HTTP" 501
get pk3 "$RB/api/practice/new?source=$PSRC&n=3" -b "$WORK/jarR"; expect "R14c plain random sets (P1–P3 behavior) keep working while pick is missing" "$CODE_HTTP" 200
get pk4 "$RB/api/practice/new?source=$PSRC&n=3&mode=bogus" -b "$WORK/jarR"; expect "R14d unknown mode -> 400" "$CODE_HTTP" 400
sw pick on
sw grade 501
nextday "$RM"; gpost m15 "$RB" one-1 "$(rbody "$(rcid 13)" '[{"qref":"example.set:math:2","answer":"B"}]')" -b "$WORK/jarR"
expect "R15 grade endpoint missing (501): 200 archive_only, brain_note not_ready, nothing recorded (not 500)" "$CODE_HTTP $(jq -c '[.mode,.brain_note,.brain_recorded,.recorded_count,.archived]' "$WORK/resp/m15.b")" '200 ["archive_only","not_ready",false,0,true]'
expect "R15b ... the answer is archived with mode archive_only" "$(tail -1 "$WORK/att-R2/attempts.jsonl" | jq -c '[.mode,.brain_note,([.items[].recorded]|any)]')" '["archive_only","not_ready",false]'
sw grade off; gpost m16 "$RB" one-1 "$(rbody "$(rcid 14)" '[{"qref":"example.set:math:2","answer":"B"}]')" -b "$WORK/jarR"
expect "R15c grade route 404 (not unknown_qref) is also 'not ready', not an error page" "$CODE_HTTP $(jq -r .brain_note "$WORK/resp/m16.b")" "200 not_ready"
sw grade fail; gpost m17 "$RB" one-1 "$(rbody "$(rcid 15)" '[{"qref":"example.set:math:2","answer":"B"}]')" -b "$WORK/jarR"
expect "R15d brain failing (500): archive_only, brain_note unavailable" "$CODE_HTTP $(jq -r '[.mode,.brain_note]|@json' "$WORK/resp/m17.b")" '200 ["archive_only","unavailable"]'
sw grade on
# --- skipping deferred picks (configurable; default is to mark them)
get pk5 "$RB/api/practice/new?source=$PSRC&n=3&mode=wrong_only" -b "$WORK/jarR"
expect "R16 default: deferred picks are kept and counted (marked 参考答案待核 by the page)" "$CODE_HTTP $(jq -c '[.mode,.count,.deferred]' "$WORK/resp/pk5.b")" '200 ["wrong_only",3,2]'
start_site RK 18242 "$RMB" "${RENV[@]}" RUYI_ATTEMPTS_DIR="$WORK/att-RK" RUYI_PICK_SKIP_DEFERRED=1; wait_http "http://127.0.0.1:18242/healthz"
post_login rk "http://127.0.0.1:18242" "$CODE" -c "$WORK/jarRK"
get pk6 "http://127.0.0.1:18242/api/practice/new?source=$PSRC&n=3&mode=wrong_only" -b "$WORK/jarRK"
expect "R16b RUYI_PICK_SKIP_DEFERRED=1: deferred picks are dropped (1 left)" "$CODE_HTTP $(jq -c '[.count,.deferred]' "$WORK/resp/pk6.b")" '200 [1,0]'
get pk7 "$RB/api/practice/$(jq -r .set_id "$WORK/resp/pk5.b")" -b "$WORK/jarR"
expect "R16c pick items carry origin and a deferred mark, never an answer; question text came from lookup" "$(jq -c '[.items[]|[.origin,(.ref_pending // false)]]' "$WORK/resp/pk7.b")" '[["wrong",false],["wrong","pending_key"],["wrong","unsupported_qtype"]]'
# --- startup: the two tokens must differ, and the message never prints either
run_bad x "${FULL[@]}" RUYI_BRAIN_GRADE_TOKEN="$TOKEN"
[ "$RC" != 0 ] && echo "$OUT" | grep -q RUYI_BRAIN_GRADE_TOKEN && ! echo "$OUT" | grep -qF "$TOKEN" && ok "R17 RUYI_BRAIN_GRADE_TOKEN == RUYI_BRAIN_TOKEN is refused at startup (no value printed)" || bad "R17" "rc=$RC"
for kv in RUYI_BRAIN_LOOKUP_PATH=nope RUYI_BRAIN_PICK_PATH=a/b RUYI_PICK_SKIP_DEFERRED=maybe; do
  run_bad x "${FULL[@]}" "$kv"; [ "$RC" != 0 ] && echo "$OUT" | grep -q "${kv%%=*}" && ok "R17b ${kv%%=*} invalid -> refused, variable named" || bad "R17b ${kv%%=*}" "rc=$RC"
done
cat "$WORK/rlogs0" "$WORK/RK.log" > "$WORK/rlogs"
for pair in "bearer token:$TOKEN" "grade token:$GTOKEN" "family code:$CODE" "answer text:自由作答" "answer text 2:\"x\",\"y\""; do
  name="${pair%%:*}"; val="${pair#*:}"; grep -qF -- "$val" "$WORK/rlogs" && bad "R18 $name appears in the R site logs" || ok "R18 $name not in the R site logs"
done
for p in "$PID_R" "$PID_RK"; do kill "$p" 2>/dev/null; done

section "S. Corrections against the brain developers' contract check — mock brain only"
cat > "$WORK/sets-s.json" <<JSON
{"sets":[
 {"id":"alias-2","title":"别名套","source":"$PSRC","qrefs":["example.set:math:2-old","Example.Set:Math:1"]},
 {"id":"free-1","title":"自由作答","source":"$PSRC","qrefs":["example.set:math:4"]},
 {"id":"one-s","title":"单题套","source":"$PSRC","qrefs":["example.set:math:2"]}
]}
JSON
SM=18260; SS=18261; SB="http://127.0.0.1:$SS"; SMB="http://127.0.0.1:$SM"
start_mock mockS "$SM" "MOCK_GRADE_TOKEN=$GTOKEN"; wait_http "$SMB/_mock/stats"
SENV=("RUYI_SETS_FILE=$WORK/sets-s.json" "RUYI_BRAIN_GRADE_TOKEN=$GTOKEN" "RUYI_NTFY_BASE_URL=$NTFYB" "RUYI_NTFY_RETRY_DELAY=300ms" "RUYI_ATTEMPTS_DIR=$WORK/att-S" "RUYI_GRADE_MAX_PER_HOUR=100" "RUYI_AUTO_SOURCES=$PSRC,ghost.source" "RUYI_AUTO_TTL=60s")
start_site S "$SS" "$SMB" "${SENV[@]}"; wait_http "$SB/healthz"
post_login s1 "$SB" "$CODE" -c "$WORK/jarS"
ssw() { curl -s -X POST "$SMB/_mock/switch?name=$1&v=$2" >/dev/null; }
sbody() { printf '{"client_attempt_id":"%s"%s,"answers":%s}' "$1" "${3:-}" "$2"; }
scid() { printf '%08d-bbbb-4bbb-8bbb-bbbbbbbbbbbb' "$1"; }
sleep 0.5
grep -q 'lookup is deployed' "$WORK/S.log" && ok "S0 startup probe (one read-only lookup of a non-existent qref): 'lookup is deployed' logged, startup unaffected" || bad "S0 probe line missing in the site log"
expect "S0b the probe wrote nothing (no grade call, no 400s)" "$(mstat grade "$SM") $(mstat lookup_400 "$SM")" "0 0"
# --- 1. 404s are told apart by structure, never by a code string
G0=$(mstat grade "$SM")
curl -s -X POST "$SMB/_mock/grade-unknown?qref=example.set:math:2" >/dev/null
gpost s2 "$SB" one-s "$(sbody "$(scid 1)" '[{"qref":"example.set:math:2","answer":"B"}]')" -b "$WORK/jarS"
expect "S1 grade 404 carrying details.qrefs (question not found) -> the site's unknown-question 404, nothing recorded" "$CODE_HTTP $(jq -r .error "$WORK/resp/s2.b") $(mstat grade_new_item "$SM")" "404 unknown_qref 0"
curl -s -X POST "$SMB/_mock/grade-unknown?qref=example.set:math:2&v=off" >/dev/null
ssw grade off
gpost s3 "$SB" one-s "$(sbody "$(scid 2)" '[{"qref":"example.set:math:2","answer":"B"}]')" -b "$WORK/jarS"
expect "S1b catch-all 404 (not_found / 'not found', no details) = route not deployed -> archive_only / not_ready, 200" "$CODE_HTTP $(jq -c '[.mode,.brain_note]' "$WORK/resp/s3.b")" '200 ["archive_only","not_ready"]'
ssw grade on
get s5 "$SB/api/practice/new?source=ghost.source&n=3&mode=wrong_only" -b "$WORK/jarS"
expect "S1d pick 404 'source not found' for a nonexistent source: an explicit error, NOT 'not deployed'" "$CODE_HTTP $(jq -r '[.error,(.message|contains("大脑接口未就绪"))]|@json' "$WORK/resp/s5.b")" '404 ["pick_source_not_found",false]'
ssw pick off
get s6 "$SB/api/practice/new?source=$PSRC&n=3&mode=wrong_only" -b "$WORK/jarS"
expect "S1e pick catch-all 404 = not deployed -> 501 大脑接口未就绪 (distinct from S1d)" "$CODE_HTTP $(jq -r '[.error,(.message|contains("大脑接口未就绪"))]|@json' "$WORK/resp/s6.b")" '501 ["wrong_only_unavailable",true]'
ssw pick on
# --- 2. 429
nextday "$SM"
for i in 11 12 13; do gpost sc$i "$SB" one-s "$(sbody "$(scid $i)" '[{"qref":"example.set:math:2","answer":"B"}]')" -b "$WORK/jarS"; done
L1=$(wc -l < "$WORK/att-S/attempts.jsonl" | tr -d ' ')
gpost s7 "$SB" one-s "$(sbody "$(scid 14)" '[{"qref":"example.set:math:2","answer":"B"}]')" -b "$WORK/jarS"
expect "S2 4th write the same Shanghai day -> 429 daily_cap, one unified message, Retry-After passed through (seconds), archived as one daily_cap line" "$CODE_HTTP $(jq -r .error "$WORK/resp/s7.b") $(grep -i '^retry-after:' "$WORK/resp/s7.h" | tr -d '\r' | awk '{print ($2>0 && $2<=86401)?"ok":"bad"}') $(( $(wc -l < "$WORK/att-S/attempts.jsonl" | tr -d ' ') - L1 ))" "429 daily_cap ok 1"
# --- 3./4. answered_at: only sent when valid
nextday "$SM"
gpost s8 "$SB" one-s "$(sbody "$(scid 21)" '[{"qref":"example.set:math:2","answer":"B"}]' ',"answered_at":"2020-01-01T00:00:00Z"')" -b "$WORK/jarS"
expect "S3 answered_at older than 7 days is left out (never sent): 200 graded, no field on the wire, no 400" "$CODE_HTTP $(jq -r .mode "$WORK/resp/s8.b") $(curl -s "$SMB/_mock/last-grade" | jq -c '.items[0]|has("answered_at")') $(mstat grade_400 "$SM")" "200 graded false 0"
FUT=$(date -u -d '+1 hour' +%Y-%m-%dT%H:%M:%SZ)
gpost s9 "$SB" one-s "$(sbody "$(scid 22)" '[{"qref":"example.set:math:2","answer":"B"}]' ",\"answered_at\":\"$FUT\"")" -b "$WORK/jarS"
expect "S3b answered_at in the future (> now+1min) is left out, not sent" "$CODE_HTTP $(curl -s "$SMB/_mock/last-grade" | jq -c '.items[0]|has("answered_at")') $(mstat grade_400 "$SM")" "200 false 0"
NOW=$(date -u +%Y-%m-%dT%H:%M:%SZ)
nextday "$SM"
gpost s10 "$SB" one-s "$(sbody "$(scid 23)" '[{"qref":"example.set:math:2","answer":"B"}]' ",\"answered_at\":\"$(date -u -d '-1 hour' +%Y-%m-%dT%H:%M:%SZ)\"")" -b "$WORK/jarS"
expect "S3c a valid answered_at is forwarded as RFC 3339" "$CODE_HTTP $(curl -s "$SMB/_mock/last-grade" | jq -r '.items[0].answered_at|test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:]+Z$")')" "200 true"
# the brain's own rule (mock mirrors it): same key + other answered_at -> 409; absent -> replay; 201 new / 200 replay
gdirect() { curl -s -D "$WORK/resp/gd.h" -o "$WORK/resp/gd.b" -w '%{http_code}' -H "Authorization: Bearer $GTOKEN" -H 'Content-Type: application/json' -d "$1" "$SMB/v1/bank/grade${2:-}"; }
nextday "$SM"
T1=$(date -u -d '-2 hour' +%Y-%m-%dT%H:%M:%SZ); T2=$(date -u -d '-3 hour' +%Y-%m-%dT%H:%M:%SZ)
gb() { printf '{"brain":"ruyi","client_attempt_id":"%s","items":[{"qref":"example.set:math:2","answer":"B"%s}]}' "$1" "$2"; }
expect "S4 brain: new write -> 201" "$(gdirect "$(gb dd-1 ",\"answered_at\":\"$T1\"")")" 201
expect "S4b brain: replay without answered_at -> 200 duplicate" "$(gdirect "$(gb dd-1 "")") $(jq -c '.items[0].duplicate' "$WORK/resp/gd.b")" "200 true"
expect "S4c brain: same key + a different answered_at -> 409 idempotency_conflict" "$(gdirect "$(gb dd-1 ",\"answered_at\":\"$T2\"")") $(jq -r .code "$WORK/resp/gd.b")" "409 idempotency_conflict"
expect "S4d brain: attempt_kind / session_ref / payload do not take part in the comparison (replay, 200)" "$(gdirect "$(gb dd-1 ",\"attempt_kind\":\"variant\",\"session_ref\":\"zz\",\"payload\":{\"a\":1}")")" 200
# --- strict 400s: the real contract rejects these; the site must never send them
expect "S5 grade: unknown body field -> 400 invalid_json (mock mirrors the brain)" "$(gdirect '{"brain":"ruyi","client_attempt_id":"x1","items":[{"qref":"example.set:math:2","answer":"B"}],"extra":1}') $(jq -r .code "$WORK/resp/gd.b")" "400 invalid_json"
expect "S5b grade: any query other than ?brain= -> 400; upper-case qref -> 400; answered_at out of range -> 400" "$(gdirect "$(gb x2 "")" '?foo=1') $(gdirect '{"brain":"ruyi","client_attempt_id":"x3","items":[{"qref":"Example.Set:Math:2","answer":"B"}]}') $(gdirect "$(gb x4 ',"answered_at":"2020-01-01T00:00:00Z"')")" "400 400 400"
B400=$(mstat grade_400 "$SM")
# answer length: the brain takes up to 2000 characters (code points, not bytes) per answer/blank (brain PR #47)
A2000=$(printf 'x%.0s' $(seq 1 2000)); A2001=$(printf 'x%.0s' $(seq 1 2001)); H2000=$(python3 -c 'print("字"*2000,end="")'); H2001=$(python3 -c 'print("字"*2001,end="")')
G0=$(mstat grade "$SM")
gpost s11 "$SB" free-1 "$(sbody "$(scid 31)" "[{\"qref\":\"example.set:math:4\",\"answer\":\"$A2000\"}]")" -b "$WORK/jarS"
expect "S5c an answer of exactly 2000 characters is SENT to the brain (mode graded, not archive_only/not_sent; this free-text qref is 'unsupported' at the mock, so recorded:false) and the brain accepts it (no 400)" "$CODE_HTTP $(jq -c '[.mode,.results[0].status]' "$WORK/resp/s11.b") $(( $(mstat grade "$SM") - G0 )) $(( $(mstat grade_400 "$SM") - B400 ))" '200 ["graded","unsupported"] 1 0'
gpost s11b "$SB" free-1 "$(sbody "$(scid 34)" "[{\"qref\":\"example.set:math:4\",\"answer\":\"$H2000\"}]")" -b "$WORK/jarS"
expect "S5c2 2000 Chinese characters (6000 bytes) count as 2000 characters: sent, accepted by the brain" "$CODE_HTTP $(jq -c '[.mode,.results[0].status]' "$WORK/resp/s11b.b") $(( $(mstat grade "$SM") - G0 )) $(( $(mstat grade_400 "$SM") - B400 ))" '200 ["graded","unsupported"] 2 0'
G1=$(mstat grade "$SM")
gpost s11c "$SB" free-1 "$(sbody "$(scid 35)" "[{\"qref\":\"example.set:math:4\",\"answer\":\"$A2001\"}]")" -b "$WORK/jarS"; C1=$CODE_HTTP
gpost s11d "$SB" free-1 "$(sbody "$(scid 36)" "[{\"qref\":\"example.set:math:4\",\"answer\":\"$H2001\"}]")" -b "$WORK/jarS"
expect "S5c3 2001 characters (ASCII or Chinese): the site refuses with 400 itself (free-text cap = the brain's cap); the brain is never called, nothing truncated" "$C1 $CODE_HTTP $(( $(mstat grade "$SM") - G1 )) $(( $(mstat grade_400 "$SM") - B400 ))" '400 400 0 0'
# the brain mock itself (mirrors the brain): 2000 per answer / per blank accepted, 2001 -> 400; array of 10 stays the cap
BLK=$(python3 -c 'import json;print(json.dumps(["字"*2000]*10,ensure_ascii=False))'); BLK1=$(python3 -c 'import json;print(json.dumps(["x"]*9+["字"*2001],ensure_ascii=False))')
expect "S5c4 brain mock: 10 blanks x 2000 characters -> 201" "$(gdirect "{\"brain\":\"ruyi\",\"client_attempt_id\":\"len-a\",\"items\":[{\"qref\":\"example.set:math:1\",\"answer\":$BLK}]}")" 201
expect "S5c5 brain mock: one blank of 2001 characters -> 400 validation_failed" "$(gdirect "{\"brain\":\"ruyi\",\"client_attempt_id\":\"len-b\",\"items\":[{\"qref\":\"example.set:math:1\",\"answer\":$BLK1}]}") $(jq -r .code "$WORK/resp/gd.b")" "400 validation_failed"
nextday "$SM"
gpost s12 "$SB" one-s "$(sbody "$(scid 32)" '[{"qref":"example.set:math:2","answer":"B","rev":0}]')" -b "$WORK/jarS"
expect "S5d a rev below 1 never reaches the brain: the site answers 400 bad_rev itself" "$CODE_HTTP $(jq -r .error "$WORK/resp/s12.b") $(mstat grade_400 "$SM")" "400 bad_rev 5"
# --- 5. rev_mismatch: details qref/rev/current_rev, the item is located by qref, never by index
curl -s -X POST "$SMB/_mock/bump-rev?qref=example.set:math:2" >/dev/null
gpost s13 "$SB" one-s "$(sbody "$(scid 33)" '[{"qref":"example.set:math:2","answer":"B","rev":1}]')" -b "$WORK/jarS"
expect "S6 rev_mismatch 409 (details qref/rev/current_rev, no index) -> 题目已更新，请刷新" "$CODE_HTTP $(jq -r '[.error,(.message|contains("题目已更新，请刷新"))]|@json' "$WORK/resp/s13.b")" '409 ["rev_mismatch",true]'
# --- 6. aliases and upper/lower case
nextday "$SM"
get s14 "$SB/api/practice/alias-2" -b "$WORK/jarS"
expect "S7 set page keeps the set's own strings (alias and mixed case); lookup answered canonical qrefs" "$CODE_HTTP $(jq -c '[.items[]|.qref]' "$WORK/resp/s14.b") $(jq -c '[.missing_qrefs // []]' "$WORK/resp/s14.b")" '200 ["example.set:math:2-old","Example.Set:Math:1"] [[]]'
gpost s15 "$SB" alias-2 "$(sbody "$(scid 41)" '[{"qref":"example.set:math:2-old","answer":"B"},{"qref":"Example.Set:Math:1","answer":["1","2","3"]}]')" -b "$WORK/jarS"
expect "S7b grade with an alias and a mixed-case qref: lower-cased on the wire, echoes matched back to the page's strings, both graded + recorded" "$CODE_HTTP $(jq -c '[.mode,.recorded_count,[.results[]|[.qref,.correct]]]' "$WORK/resp/s15.b")" '200 ["graded",2,[["example.set:math:2-old",true],["Example.Set:Math:1",true]]]'
expect "S7c on the wire: lower-case only" "$(curl -s "$SMB/_mock/last-grade" | jq -c '[.items[].qref]')" '["example.set:math:2-old","example.set:math:1"]'
# the same attempt again through the canonical qref is the same key -> a replay at the brain (200, duplicate), not a second write
W0=$(mstat grade_new_item "$SM")
gpost s16 "$SB" one-s "$(sbody "$(scid 41)" '[{"qref":"example.set:math:2","answer":"B"}]')" -b "$WORK/jarS"
expect "S7d alias and canonical qref share one idempotency key (no second write)" "$(( $(mstat grade_new_item "$SM") - W0 ))" 0
# --- picks: n limit, reasons vocabulary
get s17 "$SB/api/practice/new?source=$PSRC&n=20&mode=due_first" -b "$WORK/jarS"
expect "S8 n above 10 is lowered to 10 before asking the brain (no 400 at the brain), page gets a note" "$CODE_HTTP $(mstat pick_400 "$SM") $(jq -r '(.note // "")|contains("最多 10 题")' "$WORK/resp/s17.b")" "200 0 true"
ssw lookup off
get s4 "$SB/api/practice/one-s" -b "$WORK/jarS"
expect "S1c lookup 404 (catch-all) = not deployed -> fallback to the list, set page still 200" "$CODE_HTTP $(jq -c '[(.items|length)]' "$WORK/resp/s4.b")" "200 [1]"
ssw lookup on
# --- startup probe against a brain that has no lookup yet
start_mock mockS2 18262 "MOCK_GRADE_TOKEN=$GTOKEN"; wait_http "http://127.0.0.1:18262/_mock/stats"
curl -s -X POST "http://127.0.0.1:18262/_mock/switch?name=lookup&v=off" >/dev/null
start_site S2 18263 "http://127.0.0.1:18262" "${SENV[@]}" RUYI_ATTEMPTS_DIR="$WORK/att-S2"; wait_http "http://127.0.0.1:18263/healthz"; sleep 0.5
grep -q 'not deployed yet' "$WORK/S2.log" && ok "S9 probe against a brain without lookup: a warning, startup continues" || bad "S9 probe warning missing"
expect "S9b ... and the site is healthy" "$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:18263/healthz)" 200
# --- the whole run: nothing the brain would reject as 400 was ever sent by the site
expect "S10 the site never produced a 400-class request to the brain in this section (grade_400 only from the 5 direct mock probes S5/S5b x3/S5c5)" "$(mstat grade_400 "$SM") $(mstat pick_400 "$SM") $(mstat lookup_400 "$SM")" "5 0 0"
expect "S10b the R section never produced one either" "$(mstat grade_400 "$RM") $(mstat pick_400 "$RM") $(mstat lookup_400 "$RM")" "0 0 0"
cat "$WORK/S.log" "$WORK/S2.log" > "$WORK/slogs"
for pair in "bearer token:$TOKEN" "grade token:$GTOKEN" "family code:$CODE"; do
  grep -qF -- "${pair#*:}" "$WORK/slogs" && bad "S11 ${pair%%:*} appears in the S site logs" || ok "S11 ${pair%%:*} not in the S site logs"
done
for p in "$PID_S" "$PID_S2"; do kill "$p" 2>/dev/null; done

section "T. Local smoke set (smoke-test): served and judged by the site itself, never touches the brain"
# the deploy snippet itself is appended to the sets file here (jq), exactly as the DEPLOY doc says
jq '.sets += [input]' "$WORK/sets.json" "$SRV/deploy/sets.smoke-entry.json" > "$WORK/sets-t.json"
start_mock mockT 18260 "MOCK_GRADE_TOKEN=$GTOKEN"; wait_http "http://127.0.0.1:18260/_mock/stats"
TENV=("RUYI_SETS_FILE=$WORK/sets-t.json" "RUYI_BRAIN_GRADE_TOKEN=$GTOKEN" "RUYI_NTFY_BASE_URL=$NTFYB" "RUYI_NTFY_RETRY_DELAY=300ms" "RUYI_ATTEMPTS_DIR=$WORK/att-T" "RUYI_GRADE_MAX_PER_HOUR=100" "RUYI_AUTO_SOURCES=$PSRC" "RUYI_AUTO_TTL=60s")
start_site T 18261 "http://127.0.0.1:18260" "${TENV[@]}"; TA="http://127.0.0.1:18261"; wait_http "$TA/healthz"
sleep 0.6   # the startup lookup probe (read-only) has happened; count from here
post_login t1 "$TA" "$CODE" -c "$WORK/jarT"
mtotal() { curl -s "http://127.0.0.1:18260/_mock/stats" | jq -r '[.[]|numbers]|add // 0'; }
T0=$(mtotal)
get s "$TA/api/practice/smoke-test" -b "$WORK/jarT"; expect "T1 GET /api/practice/smoke-test -> 200, local:true, 1 question" "$CODE_HTTP $(jq -c '[.local,(.items|length),.items[0].qref]' "$WORK/resp/s.b")" '200 [true,1,"smoke:q1"]'
grep -qi -e '"answer' -e 'explanation' -e 'solution' "$WORK/resp/s.b" && bad "T2 response mentions an answer-like key" || ok "T2 no answer / explanation / solution key anywhere in the response"
grep -q '测试题，随便填' "$WORK/resp/s.b" && ok "T3 the stem says 测试题，随便填" || bad "T3 stem marker"
CODE_HTTP=$(curl -sS -o "$WORK/resp/c.b" -w '%{http_code}' -H 'Content-Type: application/json' -d '{"answers":[{"qref":"smoke:q1","answer":"B"}]}' -b "$WORK/jarT" "$TA/api/practice/smoke-test/check")
expect "T4 /check: right choice -> graded, correct:true; response has no answer key" "$CODE_HTTP $(jq -c '[.test,.results[0].status,.results[0].correct]' "$WORK/resp/c.b") $(grep -ci '"answer' "$WORK/resp/c.b")" '200 [true,"graded",true] 0'
CODE_HTTP=$(curl -sS -o "$WORK/resp/c.b" -w '%{http_code}' -H 'Content-Type: application/json' -d '{"answers":[{"qref":"smoke:q1","answer":"A"}]}' -b "$WORK/jarT" "$TA/api/practice/smoke-test/check")
expect "T5 /check: wrong choice -> correct:false" "$CODE_HTTP $(jq -c '.results[0].correct' "$WORK/resp/c.b")" '200 false'
CODE_HTTP=$(curl -sS -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' -d '{"answers":[{"qref":"example.set:math:1","answer":["1","2","3"]}]}' -b "$WORK/jarT" "$TA/api/practice/demo-1/check")
expect "T6 /check on a real set -> 404" "$CODE_HTTP" 404
CODE_HTTP=$(curl -sS -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' -d '{"answers":[{"qref":"smoke:q1","answer":"B"}]}' "$TA/api/practice/smoke-test/check"); expect "T7 /check anonymous -> 401" "$CODE_HTTP" 401
N0=$(ntfy_n)
CT1=33333333-3333-4333-8333-333333333331
gpost t "$TA" smoke-test "$(printf '{"client_attempt_id":"%s","answers":[{"qref":"smoke:q1","answer":"B"}]}' $CT1)" -b "$WORK/jarT"
expect "T8 submit -> 200 archive_only, brain_note test_set, test:true, nothing recorded" "$CODE_HTTP $(jq -c '[.mode,.brain_note,.test,.brain_recorded,.recorded_count,.archived,.notify,.results[0].status,.results[0].correct,.results[0].recorded]' "$WORK/resp/t.b")" '200 ["archive_only","test_set",true,false,0,true,"sent","graded",true,false]'
expect "T9 the brain got 0 requests of any kind (grade token IS configured; list/lookup/grade/pick/media all 0)" "$(( $(mtotal) - T0 ))" 0
expect "T10 archive line has test:true, mode archive_only, brain_note test_set, source local.smoke" "$(tail -1 "$WORK/att-T/attempts.jsonl" | jq -c '[.test,.mode,.brain_note,.source,.set_id,.items[0].recorded]')" '[true,"archive_only","test_set","local.smoke","smoke-test",false]'
expect "T11 one ntfy post" "$(( $(ntfy_n) - N0 ))" 1
python3 - "$WORK/ntfy.jsonl" > "$WORK/resp/ntfy_t.txt" <<'PY'
import sys, json, email.header
last = [json.loads(l) for l in open(sys.argv[1]) if l.strip()][-1]
t = str(email.header.make_header(email.header.decode_header(last["title"])))
body = last["body"]
print(json.dumps({"title_has_tag": t.startswith("【测试】"), "title_ascii_on_wire": all(ord(c) < 127 for c in last["title"]), "body_starts_tag": body.startswith("【测试】"), "body_no_json": "{" not in body and "\n" not in body, "says_not_in_brain": "未记入大脑" in body}, ensure_ascii=False))
PY
expect "T12 ntfy: Title starts with 【测试】 (RFC 2047 on the wire), body starts with 【测试】, no JSON, says 未记入大脑" "$(cat "$WORK/resp/ntfy_t.txt")" '{"title_has_tag": true, "title_ascii_on_wire": true, "body_starts_tag": true, "body_no_json": true, "says_not_in_brain": true}'
# a real set next to it is unchanged: graded by the brain, plain ntfy title, no test fields
N1=$(ntfy_n); T1=$(mtotal)
gpost t "$TA" demo-1 "$(mkbody 33333333-3333-4333-8333-333333333332)" -b "$WORK/jarT"
expect "T13 real set on the same server: graded by the brain, no test key in the response" "$CODE_HTTP $(jq -c '[.mode,(has("test"))]' "$WORK/resp/t.b")" '200 ["graded",false]'
expect "T14 real set: archive line has no test key, ntfy title is the plain 'Ruyi practice', no 【测试】" "$(tail -1 "$WORK/att-T/attempts.jsonl" | jq -c 'has("test")') $(tail -1 "$WORK/ntfy.jsonl" | jq -r '.title') $(tail -1 "$WORK/ntfy.jsonl" | jq -r '.body' | grep -c '测试')" 'false Ruyi practice 0'
[ "$(mtotal)" -gt "$T1" ] && ok "T15 the real set did use the brain (sanity: the counter works)" || bad "T15 sanity"
# not auto-selectable
get s "$TA/api/practice/new?source=local.smoke&n=1" -b "$WORK/jarT"; expect "T16 /api/practice/new?source=local.smoke -> 400" "$CODE_HTTP" 400
get s "$TA/api/practice/new?source=$PSRC&n=5&order=seq" -b "$WORK/jarT"; expect "T17 /api/practice/new picks only from the brain source (id auto-…, no smoke qrefs)" "$CODE_HTTP $(jq -r '.set_id|startswith("auto-")' "$WORK/resp/s.b")" '200 true'
SETID=$(jq -r .set_id "$WORK/resp/s.b"); get s "$TA/api/practice/$SETID" -b "$WORK/jarT"; grep -q 'smoke:' "$WORK/resp/s.b" && bad "T18 an auto set contains a smoke qref" || ok "T18 the auto set has no smoke qref"
# config refuses the reserved source
for V in RUYI_AUTO_SOURCES RUYI_ALLOWED_SOURCES; do
  run_bad x "${FULL[@]}" "$V=local.smoke"
  [ "$RC" != 0 ] && echo "$OUT" | grep -q "$V" && ok "T19 $V=local.smoke -> refused at startup, names $V" || bad "T19 $V" "rc=$RC out=$OUT"
done
# bad local sets fail at load
printf '{"sets":[{"id":"demo-9","local":true,"title":"t","questions":[{"qref":"smoke:SECRET-QREF-MARK","qtype":"single_choice","stem":"s","options":[{"key":"A","text":"1"},{"key":"B","text":"2"}],"answer":["A"]}]}]}' > "$WORK/badlocal.json"
run_bad x "${FULL[@]}" RUYI_SETS_FILE="$WORK/badlocal.json"
[ "$RC" != 0 ] && echo "$OUT" | grep -q RUYI_SETS_FILE && ! echo "$OUT" | grep -q -e SECRET-QREF-MARK && ok "T20 local set with a non-smoke-/test- id -> refused, names RUYI_SETS_FILE, no content echoed" || bad "T20" "rc=$RC out=$OUT"
OUT=$(env -i PATH="$PATH" "${FULL[@]}" RUYI_SETS_FILE="$SRV/deploy/sets.smoke.example.json" RUYI_LISTEN=127.0.0.1:18216 timeout 2 "$BIN" 2>&1); RC=$?
[ "$RC" = 124 ] && ok "T21 deploy/sets.smoke.example.json is accepted at startup" || bad "T21" "rc=$RC $OUT"
cat "$WORK"/T.log > "$WORK/tlogs"
for pair in "family code:$CODE" "grade token:$GTOKEN" "ntfy topic:$TOPIC" "bearer token:$TOKEN"; do
  grep -qF -- "${pair#*:}" "$WORK/tlogs" && bad "T22 ${pair%%:*} appears in the T site log" || ok "T22 ${pair%%:*} not in the T site log"
done
kill "$PID_T" "$PID_mockT" 2>/dev/null

section "U. RUYI_BRAIN_ID — a configurable brain id (default ruyi; qa-practice for integration) — mock brain only"
cat > "$WORK/sets-u.json" <<JSON
{"sets":[{"id":"one-t","title":"单题套","source":"$PSRC","qrefs":["example.set:math:2"]}]}
JSON
TB_ANS='[{"qref":"example.set:math:2","answer":"B"}]'
TENV=("RUYI_SETS_FILE=$WORK/sets-u.json" "RUYI_BRAIN_GRADE_TOKEN=$GTOKEN" "RUYI_NTFY_BASE_URL=$NTFYB" "RUYI_ATTEMPTS_DIR=$WORK/att-T" "RUYI_GRADE_MAX_PER_HOUR=100" "RUYI_AUTO_SOURCES=$PSRC" "RUYI_AUTO_TTL=60s")
# U-A: the default (RUYI_BRAIN_ID unset) against a default mock: everything is "ruyi", exactly as before
start_mock mockT0 18270 "MOCK_GRADE_TOKEN=$GTOKEN"; wait_http "http://127.0.0.1:18270/_mock/stats"
start_site T0 18271 "http://127.0.0.1:18270" "${TENV[@]}" "RUYI_ATTEMPTS_DIR=$WORK/att-T0"; wait_http "http://127.0.0.1:18271/healthz"
post_login t0 "http://127.0.0.1:18271" "$CODE" -c "$WORK/jarT0"
get t0 "http://127.0.0.1:18271/api/practice/new?source=$PSRC&n=2&mode=due_first&brain=evil" -b "$WORK/jarT0"; expect "U1 default: pick-practice answered by the ruyi mock (an extra ?brain= on the site URL is ignored)" "$CODE_HTTP" 200
gpost t0 "http://127.0.0.1:18271" one-t "$(mkbody 00000001-bbbb-4bbb-8bbb-bbbbbbbbbbbb "$TB_ANS")" -b "$WORK/jarT0"
expect "U2 default: graded and recorded by the brain" "$CODE_HTTP $(jq -c '[.mode,.brain_recorded]' "$WORK/resp/t0.b")" '200 ["graded",true]'
expect "U3 default: the grade body says brain=ruyi" "$(curl -s http://127.0.0.1:18270/_mock/last-grade | jq -r .brain)" ruyi
grep -q 'brain_id=ruyi' "$WORK/T0.log" && ok "U4 startup summary shows the effective brain id (ruyi)" || bad "U4 startup summary"
# pick-practice needs the brain's write scope: the grade token works, the bank:read token is 403 (checked on the mock directly)
PURL="http://127.0.0.1:18270/v1/brain/pick-practice?source=$PSRC&n=2&mode=due_first&brain=ruyi"
expect "U4b mock: pick with the grade token -> 200" "$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $GTOKEN" "$PURL")" 200
expect "U4c mock: pick with the bank:read token -> 403" "$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" "$PURL")" 403
expect "U4d the site's own pick (T1) never used the read token (mock counted 1: only the direct probe T4c)" "$(mstat pick_with_bank_read_token 18270)" 1
# no grade token configured: pick is "not ready" (501), the mock receives no pick request at all, the read token is not used instead
P0=$(mstat pick 18270)
start_site T0N 18279 "http://127.0.0.1:18270" "RUYI_SETS_FILE=$WORK/sets-u.json" "RUYI_ATTEMPTS_DIR=$WORK/att-T0N" "RUYI_AUTO_SOURCES=$PSRC"; wait_http "http://127.0.0.1:18279/healthz"
post_login t0n "http://127.0.0.1:18279" "$CODE" -c "$WORK/jarT0N"
get t0n "http://127.0.0.1:18279/api/practice/new?source=$PSRC&n=2&mode=due_first" -b "$WORK/jarT0N"
expect "U4e no grade token: due_first -> 501 due_first_unavailable (not ready), message names the grade token" "$CODE_HTTP $(jq -r '[.error,(.message|contains("判分令牌"))]|@csv' "$WORK/resp/t0n.b")" '501 "due_first_unavailable",true'
get t0n "http://127.0.0.1:18279/api/practice/new?source=$PSRC&n=2&wrong_only=1" -b "$WORK/jarT0N"; expect "U4f ... and wrong_only -> 501" "$CODE_HTTP" 501
expect "U4g ... and the mock received 0 pick requests from it" "$(( $(mstat pick 18270) - P0 ))" 0
grep -qF -e "$TOKEN" -e "$GTOKEN" "$WORK/T0.log" "$WORK/T0N.log" "$WORK/resp/t0n.b" && bad "U4h a token appears in the site logs / responses" || ok "U4h no token in the T site logs or the 501 response"
kill "$PID_T0N" 2>/dev/null
# the qa-practice sample is a TEMPLATE with <placeholders> (no real brain identifiers in the public repo): check its shape,
# swap in fixture values, then it must load and start
QAX="$SRV/deploy/sets.qa-practice.example.json"
expect "U4i the sample is a template: ids, 3 source placeholders, 7 numbered qref placeholders, a real set titled 联调用，非线上" "$(jq -c '[(.sets|map(.id)),(.sets|map(.source)|map(test("^<qa-practice-source(-[0-9]+)?>$"))|all),([.sets[].qrefs[]]),(.sets|map(.title|contains("联调用，非线上"))|all)]' "$QAX")" '[["qa-practice-1","qa-practice-2","qa-practice-alt"],true,["<qref-lowercase-1>","<qref-lowercase-2>","<qref-lowercase-3>","<qref-lowercase-4>","<qref-lowercase-5>","<qref-lowercase-6>","<qref-lowercase-7>"],true]'
OUT=$(env -i PATH="$PATH" "${FULL[@]}" RUYI_SETS_FILE="$QAX" RUYI_LISTEN=127.0.0.1:18277 timeout 2 "$BIN" 2>&1); RC=$?
[ "$RC" != 0 ] && [ "$RC" != 124 ] && echo "$OUT" | grep -q RUYI_SETS_FILE && ok "U4j the template with <placeholders> is refused at startup (names RUYI_SETS_FILE)" || bad "U4j template accepted or wrong error" "rc=$RC $OUT"
sed -E -e 's/<qa-practice-source(-[0-9]+)?>/qa-fixture.src\1/' -e 's/<qref-lowercase-([0-9]+)>/qa-fixture.src:q:\1/' "$QAX" > "$WORK/sets-qa.json"
grep -q '[<>]' "$WORK/sets-qa.json" && bad "U4k placeholder left after substitution" || ok "U4k all placeholders substituted with fixture values"
OUT=$(env -i PATH="$PATH" "${FULL[@]}" RUYI_SETS_FILE="$WORK/sets-qa.json" RUYI_LISTEN=127.0.0.1:18278 timeout 2 "$BIN" 2>&1); RC=$?
[ "$RC" = 124 ] && ok "U4l the sample (placeholders replaced by fixture values) is accepted at startup (kept running until the 2 s timeout)" || bad "U4l qa-practice sets file" "rc=$RC $OUT"
# U-B: RUYI_BRAIN_ID=qa-practice against a mock that serves only qa-practice
start_mock mockT1 18272 "MOCK_GRADE_TOKEN=$GTOKEN" MOCK_BRAIN_ID=qa-practice; wait_http "http://127.0.0.1:18272/_mock/stats"
start_site T1 18273 "http://127.0.0.1:18272" "${TENV[@]}" "RUYI_ATTEMPTS_DIR=$WORK/att-T1" RUYI_BRAIN_ID=qa-practice; wait_http "http://127.0.0.1:18273/healthz"
post_login t1 "http://127.0.0.1:18273" "$CODE" -c "$WORK/jarT1"
get t1 "http://127.0.0.1:18273/api/practice/new?source=$PSRC&n=2&mode=due_first&brain=ruyi" -b "$WORK/jarT1"; expect "U5 qa-practice: pick-practice accepted (the mock would 404 any other brain; ?brain=ruyi on the site URL is ignored)" "$CODE_HTTP" 200
gpost t1 "http://127.0.0.1:18273" one-t "$(mkbody 00000002-bbbb-4bbb-8bbb-bbbbbbbbbbbb "$TB_ANS")" -b "$WORK/jarT1"
expect "U6 qa-practice: graded and recorded:true by the brain" "$CODE_HTTP $(jq -c '[.mode,.brain_recorded,.recorded_count]' "$WORK/resp/t1.b")" '200 ["graded",true,1]'
expect "U7 qa-practice: the grade body says brain=qa-practice" "$(curl -s http://127.0.0.1:18272/_mock/last-grade | jq -r .brain)" qa-practice
expect "U8 qa-practice: the mock saw no 400-class request (grade/pick/lookup)" "$(mstat grade_400 18272) $(mstat pick_400 18272) $(mstat lookup_400 18272)" "0 0 0"
grep -q 'brain_id=qa-practice' "$WORK/T1.log" && ok "U9 startup summary shows brain id qa-practice" || bad "U9 startup summary"
# U-C: scope mismatch (default site -> a qa-practice-only mock): the brain refuses, the page never claims a brain write
start_site T2 18274 "http://127.0.0.1:18272" "${TENV[@]}" "RUYI_ATTEMPTS_DIR=$WORK/att-T2"; wait_http "http://127.0.0.1:18274/healthz"
post_login t2 "http://127.0.0.1:18274" "$CODE" -c "$WORK/jarT2"
get t2 "http://127.0.0.1:18274/api/practice/new?source=$PSRC&n=2&mode=due_first" -b "$WORK/jarT2"; [ "$CODE_HTTP" != 200 ] && ok "U10 scope mismatch: pick-practice for ruyi refused by the qa-practice mock ($CODE_HTTP)" || bad "U10 mismatch pick"
gpost t2 "http://127.0.0.1:18274" one-t "$(mkbody 00000003-bbbb-4bbb-8bbb-bbbbbbbbbbbb "$TB_ANS")" -b "$WORK/jarT2"
expect "U11 scope mismatch: the page never claims the brain recorded it" "$(jq -r '.brain_recorded // false' "$WORK/resp/t2.b")" false
# U-D: startup validation — the bad value is never printed
run_bad x "${FULL[@]}" RUYI_BRAIN_ID='Bad Brain/1'
[ "$RC" != 0 ] && echo "$OUT" | grep -q 'RUYI_BRAIN_ID' && ! echo "$OUT" | grep -qF 'Bad Brain' && ok "U12 invalid RUYI_BRAIN_ID -> exit $RC, names the variable, prints no value" || bad "U12 invalid brain id" "rc=$RC"
for p in "$PID_T0" "$PID_T1" "$PID_T2"; do kill "$p" 2>/dev/null; done

section "W. S1 pending-answer queue (RUYI_QUEUE_ENABLED) — mock brain + mock ntfy only; contract v1.0"
NTOKEN="notify-$(rnd)"; WQ=18300; WS=18301
start_mock mockWQ "$WQ" "MOCK_GRADE_TOKEN=$GTOKEN" MOCK_QUEUE=1 "MOCK_NOTIFY_TOKEN=$NTOKEN"; wait_http "http://127.0.0.1:$WQ/_mock/stats"
WENV=("RUYI_SETS_FILE=$WORK/sets.json" "RUYI_BRAIN_GRADE_TOKEN=$GTOKEN" "RUYI_NTFY_BASE_URL=$NTFYB" "RUYI_NTFY_RETRY_DELAY=300ms" "RUYI_ATTEMPTS_DIR=$WORK/att-W" "RUYI_GRADE_MAX_PER_HOUR=200" \
      "RUYI_QUEUE_ENABLED=1" "RUYI_QUEUE_NOTIFY_ENABLED=1" "RUYI_BRAIN_NOTIFY_TOKEN=$NTOKEN" "RUYI_QUEUE_STATE_DIR=$WORK/q-W" "RUYI_QUEUE_POLL_SECONDS=40")
mkdir -p "$WORK/q-W"; chmod 700 "$WORK/q-W"
start_site W "$WS" "http://127.0.0.1:$WQ" "${WENV[@]}"; WA="http://127.0.0.1:$WS"; wait_http "$WA/healthz"
post_login w1 "$WA" "$CODE" -c "$WORK/jarW"
wq() { curl -s "http://127.0.0.1:$WQ/_mock/queue/stats" | jq -r ".$1 // 0"; }
WC=33333333-3333-4333-8333-333333333333
N0=$(ntfy_n)
gpost w1 "$WA" demo-1 "$(mkbody $WC)" -b "$WORK/jarW"
expect "W1 grade with the queue on: 200, graded items untouched, the pending_key item queued" "$CODE_HTTP $(jq -c '[.queue.enqueue,(.queue.items|map([.qref,.enq]))]' "$WORK/resp/w1.b")" '200 ["ok",[["example.set:math:3","ok"]]]'
expect "W1b only the pending answer reached the queue (graded ones are not queued), one batch, top-level session_ref" "$(wq enqueue_new_item) $(wq enqueue_calls) $(wq enqueue_400)" "1 1 0"
expect "W1c the heads-up was sent once and the response is otherwise the #22 shape (queue is the only added key)" "$(( $(ntfy_n) - N0 )) $(jq -c 'keys' "$WORK/resp/w1.b")" '1 ["archived","brain_recorded","client_attempt_id","mode","notify","queue","recorded_count","results","set_id","summary"]'
get wv "$WA/api/practice/attempts/$WC/queue" -b "$WORK/jarW"
expect "W2 status page data: waiting, 待解答 label with 24 hours, reanalyze not offered" "$CODE_HTTP $(jq -c '[.items[0].ui_state,.items[0].hours,.items[0].reanalyze_enabled,(.items[0].label|test("待解答"))]' "$WORK/resp/wv.b")" '200 ["waiting",24,false,true]'
E0=$(wq enqueue_calls); get wv2 "$WA/api/practice/attempts/$WC/queue" -b "$WORK/jarW"; get wv3 "$WA/api/practice/demo-1" -b "$WORK/jarW"
expect "W3 opening the page / the status GET never queues anything (pure read)" "$(( $(wq enqueue_calls) - E0 ))" 0
get wv4 "$WA/api/practice/attempts/$WC/queue"; expect "W3b the status endpoint needs a login" "$CODE_HTTP" 401
get wv5 "$WA/api/practice/attempts/22222222-2222-4222-8222-222222222222/queue" -b "$WORK/jarW"; expect "W3c unknown submission -> 404" "$CODE_HTTP" 404
curl -s -X POST "http://127.0.0.1:$WQ/_mock/queue/advance?qref=example.set:math:3&to=claimed" >/dev/null
sleep 5.5   # the site caches a status for 5 s
get wv "$WA/api/practice/attempts/$WC/queue" -b "$WORK/jarW"
expect "W4 claimed -> AI 判卷中" "$(jq -r '.items[0].ui_state' "$WORK/resp/wv.b")" ai_working
curl -s -X POST --get "http://127.0.0.1:$WQ/_mock/queue/advance" --data-urlencode "qref=example.set:math:3" --data-urlencode "to=compared" --data-urlencode "answer=AI-拟答案 12" --data-urlencode "rationale=AI-解析全文" --data-urlencode "compare=mismatch" >/dev/null
sleep 5.5   # the site caches a status for 5 s
get wv "$WA/api/practice/attempts/$WC/queue" -b "$WORK/jarW"
expect "W5 compared -> AI初评·待家长核 with the FULL AI text and the plain 「AI 没有一致答案，需要家长看」 note (no judgement of the child's answer)" "$(jq -c '[.items[0].ui_state,.items[0].label,.items[0].result.proposed_answer,.items[0].result.rationale,(.items[0].compare_text == "AI 没有一致答案，需要家长看")]' "$WORK/resp/wv.b")" '["ai_first_pass","AI 初评 · 待家长核","AI-拟答案 12","AI-解析全文",true]'
say "     waiting for the notify poller (45 s ±10%) ..."
for _ in $(seq 1 80); do [ "$(grep -c 'AI初评已出' "$WORK/ntfy.jsonl")" -ge 1 ] && break; sleep 1; done
expect "W6 exactly one 'AI初评已出，待家长核' notification, no answer text in it" "$(grep -c 'AI初评已出，待家长核' "$WORK/ntfy.jsonl") $(grep 'AI初评已出' "$WORK/ntfy.jsonl" | grep -c 'AI-拟答案\|AI-解析')" "1 0"
expect "W6b the events were acked (ack after ingest), only the notify token touched events/ack" "$(wq ack_ok | awk '{print ($1>=1)}') $(wq write_token_on_events_endpoint) $(wq notify_token_on_write_endpoint) $(wq stats_calls)" "1 0 0 0"
ls "$WORK/q-W" | tr '\n' ' ' | grep -q 'links.jsonl' && ls "$WORK/q-W" | grep -q 'notify.json' && ls "$WORK/q-W" | grep -q 'sent.jsonl' && ok "W7 state files exist (links.jsonl, notify.json, sent.jsonl)" || bad "W7 state files" "$(ls "$WORK/q-W")"
expect "W7b state files are private (600) and the dir 700" "$(stat -c %a "$WORK/q-W/links.jsonl") $(stat -c %a "$WORK/q-W/notify.json") $(stat -c %a "$WORK/q-W")" "600 600 700"
# restart: nothing is told twice, the status survives
kill "$PID_W" 2>/dev/null; sleep 0.5
start_site W2 "$WS" "http://127.0.0.1:$WQ" "${WENV[@]}"; wait_http "$WA/healthz"; sleep 3
expect "W8 restart: no second notification" "$(grep -c 'AI初评已出' "$WORK/ntfy.jsonl")" 1
post_login w1 "$WA" "$CODE" -c "$WORK/jarW"; get wv "$WA/api/practice/attempts/$WC/queue" -b "$WORK/jarW"
expect "W8b restart: the status is still there (state files)" "$(jq -r '.items[0].ui_state' "$WORK/resp/wv.b")" ai_first_pass
gpost w1r "$WA" demo-1 "$(mkbody $WC)" -b "$WORK/jarW"
expect "W8c replay of the same submission keeps the queue state and queues nothing new" "$(jq -c '[.replayed,.queue.items[0].enq]' "$WORK/resp/w1r.b") $(wq enqueue_calls)" "[true,\"ok\"] 1"
# purged item: the page says deleted, no text
WC2=44444444-4444-4444-8444-444444444444
gpost w2 "$WA" demo-1 "$(mkbody $WC2 '[{"qref":"example.set:math:3","answer":["p","q"]}]')" -b "$WORK/jarW"
curl -s -X POST "http://127.0.0.1:$WQ/_mock/queue/advance?qref=example.set:math:3&to=purged" >/dev/null
get wv "$WA/api/practice/attempts/$WC2/queue" -b "$WORK/jarW"
expect "W9 purged -> 家长已删除这道题的解析, no AI text" "$(jq -c '[.items[0].ui_state,.items[0].label,(.items[0].result // null)]' "$WORK/resp/wv.b")" '["purged","家长已删除这道题的解析",null]'
# unsupported (free-3): the enqueue writes the attempt
gpost w3 "$WA" free-3 "$(mkbody 55555555-5555-4555-8555-555555555555 '[{"qref":"example.set:math:4","answer":"x"}]')" -b "$WORK/jarW"
expect "W10 unsupported answer: queued, recorded by the enqueue (not 不入大脑 any more)" "$(jq -c '[.queue.items[0].enq,.recorded_count,.results[0].recorded]' "$WORK/resp/w3.b")" '["ok",1,true]'
get wq1 "$WA/api/practice/free-3/quota" -b "$WORK/jarW"
expect "W10b the daily quota counts the answer the enqueue wrote ('约还剩N次' now 2)" "$(jq -c '[.used["example.set:math:4"]]' "$WORK/resp/wq1.b")" '[1]'
get wcfg "$WA/api/config" -b "$WORK/jarW"
expect "W10c queue on: /api/config carries queue=true (the page words the daily limit by it)" "$(jq -c '.queue' "$WORK/resp/wcfg.b")" 'true'
expect "W10d queue off (default site): /api/config has no queue key (= #22)" "$(printf %s "$CONFIG_BODY" | jq -c 'has("queue")')" 'false'
# enqueue 500 -> submission still succeeds, retry is automatic
curl -s -X POST "http://127.0.0.1:$WQ/_mock/queue/config?enqueue_mode=500" >/dev/null
gpost w4 "$WA" demo-1 "$(mkbody 66666666-6666-4666-8666-666666666666 '[{"qref":"example.set:math:3","answer":["m","n"]}]')" -b "$WORK/jarW"
expect "W11 enqueue failing: the submission is still 200 + archived, item marked enqueue failed (auto retry)" "$CODE_HTTP $(jq -c '[.archived,.queue.items[0].enq]' "$WORK/resp/w4.b")" '200 [true,"failed"]'
curl -s -X POST "http://127.0.0.1:$WQ/_mock/queue/config?enqueue_mode=" >/dev/null
CODE_HTTP=$(curl -sS -o "$WORK/resp/w4e.b" -w '%{http_code}' -H 'Content-Type: application/json' -d '{}' -b "$WORK/jarW" "$WA/api/practice/attempts/66666666-6666-4666-8666-666666666666/enqueue")
get wv "$WA/api/practice/attempts/66666666-6666-4666-8666-666666666666/queue" -b "$WORK/jarW"
expect "W11b the single page-triggered retry queued it (idempotent: brain items +1 only)" "$CODE_HTTP $(jq -r '.items[0].ui_state' "$WORK/resp/wv.b")" '200 waiting'
# an old brain (no enqueue endpoint: 404): "not queued", no retry, no failed try
curl -s -X POST "http://127.0.0.1:$WQ/_mock/queue/config?enqueue_mode=off" >/dev/null
W404=88888888-8888-4888-8888-888888888888; E0=$(wq enqueue_calls)
gpost w5 "$WA" free-3 "$(mkbody $W404 '[{"qref":"example.set:math:4","answer":"o"}]')" -b "$WORK/jarW"
CODE_HTTP=$(curl -sS -o "$WORK/resp/w5e.b" -w '%{http_code}' -H 'Content-Type: application/json' -d '{}' -b "$WORK/jarW" "$WA/api/practice/attempts/$W404/enqueue")
get wv "$WA/api/practice/attempts/$W404/queue" -b "$WORK/jarW"
expect "W11c old brain (enqueue 404): 200 + archived, item not_queued/not_ready, ONE call only (no retry), shown as 未排队, not 'retrying'" "$(jq -c '[.archived,.queue.items[0].enq,.queue.items[0].reason]' "$WORK/resp/w5.b") $(( $(wq enqueue_calls) - E0 )) $(jq -c '[.items[0].ui_state,(.items[0].label|test("未排队")),.summary.enqueue_retry]' "$WORK/resp/wv.b")" '[true,"not_queued","not_ready"] 1 ["not_queued",true,0]'
curl -s -X POST "http://127.0.0.1:$WQ/_mock/queue/config?enqueue_mode=" >/dev/null
expect "W11d no request of this run ever carried a changed session_total (mock answers 400 for that)" "$(wq enqueue_total_mismatch)" 0
# hygiene
grep -qiE "$CODE|$TOKEN|$GTOKEN|$NTOKEN|$TOPIC" "$WORK/W.log" "$WORK/W2.log" && bad "W12 secrets in the site log" || ok "W12 no code/token/topic in the site log"
grep -rqiE "$GTOKEN|$NTOKEN|$TOKEN|$TOPIC" "$WORK/q-W" "$WORK/att-W" && bad "W12b secrets in the queue state / archive" || ok "W12b no token/topic in the queue state files or the archive"
# switch off = old behaviour
start_site WO 18302 "http://127.0.0.1:$WQ" "RUYI_SETS_FILE=$WORK/sets.json" "RUYI_BRAIN_GRADE_TOKEN=$GTOKEN" "RUYI_NTFY_BASE_URL=$NTFYB" "RUYI_ATTEMPTS_DIR=$WORK/att-WO" "RUYI_GRADE_MAX_PER_HOUR=200" "RUYI_QUEUE_ENABLED=0"; wait_http "http://127.0.0.1:18302/healthz"
post_login wo "http://127.0.0.1:18302" "$CODE" -c "$WORK/jarWO"; E0=$(wq enqueue_calls)
gpost wo "http://127.0.0.1:18302" demo-1 "$(mkbody 77777777-7777-4777-8777-777777777777)" -b "$WORK/jarWO"
get wov "http://127.0.0.1:18302/api/practice/attempts/77777777-7777-4777-8777-777777777777/queue" -b "$WORK/jarWO"
expect "W13 RUYI_QUEUE_ENABLED=0: grade has the #22 keys (no queue), the status route is not served, the brain queue gets nothing" "$(jq -c 'has("queue")' "$WORK/resp/wo.b") $CODE_HTTP $(( $(wq enqueue_calls) - E0 ))" "false 400 0"
kill "$PID_WO" 2>/dev/null
# rollback with only the master switch off: the other queue variables stay in the env file and are ignored
start_site WO2 18305 "http://127.0.0.1:$WQ" "RUYI_SETS_FILE=$WORK/sets.json" "RUYI_BRAIN_GRADE_TOKEN=$GTOKEN" "RUYI_NTFY_BASE_URL=$NTFYB" "RUYI_ATTEMPTS_DIR=$WORK/att-WO2" "RUYI_QUEUE_ENABLED=0" "RUYI_QUEUE_NOTIFY_ENABLED=1" "RUYI_BRAIN_NOTIFY_TOKEN=$NTOKEN" "RUYI_QUEUE_POLL_SECONDS=fast"
wait_http "http://127.0.0.1:18305/healthz" && ok "W13b RUYI_QUEUE_ENABLED=0 + RUYI_QUEUE_NOTIFY_ENABLED=1 (+ a bad poll value) starts" || bad "W13b start with the queue off and notify on" "$(head -c 300 "$WORK/WO2.log")"
grep -q 'configuration warning' "$WORK/WO2.log" && ! grep -qF "$NTOKEN" "$WORK/WO2.log" && ok "W13c ... the start log has a warning, no token" || bad "W13c warning in the log"
kill "$PID_WO2" 2>/dev/null
# startup validation: three identical tokens refuse to start; values never printed
OUT=$(env -i PATH="$PATH" RUYI_LISTEN=127.0.0.1:18303 RUYI_STATIC_DIR="$STATIC" RUYI_FAMILY_CODE="$CODE" RUYI_SESSION_SECRET="$SECRET" RUYI_BRAIN_BASE_URL="http://127.0.0.1:$WQ" RUYI_BRAIN_TOKEN="$TOKEN" RUYI_BRAIN_GRADE_TOKEN="$TOKEN" RUYI_BRAIN_NOTIFY_TOKEN="$TOKEN" RUYI_QUEUE_ENABLED=1 RUYI_NTFY_TOPIC="$TOPIC" timeout 5 "$BIN" 2>&1); RC=$?
[ "$RC" != 0 ] && [ "$RC" != 124 ] && echo "$OUT" | grep -q 'RUYI_BRAIN_NOTIFY_TOKEN' && ! echo "$OUT" | grep -qF "$TOKEN" && ok "W14 three identical tokens -> refuses to start (exit $RC), names the variable, prints no value" || bad "W14 identical tokens" "rc=$RC"
OUT=$(env -i PATH="$PATH" RUYI_LISTEN=127.0.0.1:18304 RUYI_STATIC_DIR="$STATIC" RUYI_FAMILY_CODE="$CODE" RUYI_SESSION_SECRET="$SECRET" RUYI_BRAIN_BASE_URL="http://127.0.0.1:$WQ" RUYI_BRAIN_TOKEN="$TOKEN" RUYI_BRAIN_GRADE_TOKEN="$GTOKEN" RUYI_QUEUE_ENABLED=1 RUYI_QUEUE_STATE_DIR="/proc/nope/queue" timeout 5 "$BIN" 2>&1); RC=$?
[ "$RC" != 0 ] && [ "$RC" != 124 ] && ok "W15 unusable queue state dir -> refuses to start (exit $RC)" || bad "W15 bad state dir" "rc=$RC"
kill "$PID_W2" "$PID_mockWQ" 2>/dev/null

if [ -n "$SHOTS" ] && command -v node >/dev/null && command -v google-chrome >/dev/null; then
  section "L. Browser (headless Chrome, mobile viewport) — criteria 1, 5, 9, submit flow"
  start_site BR 18199 "http://127.0.0.1:$PORT_MOCK"; wait_http "http://127.0.0.1:18199/healthz"
  BROWSER_CODE="$CODE" node "$HERE/browser.js" "http://127.0.0.1:18199" "$SHOTS" > "$WORK/browser.out" 2>&1; BRC=$?
  cat "$WORK/browser.out" | tee -a "$RESULTS"
  [ "$BRC" = 0 ] && ok "L1 browser checks (all passed)" || bad "L1 browser checks"
  kill "$PID_BR" 2>/dev/null
  start_mock mockNM 18201 MOCK_NO_MEDIA=1; wait_http "http://127.0.0.1:18201/_mock/stats"
  start_site NM 18202 "http://127.0.0.1:18201"; wait_http "http://127.0.0.1:18202/healthz"
  post_login nm "http://127.0.0.1:18202" "$CODE" -c "$WORK/jarNM"; get nm "http://127.0.0.1:18202/api/sets/$SOURCE" -b "$WORK/jarNM"
  expect "M1 brain has no figures yet (today's real state): 60 questions, all with empty media" "$(jq -c '[(.items|length),([.items[]|select(.media|length==0)]|length)]' "$WORK/resp/nm.b")" '[60,60]'
  ALL_MISSING=1 BROWSER_CODE="$CODE" node "$HERE/browser.js" "http://127.0.0.1:18202" "$SHOTS" > "$WORK/browser3.out" 2>&1; BRC=$?
  cat "$WORK/browser3.out" | tee -a "$RESULTS"
  [ "$BRC" = 0 ] && ok "L3 whole page of placeholders when no figure is ingested (all passed)" || bad "L3 all-missing browser check"
  kill "$PID_NM" 2>/dev/null
  start_site BRD 18200 "http://127.0.0.1:1"; wait_http "http://127.0.0.1:18200/healthz"
  BRAIN_DOWN=1 BROWSER_CODE="$CODE" node "$HERE/browser.js" "http://127.0.0.1:18200" "$SHOTS" > "$WORK/browser2.out" 2>&1; BRC=$?
  cat "$WORK/browser2.out" | tee -a "$RESULTS"
  [ "$BRC" = 0 ] && ok "L2 brain-down page shows readable notice (all passed)" || bad "L2 brain-down browser checks"
  kill "$PID_BRD" 2>/dev/null
  nextday
  start_site PB 18211 "http://127.0.0.1:$PORT_MOCK" "RUYI_SETS_FILE=$WORK/sets.json" "RUYI_BRAIN_GRADE_TOKEN=$GTOKEN" "RUYI_NTFY_BASE_URL=$NTFYB" "RUYI_ATTEMPTS_DIR=$WORK/att-PB" "RUYI_GRADE_MAX_PER_HOUR=200"; wait_http "http://127.0.0.1:18211/healthz"
  BROWSER_CODE="$CODE" node "$HERE/browser-practice.js" "http://127.0.0.1:18211" "$SHOTS" > "$WORK/browser4.out" 2>&1; BRC=$?
  cat "$WORK/browser4.out" | tee -a "$RESULTS"
  [ "$BRC" = 0 ] && ok "L4 practice page + submit flow in headless Chrome, brain grades (all passed)" || bad "L4 practice browser checks"
  grep -qiE "$CODE|$TOKEN|$GTOKEN" "$WORK/PB.log" && bad "L5 secrets in the log of the browser-driven run" || ok "L5 no code/token in the log of the browser-driven practice run"
  expect "L5b the browser-driven attempts were archived (JSON Lines, 600)" "$(stat -c %a "$WORK/att-PB/attempts.jsonl")" 600
  kill "$PID_PB" 2>/dev/null
  start_site PBA 18212 "http://127.0.0.1:$PORT_MOCK" "RUYI_SETS_FILE=$WORK/sets.json" "RUYI_NTFY_BASE_URL=$NTFYB" "RUYI_ATTEMPTS_DIR=$WORK/att-PBA" "RUYI_GRADE_MAX_PER_HOUR=200"; wait_http "http://127.0.0.1:18212/healthz"
  BROWSER_FLOW=archive BROWSER_CODE="$CODE" node "$HERE/browser-practice.js" "http://127.0.0.1:18212" "$SHOTS" > "$WORK/browser5.out" 2>&1; BRC=$?
  cat "$WORK/browser5.out" | tee -a "$RESULTS"
  [ "$BRC" = 0 ] && ok "L6 degraded (archive-only) submit flow: the page never claims the brain recorded it (all passed)" || bad "L6 archive-only browser checks"
  kill "$PID_PBA" 2>/dev/null
  start_mock mockRC 18250 "MOCK_GRADE_TOKEN=$GTOKEN"; wait_http "http://127.0.0.1:18250/_mock/stats"
  start_site PC 18251 "http://127.0.0.1:18250" "RUYI_SETS_FILE=$WORK/sets.json" "RUYI_BRAIN_GRADE_TOKEN=$GTOKEN" "RUYI_NTFY_BASE_URL=$NTFYB" "RUYI_ATTEMPTS_DIR=$WORK/att-PC" "RUYI_GRADE_MAX_PER_HOUR=200" "RUYI_AUTO_SOURCES=$PSRC" "RUYI_AUTO_TTL=60s"; wait_http "http://127.0.0.1:18251/healthz"
  MOCK_URL="http://127.0.0.1:18250" BROWSER_FLOW=contract BROWSER_CODE="$CODE" node "$HERE/browser-practice.js" "http://127.0.0.1:18251" "$SHOTS" > "$WORK/browser6.out" 2>&1; BRC=$?
  cat "$WORK/browser6.out" | tee -a "$RESULTS"
  [ "$BRC" = 0 ] && ok "L7 final-contract page states in headless Chrome: rev refresh, new-id retry, 429, recorded:false, picked-set tags (all passed)" || bad "L7 contract browser checks"
  grep -qiE "$CODE|$TOKEN|$GTOKEN" "$WORK/PC.log" && bad "L7b secrets in the log of the contract browser run" || ok "L7b no code/token in the log of the contract browser run"
  kill "$PID_PC" 2>/dev/null
  start_site PN 18252 "http://127.0.0.1:18221" "RUYI_SETS_FILE=$WORK/sets.json" "RUYI_BRAIN_GRADE_TOKEN=$GTOKEN" "RUYI_NTFY_BASE_URL=$NTFYB" "RUYI_ATTEMPTS_DIR=$WORK/att-PN" "RUYI_GRADE_MAX_PER_HOUR=200"; wait_http "http://127.0.0.1:18252/healthz"
  BROWSER_FLOW=notready BROWSER_CODE="$CODE" node "$HERE/browser-practice.js" "http://127.0.0.1:18252" "$SHOTS" > "$WORK/browser7.out" 2>&1; BRC=$?
  cat "$WORK/browser7.out" | tee -a "$RESULTS"
  [ "$BRC" = 0 ] && ok "L8 grade endpoint not deployed: the page says 暂未判分 / 大脑接口还没准备好, never 已提交到大脑 (all passed)" || bad "L8 not-ready browser checks"
  kill "$PID_PN" 2>/dev/null
  start_mock mockSM 18262 "MOCK_GRADE_TOKEN=$GTOKEN"; wait_http "http://127.0.0.1:18262/_mock/stats"
  start_site SM 18263 "http://127.0.0.1:18262" "RUYI_SETS_FILE=$WORK/sets-t.json" "RUYI_BRAIN_GRADE_TOKEN=$GTOKEN" "RUYI_NTFY_BASE_URL=$NTFYB" "RUYI_ATTEMPTS_DIR=$WORK/att-SM" "RUYI_GRADE_MAX_PER_HOUR=200"; wait_http "http://127.0.0.1:18263/healthz"
  sleep 0.6; SM0=$(curl -s "http://127.0.0.1:18262/_mock/stats" | jq -r '[.[]|numbers]|add // 0'); NS0=$(ntfy_n)
  BROWSER_FLOW=smoke BROWSER_CODE="$CODE" node "$HERE/browser-practice.js" "http://127.0.0.1:18263" "$SHOTS" > "$WORK/browser8.out" 2>&1; BRC=$?
  cat "$WORK/browser8.out" | tee -a "$RESULTS"
  [ "$BRC" = 0 ] && ok "L9 local smoke set in headless Chrome: answer, 检查, submit -> archive-only wording, never 已提交到大脑 (all passed)" || bad "L9 smoke browser checks"
  expect "L9b the brain got 0 requests during the whole browser run (grade token configured)" "$(( $(curl -s "http://127.0.0.1:18262/_mock/stats" | jq -r '[.[]|numbers]|add // 0') - SM0 ))" 0
  expect "L9c the browser run was archived with test:true and pushed once, with 【测试】" "$(tail -1 "$WORK/att-SM/attempts.jsonl" | jq -c '.test') $(( $(ntfy_n) - NS0 )) $(tail -1 "$WORK/ntfy.jsonl" | jq -r '.body' | head -c 12)" 'true 1 【测试】'
  kill "$PID_SM" "$PID_mockSM" 2>/dev/null
  # --- daily limit UX: N left today, confirm, 3 ok then 429 daily_cap archived, wording
  start_mock mockDL 18296 "MOCK_GRADE_TOKEN=$GTOKEN"; wait_http "http://127.0.0.1:18296/_mock/stats"
  start_site DL 18297 "http://127.0.0.1:18296" "RUYI_SETS_FILE=$WORK/sets.json" "RUYI_BRAIN_GRADE_TOKEN=$GTOKEN" "RUYI_NTFY_BASE_URL=$NTFYB" "RUYI_ATTEMPTS_DIR=$WORK/att-DL" "RUYI_GRADE_MAX_PER_HOUR=200"; wait_http "http://127.0.0.1:18297/healthz"
  ND0=$(ntfy_n)
  BROWSER_FLOW=dailylimit BROWSER_CODE="$CODE" node "$HERE/browser-practice.js" "http://127.0.0.1:18297" "$SHOTS" > "$WORK/browser9.out" 2>&1; BRC=$?
  cat "$WORK/browser9.out" | tee -a "$RESULTS"
  [ "$BRC" = 0 ] && ok "L10 daily-limit UX in headless Chrome: N left, confirm (cancel keeps the draft), 3 ok + 4th 429 daily_cap, rate_limited wording, 不入大脑 badge (all passed)" || bad "L10 daily-limit browser checks"
  sleep 1.2
  expect "L10b the browser run archived 3 graded + 2 refused (daily_cap) + 1 unsupported(graded mode) attempts, in that order" "$(jq -r '[.mode,.brain_note]|join("/")' "$WORK/att-DL/attempts.jsonl" | paste -sd' ')" 'graded/ graded/ graded/ archive_only/daily_cap archive_only/daily_cap graded/'
  expect "L10c refused attempts recorded nothing in the brain (recorded:false on every item) and only the 4 non-refused ones were announced (3 + 1)" "$(jq -c 'select(.brain_note=="daily_cap")|[.items[].recorded]|any' "$WORK/att-DL/attempts.jsonl" | sort -u | paste -sd' ') $(( $(ntfy_n) - ND0 ))" 'false 4'
  grep -qiE "$CODE|$TOKEN|$GTOKEN" "$WORK/DL.log" && bad "L10d secrets in the log of the daily-limit browser run" || ok "L10d no code/token in the log of the daily-limit browser run"
  kill "$PID_DL" "$PID_mockDL" 2>/dev/null
  # --- S1 queue: AI first-pass status under queued answers
  start_mock mockQB 18310 "MOCK_GRADE_TOKEN=$GTOKEN" MOCK_QUEUE=1 "MOCK_NOTIFY_TOKEN=$NTOKEN"; wait_http "http://127.0.0.1:18310/_mock/stats"
  start_site QB 18311 "http://127.0.0.1:18310" "RUYI_SETS_FILE=$WORK/sets.json" "RUYI_BRAIN_GRADE_TOKEN=$GTOKEN" "RUYI_NTFY_BASE_URL=$NTFYB" "RUYI_ATTEMPTS_DIR=$WORK/att-QB" "RUYI_GRADE_MAX_PER_HOUR=200" "RUYI_QUEUE_ENABLED=1" "RUYI_QUEUE_STATE_DIR=$WORK/q-QB"; wait_http "http://127.0.0.1:18311/healthz"
  MOCK_URL="http://127.0.0.1:18310" BROWSER_FLOW=queue BROWSER_CODE="$CODE" node "$HERE/browser-practice.js" "http://127.0.0.1:18311" "$SHOTS" > "$WORK/browser10.out" 2>&1; BRC=$?
  cat "$WORK/browser10.out" | tee -a "$RESULTS"
  [ "$BRC" = 0 ] && ok "L11 queue status in headless Chrome: 待解答 → AI 判卷中 → AI初评·待家长核 (full escaped text), timeout copy, purged, polling schedule, hidden tab, reload (all passed)" || bad "L11 queue browser checks"
  grep -qiE "$CODE|$TOKEN|$GTOKEN|$NTOKEN|$TOPIC" "$WORK/QB.log" && bad "L11b secrets in the log of the queue browser run" || ok "L11b no code/token in the log of the queue browser run"
  kill "$PID_QB" "$PID_mockQB" 2>/dev/null
fi

kill "$PID_P" 2>/dev/null
say ""; say "TOTAL: $PASS passed, $FAIL failed"
[ "$FAIL" = 0 ]
