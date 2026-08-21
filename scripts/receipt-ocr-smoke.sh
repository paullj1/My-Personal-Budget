#!/usr/bin/env bash
# Phase 0 / regression harness for receipt extraction.
#   ./scripts/receipt-ocr-smoke.sh receipt.jpg
# Env: OLLAMA_URL (default http://localhost:11434), OCR_MODEL (default qwen3.8:27b),
#      MAX_EDGE (default 1600), NUM_CTX (default 32768), NO_NORMALIZE=1 to skip resize.
#
# Implements the design validated in docs/receipt-scan-design.md §3.4:
#   * native /api/chat -- Ollama ignores OpenAI's response_format:json_schema (ollama#10001)
#   * LANDSCAPE orientation -- a portrait receipt silently loses ~half its items
#   * 1600px long edge -- 2048px and 4032px hit the same prompt_eval ceiling for +20% time
#   * think:false, temperature:0, explicit num_ctx (Ollama truncates silently at ~4096)

set -euo pipefail

IMAGE="${1:?usage: $0 <receipt-image>}"
OLLAMA_URL="${OLLAMA_URL:-http://localhost:11434}"
OCR_MODEL="${OCR_MODEL:-qwen3.8:27b}"
MAX_EDGE="${MAX_EDGE:-1600}"
NUM_CTX="${NUM_CTX:-32768}"

command -v jq >/dev/null || { echo "need jq" >&2; exit 1; }
[ -f "$IMAGE" ] || { echo "no such file: $IMAGE" >&2; exit 1; }

TMP="$(mktemp -d -t receipt-XXXXXX)"; trap 'rm -rf "$TMP"' EXIT
SEND="$IMAGE"

# Must reflect what the DECODER sees: this JPEG carries EXIF Orientation=6, so its
# raw SOF says 4032x3024 (landscape) while every decoder renders 3024x4032 (portrait).
# Reading SOF directly gets the orientation backwards.
# Must reflect what the DECODER sees, not the container metadata. This JPEG carries
# EXIF Orientation=6: raw SOF and ffprobe both say 4032x3024 (landscape), while every
# decoder renders 3024x4032 (portrait). Only an actual decode tells the truth, so
# round-trip one frame through ffmpeg and measure that.
dims() {
  ffmpeg -v error -y -i "$1" -frames:v 1 -f image2 "$TMP/probe.png" 2>/dev/null || { echo "0 0"; return; }
  python3 -c "
import struct,sys
d=open(sys.argv[1],'rb').read(33); w,h=struct.unpack('>II',d[16:24]); print(w,h)" "$TMP/probe.png"
}

read -r W H <<<"$(dims "$IMAGE")"
echo "==> input ${W}x${H} $([ "$W" -ge "$H" ] && echo landscape || echo PORTRAIT)"

if [ -z "${NO_NORMALIZE:-}" ] && command -v ffmpeg >/dev/null && [ "$W" -gt 0 ]; then
  VF=""
  # Rotate tall images to landscape. Extremely tall images lose their top half
  # entirely (design doc SS3.4); landscape is safe even at 2.4:1.
  if [ "$H" -gt "$W" ]; then VF="transpose=2,"; echo "==> tall input: rotating 90deg to landscape"; fi
  # force_original_aspect_ratio=decrease fits inside a MAX_EDGE box, i.e. bounds the
  # LONG edge in either orientation. Plain "scale=N:-2" pins WIDTH and silently
  # upscales a portrait image's long edge instead.
  VF="${VF}scale=w=${MAX_EDGE}:h=${MAX_EDGE}:force_original_aspect_ratio=decrease:flags=lanczos"
  VF="$VF,scale=trunc(iw/2)*2:trunc(ih/2)*2"
  ffmpeg -v error -y -i "$IMAGE" -vf "$VF" -q:v 3 "$TMP/norm.jpg" && SEND="$TMP/norm.jpg"
  read -r W2 H2 <<<"$(dims "$SEND")"
  echo "==> normalized ${W2}x${H2}, $(wc -c <"$SEND") bytes"
else
  echo "==> normalization skipped (set MAX_EDGE / install ffmpeg to enable)"
fi

# Staged through a file: a phone photo base64s past ARG_MAX as a jq --arg.
base64 -w0 "$SEND" > "$TMP/b64"

PROMPT='Extract this receipt into JSON. The image may be rotated.

RULES
1. Copy text and numbers exactly. Perform NO arithmetic.
2. Every purchased item goes in items, in printed order. Do not skip or duplicate any item.
3. Sub-lines like "Regular Price $39.99" or "Buy1Get1 50%off" are INFORMATIONAL when the
   item amount is already the net price. They are NOT adjustments. Do not emit them.
4. Record each taxability marker verbatim in marker (TF, T, N, F) and set taxable.
5. If a tax line prints its own base ("6.00000 on $70.66"), put 70.66 in base and the
   rate as a decimal in rate.
6. SUBTOTAL, TAX, TOTAL, payment, savings, auth and survey lines are NOT items.
7. A savings SUMMARY line such as "YOUR TOTAL SAVINGS THIS TRIP: $20.00" restates a
   discount already reflected in the item prices. It is NOT an adjustment. Omit it.
   Only emit an adjustment for a discount printed as its own deducted line.
8. Use null when something is not visible. A missing date is null, not today.'

read -r -d '' SCHEMA <<'JSONEOF' || true
{"type":"object","properties":{
 "merchant":{"type":["string","null"]},"purchased_at":{"type":["string","null"]},
 "currency":{"type":["string","null"]},
 "items":{"type":"array","items":{"type":"object","properties":{
   "position":{"type":"integer"},"line_text":{"type":"string"},"description":{"type":"string"},
   "quantity":{"type":["number","null"]},"unit_price":{"type":["number","null"]},
   "amount":{"type":"number"},"taxable":{"type":["boolean","null"]},
   "marker":{"type":["string","null"]}},
   "required":["position","line_text","description","amount","taxable"]}},
 "adjustments":{"type":"array","items":{"type":"object","properties":{
   "label":{"type":"string"},"amount":{"type":"number"},
   "applies_to_position":{"type":["integer","null"]}},"required":["label","amount"]}},
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
ELAPSED=$(( $(date +%s) - START ))

if jq -e '.error' >/dev/null 2>&1 < "$TMP/resp.json"; then
  echo "!! ollama error: $(jq -r .error < "$TMP/resp.json")" >&2
  echo "   'think' rejected? try \"think\":\"low\". Missing model? ollama pull $OCR_MODEL" >&2
  exit 1
fi

THINK="$(jq -r '(.message.thinking // "") | length' < "$TMP/resp.json")"
echo "==> ${ELAPSED}s  prompt_eval=$(jq -r '.prompt_eval_count // "?"' < "$TMP/resp.json") eval=$(jq -r '.eval_count // "?"' < "$TMP/resp.json") thinking_chars=$THINK"
[ "$THINK" -gt 0 ] && echo "!! emitted thinking despite think:false -- pure latency, see design SS12"

jq -r '.message.content' < "$TMP/resp.json" > "$TMP/out.json"
jq -e . >/dev/null 2>&1 < "$TMP/out.json" || {
  echo "!! not valid JSON -- grammar not applied. Are you on /api/chat, not /v1?" >&2
  cat "$TMP/out.json"; exit 1; }

echo "==> extracted:"; jq . < "$TMP/out.json"

# The free hard validator: two independent checks, in integer cents. Far more
# trustworthy than model self-confidence, which measured 1.0 on a 50%-wrong answer.
echo "==> reconciliation (cents):"
jq -r 'def c:(.//0)*100|round;
 (reduce .items[].amount as $a (0;.+($a|c)))            as $i |
 (reduce .tax_lines[].amount as $a (0;.+($a|c)))        as $t |
 (reduce (.adjustments//[])[].amount as $a (0;.+($a|c))) as $j |
 (.subtotal|c) as $s | (.total|c) as $o |
 "  items=\($i) subtotal=\($s) delta=\($i-$s)",
 "  subtotal+tax+adj=\($s+$t+$j) total=\($o) delta=\($s+$t+$j-$o)",
 "  items found: \(.items|length)",
 (if ($i-$s)==0 and ($s+$t+$j-$o)==0 then "  OK - reconciles exactly"
  else "  MISMATCH - would fall back to pre-filled manual entry" end),
 (if (.tax_lines[0].base // null) != null then
    "  tax base printed=\(.tax_lines[0].base) subtotal=\(.subtotal) -> taxable set = " +
    (if .tax_lines[0].base == .subtotal then "ALL items" else "marker subset" end)
  else "  no printed tax base -> fall back to markers / proration" end)
' < "$TMP/out.json"
