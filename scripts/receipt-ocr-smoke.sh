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
#      MODEL_URL (default http://localhost:11435 -- llama-server),
#      OCR_MODEL (default qwen3.8-27b), MODE (api|model).

set -euo pipefail

IMAGE="${1:?usage: $0 <receipt-image>}"
MODE="${MODE:-api}"
API_URL="${API_URL:-http://localhost:8080}"
API_TOKEN="${API_TOKEN:-}"
MODEL_URL="${MODEL_URL:-http://localhost:11435}"
OCR_MODEL="${OCR_MODEL:-qwen3.8-27b}"

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
         then "==> detected: area=\((.image.detect.area_fraction // 0) * 100 | floor)% aspect=\((.image.detect.aspect // 0) * 100 | floor / 100) fill=\((.image.detect.fill // 0) * 100 | floor)% angle=\((.image.detect.angle_degrees // 0) | floor)deg"
         else "!! no document detected (\(.image.detect.reason // "no reason given")); using the whole frame, expect worse extraction" end' < "$TMP/resp.json"
  jq -r '"==> model: \(.model) in \(.elapsed_ms)ms  tax_basis=\(.tax_basis) tax_evidence=\(.tax_evidence)"' < "$TMP/resp.json"

  echo "==> items:"
  jq -r '.items[] | "  \(.position). \(.description) net=\(.amount_cents)c tax=\(.tax_cents)c total=\(.total_cents)c marker=\(.marker // "-") suggested=\(.suggested_budget_id // "none")"' < "$TMP/resp.json"
  echo "==> reconciliation:"
  reconcile_report "$TMP/resp.json"
  exit 0
fi

# ---------------------------------------------------------------- model mode ---
# Talks to llama-server directly. Two settings are not optional:
#   cache_prompt:false -- a stale prompt cache is how the previous backend came to
#     answer with a completely different receipt.
#   enable_thinking:false -- extraction is perception, not deliberation, so every
#     reasoning token is latency spent for nothing.
echo "!! model mode skips document detection, which the server does. Results here are a" >&2
echo "   floor, not what the app produces. Use MODE=api to test the app." >&2

SEND="$IMAGE"
if command -v ffmpeg >/dev/null; then
  ffmpeg -v error -y -i "$IMAGE" \
    -vf "scale=w=2048:h=2048:force_original_aspect_ratio=decrease:flags=lanczos,scale=trunc(iw/2)*2:trunc(ih/2)*2" \
    -q:v 3 "$TMP/norm.jpg" && SEND="$TMP/norm.jpg"
  echo "==> capped long edge to 2048: $(wc -c <"$SEND" | tr -d ' ') bytes"
fi

base64 -w0 "$SEND" > "$TMP/b64"

PROMPT=$(cat <<'PEOF'
Extract this receipt into JSON. The image may be rotated.
Copy numbers exactly and perform NO arithmetic. Every purchased item goes in items, in
printed order; repeated items are normal. A number left of the description is a quantity,
not a price. Sub-lines with no price, or [0.00], are modifiers. Tip and savings guides are
not adjustments. SUBTOTAL, TAX, TOTAL, payment and department headers are not items.
PEOF
)

jq -n --arg m "$OCR_MODEL" --arg p "$PROMPT" --rawfile img "$TMP/b64" '{
  model: $m,
  messages: [{role:"user", content:[
    {type:"text", text:$p},
    {type:"image_url", image_url:{url:("data:image/jpeg;base64," + ($img|rtrimstr("\n")))}}]}],
  temperature: 0,
  cache_prompt: false,
  chat_template_kwargs: {enable_thinking: false}
}' > "$TMP/req.json"

echo "==> $OCR_MODEL via $MODEL_URL/v1/chat/completions"
START="$(date +%s)"
curl -sS --max-time 900 "$MODEL_URL/v1/chat/completions" -H 'Content-Type: application/json' \
  --data-binary "@$TMP/req.json" > "$TMP/resp.json"
echo "==> $(( $(date +%s) - START ))s"

python3 - "$TMP/resp.json" <<'PYEOF2'
import json, sys
d = json.load(open(sys.argv[1]))
if d.get("error"):
    print("!! server error:", d["error"].get("message", d["error"]), file=sys.stderr); raise SystemExit(1)
if not d.get("choices"):
    print("!! no choices in response", file=sys.stderr); raise SystemExit(1)
msg = d["choices"][0]["message"]
if msg.get("reasoning_content") and not (msg.get("content") or "").strip():
    print("!! model emitted only reasoning -- thinking is not suppressed", file=sys.stderr); raise SystemExit(1)
content = (msg.get("content") or "").strip()
try:
    r = json.loads(content)
except Exception:
    print("==> not JSON (model mode sends no schema, so this is expected):")
    print(content[:1200]); raise SystemExit(0)
cents = lambda v: int(round((v or 0) * 100))
items = sum(cents(i.get("amount")) for i in r.get("items", []))
tax   = sum(cents(t.get("amount")) for t in r.get("tax_lines", []))
adj   = sum(cents(a.get("amount")) for a in (r.get("adjustments") or []))
sub, tot = cents(r.get("subtotal")), cents(r.get("total"))
eff = sub or items
print(f"==> items={len(r.get('items', []))} itemsum={items}c tax={tax}c subtotal={sub}c total={tot}c")
ok = (sub == 0 or items == sub) and (tot == 0 or eff + tax + adj == tot)
print("    OK - reconciles exactly" if ok else "    MISMATCH - would fall back to manual entry")
PYEOF2
