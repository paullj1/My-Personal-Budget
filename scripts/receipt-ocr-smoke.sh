#!/usr/bin/env bash
# Regression harness for receipt extraction.
#
#   ./scripts/receipt-ocr-smoke.sh receipt.jpg              # through the API (default)
#   MODE=model ./scripts/receipt-ocr-smoke.sh receipt.jpg    # straight to Ollama
#
# API mode exercises what actually ships: upload -> document detection, crop and
# deskew -> extraction -> tax allocation -> reconciliation. Prefer it. Point it at
# a running API with API_URL and API_TOKEN.
#
# Model mode bypasses the server to debug the model itself. It therefore skips the
# crop, which is the step that makes extraction reliable, so expect worse results
# than the app produces -- that gap is the point of having both modes.
#
# Env: API_URL (default http://localhost:8080), API_TOKEN,
#      OLLAMA_URL (default http://localhost:11434), OCR_MODEL (default qwen3.8:27b),
#      NUM_CTX (default 32768), MODE (api|model).

set -euo pipefail

IMAGE="${1:?usage: $0 <receipt-image>}"
MODE="${MODE:-api}"
API_URL="${API_URL:-http://localhost:8080}"
API_TOKEN="${API_TOKEN:-}"
OLLAMA_URL="${OLLAMA_URL:-http://localhost:11434}"
OCR_MODEL="${OCR_MODEL:-qwen3.8:27b}"
NUM_CTX="${NUM_CTX:-32768}"

command -v jq >/dev/null || { echo "need jq" >&2; exit 1; }
[ -f "$IMAGE" ] || { echo "no such file: $IMAGE" >&2; exit 1; }

TMP="$(mktemp -d -t receipt-XXXXXX)"; trap 'rm -rf "$TMP"' EXIT

# The two independent checks that gate a commit, recomputed here from the response
# so the harness verifies the invariant rather than trusting the server's flag.
reconcile_report() {
  jq -r 'def c:(.//0);
    ([.items[]? | (.amount_cents + .tax_cents + .adjust_cents)] | add // 0) as $lines |
    ([.items[]?.amount_cents] | add // 0)                                   as $net |
    "  items=\($net)c  +tax=\(.tax_cents|c)c  lines=\($lines)c  printed_total=\(.total_cents|c)c",
    "  items found: \(.items|length)",
    (if $lines == (.total_cents|c) then "  OK - line totals equal the printed total"
     else "  MISMATCH - \($lines - (.total_cents|c))c apart; would fall back to manual entry" end),
    "  server reconciliation: ok=\(.reconciliation.ok)\(if .reconciliation.message then " (" + .reconciliation.message + ")" else "" end)"
  ' "$1"
}

if [ "$MODE" = "api" ]; then
  [ -n "$API_TOKEN" ] || echo "!! API_TOKEN is empty; expect 401 unless auth is disabled" >&2
  echo "==> POST $API_URL/api/v1/receipts/scan  ($(wc -c <"$IMAGE" | tr -d ' ') bytes)"
  START="$(date +%s)"
  CODE="$(curl -sS --max-time 600 -o "$TMP/resp.json" -w '%{http_code}' \
    -X POST "$API_URL/api/v1/receipts/scan" \
    ${API_TOKEN:+-H "Authorization: Bearer $API_TOKEN"} \
    -F "image=@$IMAGE")"
  echo "==> HTTP $CODE in $(( $(date +%s) - START ))s"

  if [ "$CODE" != "200" ]; then
    echo "!! $(jq -r '.error // "no error field"' < "$TMP/resp.json" 2>/dev/null || cat "$TMP/resp.json")" >&2
    [ "$CODE" = "503" ] && echo "   RECEIPT_OCR_URL is probably unset on the server." >&2
    exit 1
  fi

  # Detection is the step that decides whether extraction has a chance.
  jq -r '"==> image: \(.image.src_width)x\(.image.src_height) -> \(.image.width)x\(.image.height)" +
         " cropped=\(.image.cropped) rotated=\(.image.rotated)"' < "$TMP/resp.json"
  jq -r 'if .image.detect.detected
         then "==> detected: area=\(.image.detect.area_fraction * 100 | floor)% aspect=\(.image.detect.aspect * 100 | floor / 100) fill=\(.image.detect.fill * 100 | floor)% angle=\(.image.detect.angle_degrees | floor)deg"
         else "!! no document detected (\(.image.detect.reason // "no reason given")); using the whole frame, expect worse extraction" end' < "$TMP/resp.json"
  jq -r '"==> model: \(.model) in \(.elapsed_ms)ms  tax_basis=\(.tax_basis) tax_evidence=\(.tax_evidence)"' < "$TMP/resp.json"

  echo "==> items:"
  jq -r '.items[] | "  \(.position). \(.description) net=\(.amount_cents)c tax=\(.tax_cents)c total=\(.total_cents)c marker=\(.marker // "-") suggested=\(.suggested_budget_id // "none")"' < "$TMP/resp.json"
  echo "==> reconciliation:"
  reconcile_report "$TMP/resp.json"
  exit 0
fi

# ---------------------------------------------------------------- model mode ---
# Uses Ollama's native /api/chat. Not the OpenAI-compatible /v1 shim: Ollama
# ignores response_format:json_schema there (ollama#10001) and returns
# unconstrained output that merely looks like JSON.
echo "!! model mode skips document detection, which is what makes extraction reliable." >&2
echo "   Results here are a floor, not what the app produces. Use MODE=api to test the app." >&2

SEND="$IMAGE"
if command -v ffmpeg >/dev/null; then
  # Only apply EXIF and cap the long edge -- no rotation. The server decides
  # orientation from the detected outline; guessing differently here would make
  # this harness test a pipeline that does not ship.
  ffmpeg -v error -y -i "$IMAGE" \
    -vf "scale=w=2048:h=2048:force_original_aspect_ratio=decrease:flags=lanczos,scale=trunc(iw/2)*2:trunc(ih/2)*2" \
    -q:v 3 "$TMP/norm.jpg" && SEND="$TMP/norm.jpg"
  echo "==> capped long edge to 2048: $(wc -c <"$SEND" | tr -d ' ') bytes"
fi

# Staged through a file: a phone photo base64s past ARG_MAX as a jq --arg.
base64 -w0 "$SEND" > "$TMP/b64"

PROMPT='Extract this receipt into JSON.

RULES
1. Copy text and numbers exactly. Perform NO arithmetic.
2. Every purchased item goes in items, in printed order. Do not skip or duplicate any item.
3. Sub-lines such as "Regular Price $39.99" or "Buy1Get1 50%off" are INFORMATIONAL when the
   item amount is already the net price. They are NOT adjustments. Do not emit them.
4. A savings SUMMARY line such as "YOUR TOTAL SAVINGS THIS TRIP: $20.00" is NOT an adjustment.
5. Record each taxability marker verbatim in marker (T, TF, N, F) and set taxable.
6. If a tax line prints its own base, as in "6.00000 on $70.66", put 70.66 in base and 0.06 in rate.
7. SUBTOTAL, TAX, TOTAL, payment, savings, auth and survey lines are NOT items.
8. Department headers such as GROCERY or KITCHEN label the items beneath them and are NOT items.
9. Use null when something is not visible. A missing date is null, not today.'

read -r -d '' SCHEMA <<'JSONEOF' || true
{"type":"object","properties":{
 "merchant":{"type":["string","null"]},"purchased_at":{"type":["string","null"]},
 "items":{"type":"array","items":{"type":"object","properties":{
   "position":{"type":"integer"},"line_text":{"type":"string"},"description":{"type":"string"},
   "amount":{"type":"number"},"taxable":{"type":["boolean","null"]},"marker":{"type":["string","null"]}},
   "required":["position","line_text","description","amount","taxable"]}},
 "adjustments":{"type":"array","items":{"type":"object","properties":{
   "label":{"type":"string"},"amount":{"type":"number"}},"required":["label","amount"]}},
 "tax_lines":{"type":"array","items":{"type":"object","properties":{
   "label":{"type":"string"},"rate":{"type":["number","null"]},
   "base":{"type":["number","null"]},"amount":{"type":"number"}},"required":["label","amount"]}},
 "subtotal":{"type":["number","null"]},"total":{"type":["number","null"]},
 "tax_evidence":{"type":"string","enum":["per_line_flags","single_rate","multi_rate","unknown"]}},
 "required":["merchant","items","tax_lines","subtotal","total","tax_evidence"]}
JSONEOF

jq -n --arg m "$OCR_MODEL" --arg p "$PROMPT" --rawfile img "$TMP/b64" \
      --argjson s "$SCHEMA" --argjson n "$NUM_CTX" '{
  model:$m, messages:[{role:"user",content:$p,images:[($img|rtrimstr("\n"))]}],
  think:false, stream:false, format:$s, options:{temperature:0, num_ctx:$n}
}' > "$TMP/req.json"

echo "==> $OCR_MODEL via $OLLAMA_URL/api/chat  num_ctx=$NUM_CTX"
START="$(date +%s)"
curl -sS --max-time 900 "$OLLAMA_URL/api/chat" -H 'Content-Type: application/json' \
  --data-binary "@$TMP/req.json" > "$TMP/resp.json"
echo "==> $(( $(date +%s) - START ))s"

if jq -e '.error' >/dev/null 2>&1 < "$TMP/resp.json"; then
  echo "!! ollama error: $(jq -r .error < "$TMP/resp.json")" >&2
  echo "   'think' rejected? try \"think\":\"low\". Missing model? ollama pull $OCR_MODEL" >&2
  exit 1
fi

THINK="$(jq -r '(.message.thinking // "") | length' < "$TMP/resp.json")"
echo "==> prompt_eval=$(jq -r '.prompt_eval_count // "?"' < "$TMP/resp.json") eval=$(jq -r '.eval_count // "?"' < "$TMP/resp.json") thinking_chars=$THINK"
[ "$THINK" -gt 0 ] && echo "!! emitted thinking despite think:false -- pure latency; a model swap must re-verify this"

jq -r '.message.content' < "$TMP/resp.json" > "$TMP/out.json"
jq -e . >/dev/null 2>&1 < "$TMP/out.json" || {
  echo "!! not valid JSON -- grammar not applied. Are you on /api/chat, not /v1?" >&2
  cat "$TMP/out.json"; exit 1; }

echo "==> extracted:"; jq . < "$TMP/out.json"
echo "==> reconciliation (cents, computed here since there is no server to do it):"
jq -r 'def c:(.//0)*100|round;
 (reduce .items[].amount as $a (0;.+($a|c)))            as $i |
 (reduce .tax_lines[].amount as $a (0;.+($a|c)))        as $t |
 (reduce (.adjustments//[])[].amount as $a (0;.+($a|c))) as $j |
 (.subtotal|c) as $s | (.total|c) as $o |
 (if $s == 0 then $i else $s end) as $eff |
 "  items=\($i) subtotal=\($s) delta=\($i - (if $s == 0 then $i else $s end))",
 "  items+tax+adj=\($eff+$t+$j) total=\($o) delta=\($eff+$t+$j-$o)",
 "  items found: \(.items|length)",
 (if ($s != 0 and $i != $s) or ($o != 0 and ($eff+$t+$j) != $o)
  then "  MISMATCH - would fall back to pre-filled manual entry"
  else "  OK - reconciles exactly" end),
 (if (.tax_lines[0].base // null) != null then
    "  tax base printed=\(.tax_lines[0].base) -> taxable set = " +
    (if (.tax_lines[0].base|c) == $i then "ALL items" else "marker subset" end)
  else "  no printed tax base -> markers or proration" end)
' < "$TMP/out.json"
