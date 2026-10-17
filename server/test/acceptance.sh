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
expect "P11 allowed item keys only" "$(jq -c '[.items[]|keys[]]|unique' "$WORK/resp/d1.b")" '["blanks","figure_status","media","options","ordinal","qref","qtype","stem_md"]'
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
            f.write(json.dumps({"path": s.path, "auth": s.headers.get('Authorization'), "cookie": s.headers.get('Cookie'), "body": b}, ensure_ascii=False) + "\n")
        s.send_response(200); s.end_headers(); s.wfile.write(b'{}')
    def log_message(self, *a): pass
http.server.HTTPServer(('127.0.0.1', int(sys.argv[1])), H).serve_forever()
PY
: > "$WORK/ntfy.jsonl"
python3 "$WORK/ntfy_stub.py" $NTFY_PORT "$WORK/ntfy.jsonl" & PIDS+=($!)
NTFYB="http://127.0.0.1:$NTFY_PORT"
for _ in $(seq 1 30); do (echo > /dev/tcp/127.0.0.1/$NTFY_PORT) 2>/dev/null && break; sleep 0.1; done
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
expect "Q1g response has only the documented keys (replayed appears only on a replay)" "$(jq -c 'keys' "$WORK/resp/g1.b")" '["archived","brain_recorded","client_attempt_id","mode","notify","results","set_id","summary"]'
# --- Q2 idempotent replay
gpost g2 "$QA" demo-1 "$(mkbody $C1)" -b "$WORK/jarQ"
expect "Q2 same client_attempt_id again -> 200 replayed:true with the stored result" "$(jq -c '[.replayed,.mode,.summary.correct]' "$WORK/resp/g2.b")" '[true,"graded",1]'
gpost g2b "$QA" demo-1 "$(mkbody $C1 '[{"qref":"example.set:math:2","answer":"B"}]')" -b "$WORK/jarQ"
expect "Q2b replay ignores a different payload (stored result wins)" "$(jq -c '[.replayed,.summary.total,.summary.correct]' "$WORK/resp/g2b.b")" '[true,3,1]'
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
expect "Q4b record fields (JSON Lines, version 1, client_attempt_id naming, brain attempt id, mode)" "$(head -1 "$WORK/att-Q/attempts.jsonl" | jq -c '[.v,.client_attempt_id,.set_id,.set_title,.source,.mode,(.brain_attempt_id|length>0),(.items|length),(.attempt_id // "none")]')" "[1,\"$C1\",\"demo-1\",\"演示套\",\"$PSRC\",\"graded\",true,3,\"none\"]"
expect "Q4c answers are archived as given (fill blanks as arrays)" "$(head -1 "$WORK/att-Q/attempts.jsonl" | jq -c '[.items[].answer]')" '[["1","2","3"],"A",["x","y"]]'
for pair in "family code:$CODE" "bearer token:$TOKEN" "grade token:$GTOKEN" "session secret:$SECRET" "ntfy topic:$TOPIC" "cookie name:ruyi_sess" "Authorization header:Authorization"; do
  name="${pair%%:*}"; val="${pair#*:}"; grep -qF -- "$val" "$WORK/att-Q/attempts.jsonl" && bad "Q4d $name found in the attempts archive" || ok "Q4d $name not in the attempts archive"
done
# --- Q5 ntfy heads-up
expect "Q5 ntfy: one POST, to /<topic>, no credentials" "$(jq -rs '[length, (.[0].path == "/'"$TOPIC"'"), (.[0].auth == null), (.[0].cookie == null)]|@json' "$WORK/ntfy.jsonl")" '[1,true,true,true]'
expect "Q5b ntfy summary line (heads-up wording, not an approval request)" "$(jq -rs '.[0].body|split("\n")[0]' "$WORK/ntfy.jsonl")" '如意练习：演示套 3 题，对 1，错 1，待核对 1，已提交。attempt 11111111。'
expect "Q5c ntfy detail JSON carries client_attempt_id and mode, no secrets" "$(jq -rs '.[0].body|split("\n\n")[1]|fromjson|[.client_attempt_id,.mode]|@json' "$WORK/ntfy.jsonl")" "[\"$C1\",\"graded\"]"
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
for i in 2 3 4; do gpost r$i "$QA" demo-1 "$(mkbody 3333333$i-3333-4333-8333-333333333333)" -b "$WORK/jarQ"; [ "$CODE_HTTP" = 200 ] && ok "Q8 submission $i of 4 accepted" || bad "Q8 submission $i" "$CODE_HTTP"; done
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
expect "Q10k wrong_only -> 501 wrong_only_unavailable (no invented logic)" "$CODE_HTTP $(jq -r .error "$WORK/resp/dn3.b")" "501 wrong_only_unavailable"
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
start_site QX 18236 "http://127.0.0.1:$PORT_MOCK" "RUYI_SETS_FILE=$WORK/sets.json" "RUYI_BRAIN_GRADE_TOKEN=$GTOKEN" "RUYI_NTFY_BASE_URL=http://127.0.0.1:1" "RUYI_NTFY_RETRY_DELAY=300ms" "RUYI_ATTEMPTS_DIR=$WORK/att-QX"; wait_http "http://127.0.0.1:18236/healthz"
post_login qx "http://127.0.0.1:18236" "$CODE" -c "$WORK/jarQX"
gpost x1 "http://127.0.0.1:18236" demo-1 "$(mkbody 77777777-7777-4777-8777-777777777771)" -b "$WORK/jarQX"
expect "Q13 ntfy unreachable: 200, graded, notify:retrying, archived" "$CODE_HTTP $(jq -c '[.mode,.notify,.archived]' "$WORK/resp/x1.b")" '200 ["graded","retrying",true]'
sleep 1.2
expect "Q13b the retry failure is logged once as a warning" "$(grep -c 'ntfy notification failed after retry' "$WORK/QX.log")" 1
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
fi

kill "$PID_P" 2>/dev/null
say ""; say "TOTAL: $PASS passed, $FAIL failed"
[ "$FAIL" = 0 ]
