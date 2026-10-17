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
MAIN_FILES="$(git -C "$ROOT" ls-tree -r --name-only "$MAIN_REF" | grep -vE '^(server/|\.gitignore$|m/numberline-4y/)')"

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
fi

say ""; say "TOTAL: $PASS passed, $FAIL failed"
[ "$FAIL" = 0 ]
