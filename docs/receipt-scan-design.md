# Receipt Scan & Itemize — Design

Status: proposed — inference stages **validated on hardware** 2026-08-20
Date: 2026-08-20

Photograph a receipt, extract line items and tax, review them as a list where each
item has a budget dropdown, and commit as transactions. Integrated into the existing
"Itemize receipt" modal rather than shipped as a parallel flow.

## 1. Decisions

| # | Decision | Rationale |
|---|---|---|
| D1 | Go API proxies to the inference box; browser never talks to it directly | Reuses existing JWT auth, no CORS/TLS work, Strix box stays private, works when the phone is off-LAN |
| D2 | Qwen3.8-27B, vision only — no Tesseract/PaddleOCR | Natively multimodal; already running on the box. One service, one model |
| D2a1 | **Model is `qwen3.8:27b` — not a smaller VLM** | **Measured. It is the only local vision model whose thinking can be disabled on Ollama; `qwen3-vl:4b`/`:8b` are 2–3× faster per token but run away thinking and never emit output (§3.6)** |
| D2a0 | **Served by llama.cpp `llama-server`, not Ollama** | **The backend choice decided whether extraction worked at all, and fixed a data-integrity bug Ollama could not (§3.8)** |
| D2a | *(superseded)* Served by Ollama via its **native `/api/chat`**, not the OpenAI-compatible `/v1` shim | Ollama ignores `response_format: json_schema`; only the native `format` parameter actually constrains output |
| **D2b** | **Single grammar-constrained pass, image → JSON** | **Measured end-to-end on a real photo: 49s, all items, reconciles at 0¢. No tiling, no transcription stage, no merge** |
| **D2c** | **Deterministic document detection: crop, deskew, orient** | **The single biggest accuracy win. Otsu + largest component + min-area rect crops the receipt out of the frame. Turned a persistently wrong extraction into two-for-two exact runs, and cut latency (§3.6)** |
| **D2d** | **Bound the LONG edge to 2048px after cropping** | **The earlier 1600 figure came from a sweep that pinned *width*, so it was really 1600×2134. Post-crop the whole budget goes to print rather than background (§3.5)** |
| D3 | Model extracts facts; **Go server does all arithmetic** | LLMs are unreliable at cent-exact math, and reconciliation against the printed total is a free hard validator |
| D4 | Tax prorated server-side from evidence the model reports | Receipts print taxability markers and rates; use them when present, prorate when not |
| D5 | Store parsed JSON, discard the image | Audit trail + suggestion training data without blob storage or image-retention risk |
| D6 | One transaction per budget per receipt | A 40-item grocery run becomes 3 ledger rows, not 40; item detail lives in `receipt_items` |
| D7 | Budget suggestions from history, not the model | Deterministic, explainable, cheap, improves with use |
| D8 | Failure falls back to pre-filled manual entry | Never a dead end; the manual path already exists and works |
| D9 | Single modal, camera button in the header | Smallest diff, one code path, manual entry untouched and serves as the fallback |
| D10 | Synchronous request with spinner + cancel | No jobs table or polling; you're standing at the counter looking at the phone |
| D11 | Transactions backdated to the printed receipt date | Spending lands in the period it happened |
| D12 | Commit is one atomic DB transaction | Replaces today's non-atomic N-POST loop |
| D13 | Suggestion history scoped to budgets the user can access | Shared budgets get shared learning, which is the point of the share table |
| **D15** | **A remote MCP client may supply the extraction instead of the image** | **D3 already draws the line: the model reports facts, this server does the arithmetic. That makes the vision step swappable, so a client that can already see the photo sends the JSON rather than paying a base64 round trip to be re-read by a smaller local model. Reconciliation validates it identically (§13)** |
| D14 | Feature disabled when `RECEIPT_OCR_URL` is empty | The API reports `features.receipt_scan: false`, the scan endpoint answers 503, the server keeps its tight 10s timeouts, and the UI shows neither the button nor any copy mentioning scanning. The capability check fails closed, so an unreachable API hides it too. Manual itemizing is untouched |

## 2. Architecture

```
Phone browser (secure context — same requirement passkeys already impose)
  │  <input type="file" accept="image/*" capture="environment">
  │  canvas downscale → JPEG q0.85, long edge 1600px
  │
  │  POST /api/v1/receipts/scan   (multipart, JWT)
  ▼
Go API
  │  validate size/type, decode, re-encode, base64
  │
  │  normalize: EXIF-rotate, force LANDSCAPE, 1600px long edge, JPEG q0.85
  │
  │  POST {RECEIPT_OCR_URL}/api/chat   — ONE call
  │    images=[raw base64], format=<schema>,
  │    think=false, options={temperature:0, num_ctx:32768}   (~49s)
  ▼
Strix Halo — existing Ollama server (:11434), reached DIRECTLY (not via openclaw)
  │
  │  ◄── receipt facts JSON
  ▼
Go API
  ├─ integer-cent tax allocation (deterministic, largest-remainder)
  ├─ reconciliation check against printed subtotal/total
  ├─ budget suggestion via norm_key history lookup
  └─ return DRAFT — nothing persisted yet
       ▼
Review UI (existing Itemize modal, now populated)
  │  user corrects amounts, sets budget dropdowns
  │  POST /api/v1/receipts   → one DB transaction
  ▼
receipts + receipt_items + grouped transacts
```

Nothing is written to the database until the user confirms. A scan that goes badly
costs nothing but time.

## 3. Inference contract

### 3.1 Serving prerequisites (verify before writing app code)

Target stack is the **existing Ollama server** on the Strix box, called directly on
`:11434`. Openclaw is a peer consumer of that same server, not a layer in this path —
routing through it would apply its own templates and prompts to a schema-constrained
extraction call.

- **Projector: nothing to do.** `ollama pull qwen3.8:27b` bundles the vision projector;
  the library entry carries capability tags `vision tools thinking 27b`. This is Ollama's
  main advantage over raw llama.cpp here — no separate `mmproj` file to manage.
- **Use `/api/chat`, not `/v1/chat/completions`.** Ollama does not recognize OpenAI's
  `response_format: json_schema` ([ollama#10001]) and silently ignores it, yielding
  *unconstrained* output that usually resembles JSON. That is the worst failure mode:
  clean in testing, broken on receipt 50. Only the native top-level `format` parameter
  compiles the schema into a real grammar.
- **Set `num_ctx` explicitly.** Ollama defaults to ~4096 tokens and, on overflow,
  **truncates from the beginning with no error or warning**. Image embeddings plus 40
  items of JSON will exceed that. Pass `options.num_ctx` per request (32768 is ample) —
  simpler than a Modelfile, and per-request beats the `OLLAMA_CONTEXT_LENGTH` env var.
  Note a Modelfile `PARAMETER num_ctx` overrides the env var, so check for a stale one.
- **`temperature: 0`.** Extraction is deterministic transcription; sampling only invents
  prices.
- **Thinking: try `think: false`, but verify it took.** See the risk in §12 — this may
  not be controllable through Ollama at all.
- **Image encoding:** raw base64 on the message's `images` array. No `data:` URI prefix
  (that is the OpenAI shape, and it will fail here).
- **Keep the model resident.** Ollama unloads after ~5 minutes idle; a cold scan pays an
  18GB load first. Set `OLLAMA_KEEP_ALIVE`. If openclaw uses a *different* model on the
  same box, the two will thrash each other in and out — sharing `qwen3.8:27b` avoids it.

Smoke test before any Go code — `scripts/receipt-ocr-smoke.sh receipt.jpg`. It sends the
real schema, then runs the §4.1 reconciliation checks and reports `eval_count` and
thinking-token length so you can see both the accuracy and the latency picture at once.

[ollama#10001]: https://github.com/ollama/ollama/issues/10001

### 3.4 Orientation is the whole problem — measured on hardware

Run against `qwen3.8:27b` on the Strix box, on a real 4-item Target receipt.

A **portrait** 960×2320 render (1:2.42) failed badly: 2 of 4 items, GATORADE and
Good&Gather dropped, "Bodum" duplicated, a hallucinated DPCI (`033220526`) — and
`confidence: 1.0` while doing it. Re-running at 4× vision tokens (`prompt_eval`
589 → 2350) produced **byte-identical** output, ruling out resolution.

Free-form transcription of the same portrait image reproduced everything from `SUBTOTAL`
down perfectly — AID, auth code, the return-policy paragraph, REC#, password — and
nothing above it. Cropping the top half and sending it alone read all four items
flawlessly. Since full-image `prompt_eval` (2350) ≈ the sum of both halves (2×1112),
**the top's vision tokens were present and simply not attended to.** Silent, and
deterministic at temperature 0.

Rotating those identical pixels 90° into **landscape** fixed it outright — the whole
receipt, in one call, `SUBTOTAL` included. Aspect ratio was unchanged at 2.42:1.

**That conclusion did not survive live testing, and the reason is instructive.** A
receipt's text lines run across its *narrow* axis, so laying the receipt horizontally
leaves every line rotated 90°. On a rendered synthetic image the model tolerated that;
on a real photo it did not. Forcing landscape on the real photo produced garbage
(`GROCERY` and `KITCHEN` as items), while the orientation the photographer actually
framed — EXIF applied, nothing else — was the only geometry that ever produced a clean
extraction. **Preserve orientation; optimize resolution instead.**

| input | geometry | result |
|---|---|---|
| synthetic portrait + grammar | 960×2320 (1:2.4) | 2 of 4 items, 1 hallucinated, no reconcile |
| same at 4× vision tokens | 960×2320 | byte-identical failure |
| same, cropped to top half | 960×1160 | all 4 items, flawless |
| same, **rotated to landscape** | 2320×960 | **whole receipt, one call** |
| **real photo, as shot** | 4032×3024 (1.33:1) | **whole receipt, 61s** |
| real photo, 1600px *wide* (portrait) | 1600×2134 | **4/4, reconciles 0¢, 49s** |
| real photo forced landscape | 1600×1200 | garbage: headers as items |
| real photo, upright, 1536×2048 / 1800×2400 / native | portrait | headers as items, 3 runs |

A camera photographs a long receipt sideways, so real photos land in landscape naturally
— which is why the real one never hit the failure. The risk is a client that "helpfully"
rotates to portrait, or a tall stitched/scanned image. Hence D2c: **normalize to
landscape explicitly**; never rely on the user having held the phone a particular way.

**Extraction reliability, before cropping, was lower than early samples suggested.**
Document detection (§3.6) largely resolved this; what follows records what *uncropped*
photos did, and is why reconciliation gates the commit. Repeat runs on the same
crumpled photo diverge: the geometry that scored 4/4 once later transcribed `$6.99` as
`$6.00` and the date as `05/20/2026`, and the current prompt persistently reads the
`GROCERY` and `KITCHEN` department headers as items with their product codes as amounts.
Subtotal, tax and total come back correct essentially every time; the *item list* is the
fragile part. This is why reconciliation gates the commit rather than decorating it, and
why the prompt needs iterating against a corpus of real receipts rather than one photo.

Also established:

- **`confidence` is worthless** — 1.0 on the 2-of-4 answer. Reconciliation caught it
  instantly. The field is dropped from the schema.
- **There is a vision-token ceiling.** 2048px and native 4032px both report
  `prompt_eval=4057`, so 2048 is the cheapest bound that keeps full effective resolution.
- **`think: false` is honoured** — `thinking_chars=0` on every call.
- **Model identity was verified**, not assumed: asked to describe the photo, it reported
  "a dark gray, textured surface that appears to be a car seat" and "a black seatbelt
  buckle visible in the lower right corner" — both real, neither in the synthetic. The
  transcriptions converging byte-for-byte was genuine determinism, not a cache.

### 3.2 Extraction schema

The model reports **only what it can see**. It computes nothing.

```json
{
  "merchant": "COSTCO WHOLESALE #487",
  "purchased_at": "2026-08-16",
  "currency": "USD",
  "items": [
    {
      "position": 1,
      "line_text": "GRND BF 93/7      8.99 T",
      "description": "Ground beef 93/7",
      "quantity": 1,
      "unit_price": 8.99,
      "amount": 8.99,
      "taxable": true,
      "marker": "T"
    }
  ],
  "adjustments": [
    { "label": "MFR COUPON", "amount": -2.00, "applies_to_position": 3 }
  ],
  "tax_lines": [
    { "label": "MD TAX", "rate": 0.06, "base": 70.66, "amount": 4.24 }
  ],
  "subtotal": 39.00,
  "total": 42.41,
  "tax_evidence": "per_line_flags",
  "notes": []
}
```

Field notes:

- **`line_text`** — verbatim, including the taxability marker. This is the audit record
  and the key for history-based suggestions. Never normalized by the model.
- **`taxable`** — tri-state: `true` / `false` / `null` (no marker visible). `null` is a
  first-class answer, not a failure.
- **`tax_evidence`** — `per_line_flags` | `single_rate` | `multi_rate` | `unknown`.
  This is how the model "decides" the tax strategy: it reports what the receipt showed
  and the server picks the matching allocation algorithm.
- **`tax_lines[].base`** — the base the receipt printed for that tax line, as in
  `MD TAX 6.00000 on $70.66`. **The single most valuable field in the schema.** It states
  the taxable subtotal outright, so the server never has to guess the taxable set from
  markers. Validated: this receipt marks two items `TF` and two `T`, and only the printed
  base reveals that all four were taxed at 6%.
- **`marker`** — the raw taxability marker (`T`, `TF`, `N`, `F`) kept verbatim, because
  its *meaning* is retailer- and state-specific and we should not bake in a reading.
- **No per-item tax field.** Deliberate. The server derives it.
- **No `confidence` field.** Measured useless (§3.4).
- **`adjustments`** — discounts/coupons as negative amounts, attached to a line when the
  receipt makes the association clear, unattached otherwise.

### 3.3 Prompt rules

1. Transcribe, do not infer. Copy printed text and numbers exactly.
2. Emit `null` rather than guessing. A missing date is `null`, not today.
3. Never perform arithmetic. Do not sum, do not compute per-item tax, do not "fix"
   a subtotal that looks wrong — report what is printed.
4. Report every taxability marker seen (`T`, `N`, `*`, `F`) via `taxable`, and set
   `tax_evidence` to describe what kind of evidence the receipt provided.
5. Non-item lines (subtotal, tax, total, payment method, loyalty balance) go in their
   own fields or are omitted — never in `items`.
6. Sub-lines such as `Regular Price $39.99` and `Buy1Get1 50%off` are **informational**
   when the item amount is already the net price. They are NOT adjustments and must not
   be emitted as such. Validated: a BOGO receipt whose $20.00 discount was already baked
   into two $29.99 line items reconciled exactly only because these were dropped.
7. If a tax line prints its own base, record it in `base` and the rate as a decimal.

### 3.5 Resolution floor and where the time actually goes

Two Ollama measurements separate prefill from generation. A text-only call
(`prompt_eval=625, eval=432`) took 21s; the same output with an image
(`prompt_eval=3556, eval=430`) took 49s. Differencing gives **prefill ≈ 123 tok/s** and
**generation ≈ 19 tok/s** on this box. A prompt-cache hit confirmed it independently:
an identical repeat call dropped 58s → 23s, exactly the prefill.

Sweeping the long edge (landscape, real photo):

| long edge | prompt_eval | wall | result |
|---|---|---|---|
| 1600 | 2195 | **35s** | reconciles 0¢ |
| 1200 | 1359 | 31s | fail — items 72.96 vs 70.66, total read as 76.65 |
| 1000 | 1008 | 30s | fail — items off by $0.30 |

So **downsampling below 1600 is a trap**: it saves ~5s and breaks price reading. At
1600 the split is prefill 18s versus generation 23s — generation already dominates, so
the remaining lever is fewer *output* tokens (schema slimming), not fewer pixels.

Sending *more* than 1600 is also waste: 2048px and native 4032px both hit the same
`prompt_eval` ceiling for +20% wall clock.

Three measurement bugs worth not repeating, each of which produced a wrong conclusion:

1. `scale=N:-2` pins **width**, not the long edge. The "1600px is the efficient frontier"
   result was really 1600×2134, so the true resolution floor was overstated by a third.
   Use `force_original_aspect_ratio=decrease`.
2. Raw JPEG SOF markers **and `ffprobe`** report pre-EXIF dimensions (4032×3024) while
   every decoder renders 3024×4032. Orientation logic must measure an actual decode.
3. Ollama caches prompts, so an identical repeat call returns in a fraction of the time.
   Differencing a cache hit against a cold call is a clean way to separate prefill from
   generation (~123 tok/s vs ~19 tok/s here), but treating the warm number as latency
   understates it threefold.

And one production bug that only end-to-end testing surfaced: `cmd/api/main.go` shipped
`ReadTimeout`/`WriteTimeout` of **10 seconds**. A synchronous scan runs 30–60s, so the
connection was torn down before the handler could answer — the client saw an empty reply
while the server logged a *successful* request. The timeouts now scale off
`RECEIPT_OCR_TIMEOUT_MS`, with `ReadHeaderTimeout` kept short.

#### Re-measured on llama.cpp with MTP, and the crop floor that followed

The numbers above are from Ollama. Re-measured on llama-server with speculative
decoding, per receipt, separating the two phases:

| receipt | crop | image tokens | prefill | gen tokens | generation | wall |
|---|---|---|---|---|---|---|
| Target | 737x2048 (cropped) | 1853 | 8.5s | 428 | **22.0s** | 30.7s |
| Lowe's | 3024x4032 (uncropped) | 4396 | 30.2s | 649 | **34.1s** | 64.6s |
| Brasserie | 764x1191 (cropped) | 1269 | 5.4s | 1068 | **47.5s** | 53.2s |

Generation is 72%, 53% and 90% of wall clock. The earlier conclusion holds and
strengthens: pixels are not the expensive part, output tokens are. Note also that
generation volume tracks item count -- 1068 tokens for 14 items against 428 for 4.

This is what made the resolution question worth revisiting, since prefill is cheap
enough to spend. Two findings, and they point opposite ways.

**Raising `maxEdge` is the wrong lever.** Brasserie's crop is 764x1191 at maxEdge
2048, 3072 *and* 4096 -- identical, because the photo is only 1080x1920 and the
receipt occupies that much of it. The cap was never the constraint; the source was
exhausted. Meanwhile Target *is* cap-bound (737x2048 -> 1475x4096), and raising it
there cost **+16s (31s -> 47s) for a byte-identical answer**, since that receipt
already read correctly. A global increase taxes the receipts that do not need it and
does nothing for the one that does.

**Upscaling a small crop is the right lever.** Upscaling adds no information, which
is why it is not done in general. It helps anyway when the crop is small, because the
vision encoder tiles into fixed-size patches: below some size a digit stops spanning
enough patches to resolve, so detail present in the crop is lost at the encoder
rather than in the optics. On Brasserie:

| scale | crop | wall | Hot Chips (printed 16.00) | result |
|---|---|---|---|---|
| 1.0x | 764x1191 | 53s | read `15.00` | items 420.00 vs subtotal 421.00 |
| **1.5x** | 1146x1786 | **59s** | read `16.00` | **reconciles** |
| 2.0x | 1528x2382 | 75s | read `16.00` | reconciles |
| 3.0x | 2292x3572 | 79s | read `16.00` | reconciles |

The gain arrives by 1.5x and nothing beyond it helps, which is the signature of a
sampling floor rather than a lucky reroll.

So `MinCropEdge = 1800` is a **floor, not a target**: a crop already at or above it
is untouched, and it is applied inside `extractRect`'s existing affine resample, so
there is one interpolation pass rather than two. `maxEdge` still bounds it, and
`maxCropUpscale = 2.0` caps the factor -- an unusually small crop is more often a bad
detection than a small receipt, and blowing that up would spend prefill on a blurred
mistake.

End to end, all three now reconcile, and the two that already worked pay nothing:

| receipt | before | after |
|---|---|---|
| Target | 737x2048, 30s, reconciles | unchanged |
| Lowe's | 3024x4032, 65s, reconciles | unchanged |
| Brasserie | 764x1191, 53s, **off by $1.00** | **1155x1800, 59s, reconciles** |

Caveat: this is one receipt's worth of evidence for the fix. It is known not to
regress the other two only because the floor does not fire on them. Whether
interpolation ever *hurts* a receipt is untested, and the risk is bounded only by the
rule firing solely on small crops -- which are already the worst-performing class.
The real fix for such an image remains a sharper photograph, which the app cannot
control.

### 3.7 Why not a smaller model

Tested on the same photo and geometry. Memory is not the constraint — 17.7GB of 128GB —
**bandwidth is**, so a smaller dense model is genuinely faster per token:

| model | size | generation | thinking disabled? | receipt result |
|---|---|---|---|---|
| `qwen3.8:27b` | 17.7GB | 19 tok/s | **yes**, `thinking_chars=0` | **35s, correct** |
| `qwen3-vl:8b` | 6.1GB | 37 tok/s | no | never completed (>170s) |
| `qwen3-vl:4b` | 3.3GB | 60 tok/s | no | 8,259 thinking chars, 0 content, 150s+ |
| 4 non-thinking VLMs | 2.2–6GB | n/a | **n/a by construction** | all failed — see below |

`think: false`, `think: "low"`, omitting the key, and a `/no_think` suffix were all
ignored by the qwen3-vl models — `/no_think` made it worse, echoed as content. Their
Ollama `template` is the 13-char stub `{{ .Prompt }}`, i.e. Ollama renders these models
through a built-in Go path, so a Modelfile `TEMPLATE` override cannot reach it either.

Grammar makes it pathological rather than merely slow: the schema forbids the `<think>`
tokens the model insists on emitting, and the call hangs indefinitely.

**Non-thinking VLMs were then surveyed** — models structurally incapable of thinking, so
the control problem disappears by construction. Four were pulled and tested; all four
report no `thinking` capability, and all four failed the task on Ollama 0.32.13:

| model | size | control test (one word) | receipt |
|---|---|---|---|
| `minicpm-v:8b` | 5.5GB | **correct, 2.6s** | header only, skipped every item, degenerate loops below 1600px |
| `glm-ocr` | 2.2GB (1.1B) | read it, then looped 8,017 tokens / 114s | digit-loop garbage (`0000000…`) |
| `granite3.2-vision:2b` | 2.4GB | emitted ` ``` `, `prompt_eval=2249` for a 465×255 image | empty output, `eval=1`; HTTP 500 with grammar |
| `qwen2.5vl:7b` | 6GB | ` addCriterion` | ` 自动生成一行`, `eval=3` |

The failure *shapes* matter. Empty output, 3-token non-sequiturs in the wrong language,
and degenerate digit loops are **broken integrations**, not models too small to read a
receipt — a model that genuinely saw the image would produce plausible *wrong* text. Only
`minicpm-v:8b` is demonstrably wired up correctly (it read a one-word control image in
2.6s), and it still cannot do dense receipt transcription: at 1600px it transcribed the
header verbatim, then jumped to the footer and omitted the entire item block.

So `qwen3.8:27b`'s advantage is not only capacity. It is the one model on this box that is
**both** correctly integrated in this Ollama build **and** has controllable thinking.
Small non-thinking VLMs are not a dead end in principle — they are a dead end on Ollama
0.32.13, which is another argument for `llama-server` if this ever needs revisiting.

**Thinking control on Ollama is per-model, not a platform guarantee.** That
`qwen3.8:27b` honours it is luck worth keeping. Any model swap must re-verify
`thinking_chars=0` before anything else.

This makes **`llama-server --jinja`** the concrete path to a 2–3× speedup rather than a
vague note: it honours the real chat template (so `enable_thinking=false` works) and
unlocks the qwen3-vl speeds above, plus the 30–36 t/s ROCmFP4 + MTP figures. Worth doing
only if §12's latency ceiling actually bites.

### 3.6 Document detection — the change that made extraction reliable

Everything above tuned *how the photo was framed*. The actual problem was that the receipt
is only part of the photo: on the reference image it covers 41% of the frame, so bounding
the long edge spent most of the pixel budget on a car seat and left the print too small to
read. Every uncropped variant failed — portrait, landscape, 1536px, 2048px, native —
usually by reading the `GROCERY` and `KITCHEN` department headers as items with their
product codes as amounts. Prompt rules aimed squarely at that did not shift it.

Cropping to the paper fixed it outright:

| input | result |
|---|---|
| whole frame, portrait or landscape, 1536–4032px | headers as items, never reconciled (6+ runs) |
| **detected, cropped, deskewed → 737×2048** | **4/4 items exact, reconciled 0¢, twice running** |

```
GATORADE 699+42  Good&Gather 369+22  ELEC KETTLE 2999+180  Bodum 2999+180
markers TF/TF/T/T correct - sum 7490 == printed 7490 - 22-27s
```

It is also *faster* — 22–27s against 36–48s — because the crop carries no background
tokens. Accuracy and latency moved the same direction, which is rare.

**The pipeline is plain image processing, no model:**

1. Downscale to a 640px working copy (detection needs shape, not detail).
2. Otsu threshold — bright paper against a darker surface is near the ideal case.
3. Largest 4-connected component, keeping only boundary pixels.
4. Convex hull (Andrew monotone chain), then minimum-area rectangle by rotating calipers.
5. Plausibility guards; on any failure, decline and use the whole frame.
6. Crop, deskew and scale in **one** `CatmullRom.Transform`. One resample rather than
   three: each extra pass compounds the blurring that costs price-reading accuracy.

**Two details that are easy to get wrong:**

- **Rotating calipers give an axis, not a direction.** The long axis is equally likely to
  point up or down, so the crop came out upside down on the first attempt. Resolving it
  from glyph shapes would be fragile; instead the long axis is pointed along the
  *photograph's* own downward direction, since people frame a receipt top-up. The
  cross-axis is then a fixed quarter turn of it, which makes a mirrored crop impossible.
- **Orientation falls out for free.** With the outline known, the receipt's long axis
  becomes the output's vertical axis, so its lines — which run across the narrow axis —
  come out horizontal. Orientation stops being a guess about frame shape, which is what
  the earlier landscape-versus-portrait flailing was really about.

**Guards, and the limit of them.** Area fraction 4–97%, aspect 1.15–25, rectangle fill
≥ 0.55, and contrast ≥ 24 grey levels between the candidate and its surroundings. The
contrast test is what rejects texture that merely happens to be rectangular. It is not
airtight: a strongly banded image still passes, and
`TestDetectDocumentCanBeFooledByBrightBands` pins that rather than hiding it. Hence
detection is fail-safe by construction — a wrong crop loses data permanently, so anything
doubtful falls back to the whole frame — and reconciliation plus the review screen remain
the real backstop.

**iOS document scanning is not available to a web app.** `VNDocumentCameraViewController`
is native-only; there is no Safari API for it, which is why this is implemented in-house.
Two partial routes exist if wanted later: a user can scan in Notes or Files and pick the
result through the file input (already cropped and contrast-boosted), or a native wrapper
could hand VisionKit's output to the same endpoint. Neither is the default path.

### 3.8 Why llama.cpp, not Ollama

Two receipts refused to extract for days under Ollama. Prompt work went nowhere:
explicit item-block rules, a transcription-first schema, resolution sweeps, encode
quality, lossless upload. Swapping the inference server fixed both, with the prompt
and schema completely unchanged.

| receipt | Ollama | llama-server |
|---|---|---|
| Target, 4 items | 4/4 | **4/4** |
| Brasserie, 14 items | 9/14 | **14/14** |
| Lowe's, 6 items | 1/6 | **6/6** |

The cause is almost certainly the chat template. `llama-server --jinja` applies the
model's own template; Ollama substitutes a generic one -- visible earlier as a
13-character `{{ .Prompt }}` stub, and the same substitution that made thinking
impossible to disable on other vision models. A wrong template degrades everything
downstream, which is why no amount of prompt phrasing recovered it.

**The correctness reason matters more than the accuracy one.** Ollama would
sporadically answer with a *previous* image's contents. A Lowe's scan returned
Brasserie's items byte-for-byte, subtotal and total included, and it reconciled
perfectly -- so the safety net could not catch it. Diagnosis:

- Not aborted requests. Cancelling mid-inference changed nothing.
- Not request volume. A 30-scan soak passed, then it recurred ~40 requests later.
- Not fixed by upgrading. It survived 0.32.13 -> 0.32.15.
- The server logs two different images producing an identical `task.n_tokens = 1269`,
  so a cached prompt prefix matches without accounting for the image content.
- A per-request nonce appended to the prompt did not help, consistent with the image
  tokens sitting before the text in the cached prefix.

The soak passed because it only compared printed totals, and in the partial form of
the failure the totals come from the *correct* image while the items come from the
previous one. That form does fail reconciliation. Only the fully-stale form is
silent, and that is what makes it unacceptable.

`llama-server` takes `cache_prompt: false` per request, which removes the mechanism
rather than mitigating it. Six alternating requests with no flushing showed no
leakage.

**Speed: recovered with MTP.** Out of the box generation was ~12 tok/s against
Ollama's 30, so a scan took 45-95s. Flash attention and context size made no
difference; the gap was speculative decoding.

Generating a token means streaming all 17GB of weights, so it is bandwidth-bound.
Verifying several candidate tokens costs one batched pass over those same weights, so
if something cheaply guesses ahead, the model checks the batch and several tokens
arrive for the price of roughly one. Wrong guesses are discarded, which makes the
output bit-identical -- a pure speedup, not a quality trade.

Multi-Token Prediction does the guessing inside the model itself: extra heads trained
to predict token n+2, n+3. No draft model, no second set of weights in memory. This
GGUF already carries it --

    general.architecture       = qwen35          (the loader that implements MTP)
    qwen35.nextn_predict_layers = 1
    blk.64.nextn.{eh_proj,enorm,hnorm,shared_head_norm}

-- and llama.cpp implements it for this architecture, but the default is off. It needs
`--spec-type draft-mtp`, which costs 708 MiB of MTP context:

| workload | before | with MTP | Ollama |
|---|---|---|---|
| generation, predictable text | 11.9 tok/s | **26.6** | 30.1 |
| receipt scan, Target | 45s | **31s** | ~30s |
| receipt scan, Brasserie | 95s | **53s** | (never correct) |
| receipt scan, Lowe's | 80s | **65s** | (never correct) |
| openclaw agent turn | 11.3 tok/s | **20.1** | ~30 |

Draft acceptance runs 0.70-0.85 on real receipts and 1.00 on predictable text, and all
three receipts still reconcile exactly, as an exact speculative scheme must. That
leaves llama.cpp near parity on generation and ahead on prompt processing (~300 tok/s
against ~270), which matters for openclaw's large system prompt.

**The service definition**, since these flags are not the defaults and the reasoning
is not obvious from them:

    llama-server \
      --model  <ollama blobs>/sha256-f5f1dd89...   # shared, not a second 17GB copy
      --mmproj <ollama blobs>/sha256-ac3714bf...   # vision projector
      --alias qwen3.8-27b --host 0.0.0.0 --port 11435 \
      --ctx-size 131072      # matches what openclaw asks for; one server backs both
      --parallel 1 \
      --n-gpu-layers 999 \
      --jinja                # the model's own chat template: the accuracy fix
      --spec-type draft-mtp  # self-speculation: the speed fix

**Deployment.** `llama-server` runs as its own systemd unit on port 11435, as the
`ollama` user (already in `render`/`video` for ROCm) reading Ollama's own GGUF blobs
rather than a second 17GB copy. `--ctx-size 131072` matches what openclaw asks for,
since one server backs both. Ollama stays installed for embeddings (memory search
needs `nomic-embed-text`, and llama-server serves one model at a time).

Select the backend with `RECEIPT_OCR_API` (`llamacpp` by default, `ollama` still
supported and tested). The two clients share one prompt and one schema, asserted by a
test, so a change to either cannot silently apply to only one backend.

### 3.9 Memory bounds — what OOM-killed the API

On 2026-08-22 the API was OOM-killed twice, minutes after three scans:

```
13:05:26  Out of memory: Killed process 1712142 (api)
          total-vm:1630448kB  anon-rss:303160kB
13:08:26  dockerd invoked oom-killer   global_oom
```

It was not a leak. Five sequential scans allocate an identical 162MB each and
settle back to the same 36MB heap; nothing accumulates. It was an unbounded
per-request spike on a 917MiB host with no swap — and because the Swarm service
had no memory limit, the kernel fired a *global* OOM, so one scan endangered
Postgres, Caddy and everything else on the box.

Two things drive the spike, and they are not the same thing.

**Pixel count.** The pipeline holds several full-frame buffers: the decode, plus
RGBA copies at 4 bytes per pixel for the rotate and downscale steps. Measured
allocation per scan:

| input | allocated | peak RSS |
|---|---|---|
| 3200x2400 (8MP, what the browser sends) | 82MB | 50MB |
| 4032x3024 (12MP, native iPhone) | 162MB | 74MB |
| 8000x6000 (48MP) | 955MB | — |
| 8600x6900 (59MP, the old `MaxPixels`) | 1086MB | — |

The old limit of 60M pixels was chosen to accommodate a 48MP sensor, which simply
cannot be honoured at native resolution on a 1GB host. `MaxPixels` is now 16M.

**The resampler, which is a cliff rather than a slope.** Above `UncroppedMaxEdge`
the uncropped path calls `downscale`, and x/image's Catmull-Rom allocates a
separable-pass scratch buffer of `dst_width x src_height x 4` float64s — roughly
32 bytes per *source* pixel. Peak RSS therefore jumps from 74MB at 4032x3024 to
**559MB at 4618x3464**: the same order of megapixels, either side of the point the
resampler engages.

Two consequences worth recording:

- **Lowering `MaxPixels` cannot fix this**, because the scratch scales with
  whatever the limit allows (~`MaxPixels` x 32 bytes).
- **`GOMEMLIMIT` barely helps** — 559MB became 507MB. The scratch is *live* while
  the resample runs, and a soft limit can only collect garbage, not shrink live
  data. It is still set, because it does help the ordinary allocation churn, but it
  is not the mechanism that bounds the worst case.

What bounds it is refusing to enter the expensive path: `MaxLongEdge` caps the
source's long edge at 4096. That costs nothing real, since 4096 is already the most
this pipeline keeps on the long edge, the browser re-encodes to 3200 before upload,
and a native 12MP photo is 4032. Only an unusually elongated original — a panorama,
or a direct caller's 8000x2000 — is refused, with a 413 naming the guard.

Both guards are checked from the header via `DecodeConfig`, before any pixels are
allocated. With both in place the worst case admitted is **96MB peak RSS**, down
from 1086MB.

The service also now carries a hard memory limit, so that if this analysis is
wrong somewhere, the container dies alone instead of taking the host with it.

A note on metrics: `runtime.MemStats.Sys` reports reserved virtual address space,
not resident memory, and reads far higher than what the OOM killer accounts for
(983MB `Sys` against a 96MB RSS). The numbers above are `VmHWM` from
`/proc/self/status`.

## 4. Tax allocation (server-side, deterministic)

All allocation happens in **integer cents**. Floats are converted at the boundary and
never used for distribution.

```
1. Parse every amount to integer cents immediately.

2. Determine the taxable set T per tax line, strongest evidence first:
   - **tax line printed a `base`** → trust it over every marker:
       base == sum(all item cents)  → T = all items
       base != sum(all item cents)  → T = marker-taxable items, verify against base
     (Validated: this is what correctly taxes a TF/TF/T/T receipt across all four items.)
   - tax_evidence = per_line_flags → T = items where taxable == true
   - tax_evidence = single_rate and rate is printed:
       implied_base = round(tax_cents / rate)
       if implied_base ≈ sum(all item cents) within tolerance → T = all items
       else → T = items where taxable != false, and record a note
   - tax_evidence = multi_rate → match each tax line to its marker group
   - tax_evidence = unknown → T = all items (flat proration)

3. Allocate each tax line over T by largest-remainder:
     base = sum(cents of items in T)
     share_i = floor(tax_cents * item_i / base)
     leftover = tax_cents - sum(share_i)
     distribute leftover one cent at a time, largest fractional remainder first,
     ties broken by item position (fully deterministic — no randomness)

4. Unattached adjustments prorate the same way; attached ones reduce their line first.

5. item_total_i = item_cents_i + allocated_tax_i + allocated_adjustment_i
```

Guarantee: `sum(item_total) == total_cents`, exactly, always. Every cent on the receipt
lands in exactly one budget.

### 4.1 Reconciliation — the free validator

Two independent checks:

- `sum(item amounts) == subtotal`
- `subtotal + sum(tax_lines) + sum(adjustments) == total`

Either failing means the extraction is wrong — a misread price, a dropped line, a
hallucinated item. This is far more reliable than the model's own `confidence`. On
failure the draft is returned anyway, flagged:

```json
"reconciliation": {
  "ok": false,
  "items_sum_cents": 4312,
  "printed_total_cents": 4788,
  "delta_cents": 476,
  "message": "Items sum to 43.12 but the receipt total is 47.88."
}
```

The UI shows this as a banner and lets the user fix it by hand (D8). We do not block
the commit — the user can see the receipt and is the authority.

## 5. Budget suggestions from history

`norm_key` normalizes a line for matching: uppercase, strip digits, currency symbols,
unit suffixes and punctuation, collapse whitespace. `GRND BF 93/7 8.99 T` → `GRND BF`.

On scan, for each item:

```sql
SELECT ri.budget_id
FROM receipt_items ri
JOIN users_budgets ub ON ub.budget_id = ri.budget_id
WHERE ri.norm_key = $1 AND ub.user_id = $2 AND ri.budget_id IS NOT NULL
GROUP BY ri.budget_id
ORDER BY COUNT(*) DESC, MAX(ri.id) DESC
LIMIT 1;
```

Most-frequent wins, most-recent breaks ties. Scoped through `users_budgets` so shared
budgets accumulate shared learning (D13). No match → the catch-all budget, with the
line marked unsuggested so the user knows to look at it.

Suggested lines are visually distinguished in the review list. Trust, but make it
verifiable at a glance.

**The client did not honour this for its first several weeks, and the divergence was
self-reinforcing.** `buildReceiptCommit` filtered out any line whose budget the user
had not set explicitly, and folded their combined value into a single "Unitemized
remainder" line on the catch-all. The money reached the right budget, so nothing
looked broken in the ledger, but two things were wrong:

- On a grocery run where one item belongs elsewhere, every *other* line had to be
  assigned by hand or lose its identity -- the opposite of what a catch-all is for.
- The dropped lines' `norm_key`s never reached the server, and that column is what
  the query above learns from. A merchant with no history produces no suggestions, so
  every line arrives unassigned, so every line was dropped, so no history was
  written. Suggestions could never bootstrap for any new merchant.

Unassigned lines are now committed individually against the catch-all, keeping their
descriptions and keys. The remainder line still exists, but only for value the lines
genuinely do not account for -- a hand-typed total above the sum of its items.

Two supporting changes were needed. The commit-readiness check required at least one
*assigned* line, which meant a scan of an unseen merchant could not be committed at
all until a row was set by hand; it now requires only a line with an amount. And the
per-item dropdown's blank option now names the catch-all ("Groceries (default)"),
because a blank reads as "nothing chosen yet" and invites exactly the row-by-row
selection this was meant to avoid.

The server side needed nothing: `CommitReceipt` already mapped a nil or zero
`budget_id` to the catch-all, and `TestCommitReceiptUnassignedItemsGoToCatchAll`
already covered it. The contract was implemented at the wrong end of the wire.

## 6. Data model

Appended to `db/schema.sql`, which `ApplyMigrations` (`internal/database/migrations.go:11`)
execs whole in one transaction. All statements idempotent, matching existing style.
**Postgres 11** in `docker-compose.yml` — no generated columns.

```sql
CREATE TABLE IF NOT EXISTS receipts (
  id SERIAL PRIMARY KEY,
  user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
  merchant VARCHAR NOT NULL DEFAULT '',
  purchased_at TIMESTAMP,
  currency VARCHAR NOT NULL DEFAULT 'USD',
  subtotal_cents INTEGER NOT NULL DEFAULT 0,
  tax_cents INTEGER NOT NULL DEFAULT 0,
  total_cents INTEGER NOT NULL DEFAULT 0,
  tax_evidence VARCHAR NOT NULL DEFAULT 'unknown',
  parsed JSONB NOT NULL DEFAULT '{}'::jsonb,
  model VARCHAR NOT NULL DEFAULT '',
  created_at TIMESTAMP NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS index_receipts_on_user_id ON receipts (user_id);

CREATE TABLE IF NOT EXISTS receipt_items (
  id SERIAL PRIMARY KEY,
  receipt_id INTEGER NOT NULL REFERENCES receipts(id) ON DELETE CASCADE,
  budget_id INTEGER REFERENCES budgets(id) ON DELETE SET NULL,
  transact_id INTEGER REFERENCES transacts(id) ON DELETE SET NULL,
  line_text VARCHAR NOT NULL DEFAULT '',
  norm_key VARCHAR NOT NULL DEFAULT '',
  description VARCHAR NOT NULL DEFAULT '',
  amount_cents INTEGER NOT NULL DEFAULT 0,
  tax_cents INTEGER NOT NULL DEFAULT 0,
  taxable BOOLEAN,
  position INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS index_receipt_items_on_norm_key ON receipt_items (norm_key);
CREATE INDEX IF NOT EXISTS index_receipt_items_on_receipt_id ON receipt_items (receipt_id);
CREATE INDEX IF NOT EXISTS index_receipt_items_on_budget_id ON receipt_items (budget_id);

ALTER TABLE transacts
  ADD COLUMN IF NOT EXISTS receipt_id INTEGER REFERENCES receipts(id) ON DELETE SET NULL;
```

`parsed` keeps the raw model output — the audit trail for "why did it think that?" and
the corpus for improving the prompt later.

**Known wart:** money is `INTEGER` cents in the new tables but `DOUBLE PRECISION` in
`transacts.amount`. All allocation math is in cents; conversion to float happens once, at
the `transacts` write. Migrating `transacts` to cents is out of scope here.

## 7. API surface

Both routes sit under `/api/v1/`, so they inherit JWT auth from `router.go:38`
automatically. No auth work required.

Registered alongside the existing handlers in `api.go:76`:

```go
mux.HandleFunc("/receipts", h.handleReceipts)
mux.HandleFunc("/receipts/scan", h.handleReceiptScan)
```

### `POST /api/v1/receipts/scan`

`multipart/form-data`, field `image`. Persists nothing. Returns a draft:

```json
{
  "merchant": "COSTCO WHOLESALE #487",
  "purchased_at": "2026-08-16",
  "total_cents": 4241,
  "subtotal_cents": 3900,
  "tax_cents": 341,
  "tax_evidence": "per_line_flags",
  "reconciliation": { "ok": true, "delta_cents": 0 },
  "items": [
    {
      "position": 1,
      "line_text": "GRND BF 93/7      8.99 T",
      "description": "Ground beef 93/7",
      "amount_cents": 899,
      "tax_cents": 79,
      "total_cents": 978,
      "taxable": true,
      "suggested_budget_id": 4,
      "suggestion_source": "history"
    }
  ]
}
```

Errors: `413` oversize, `415` bad type, `502` inference unreachable, `504` timeout.
All of them land the client in pre-filled manual entry (D8).

### `POST /api/v1/receipts`

Commit. One DB transaction (D12), replacing the loop at `Dashboard.tsx:387`.

```json
{
  "merchant": "COSTCO WHOLESALE #487",
  "purchased_at": "2026-08-16",
  "catch_all_budget_id": 2,
  "total_cents": 4241,
  "items": [
    { "line_text": "...", "description": "Ground beef",
      "amount_cents": 899, "tax_cents": 79, "budget_id": 4 }
  ]
}
```

Server behaviour:

1. Verify the user can access every referenced budget (`ensureBudgetAccess`).
2. Insert `receipts`.
3. Group items by `budget_id`; unassigned → `catch_all_budget_id`.
4. One `transacts` row per group: `amount = sum(amount_cents + tax_cents) / 100`,
   `credit = false`, `created_at = purchased_at` (falling back to `NOW()`),
   description `"{merchant} — {n} items"`.
5. Insert `receipt_items` linked to both the receipt and its group's `transact_id`.
6. Commit. Return the receipt and affected budget ids for cache invalidation.

Store change: a `CommitReceipt` method doing all of the above inside one `BeginTx`,
with explicit `created_at` rather than the hardcoded `NOW()` at `store.go:412`.

`GET /api/v1/receipts/{id}` for item drill-down is phase 3.

## 8. Frontend changes

All in `frontend/src/pages/Dashboard.tsx` unless noted.

**Capture.** `<input type="file" accept="image/*" capture="environment">` — not
`getUserMedia`. iOS and Android hand back the native camera UI with no permission
dance, no `<video>` element, and no preview plumbing. Requires a secure context, which
passkeys already do.

**The browser only applies EXIF and caps the upload.** Detection, crop, deskew and the
final scale all happen server-side: one implementation to test, no mobile CPU cost, and
direct API callers get the same treatment.

The client bound is 3200px — an upload budget, not the extraction resolution. Shrinking
harder would throw away the detail the server's crop depends on, since the receipt's own
long axis is roughly two thirds of the frame's. A 2.4 MB photo becomes ~1.5 MB, and the
server's crop lands at 737×2048.

**Modal header** gains `📸 Scan receipt` next to the existing title (`Dashboard.tsx:953`).
Scanning fills in the fields the user would otherwise type. Manual entry is untouched.

**`ItemizedLine` extends** with `lineText`, `taxCents`, `suggestionSource`, keeping
`budgetId` / `description` / `amount` so the existing render and edit logic still applies.
The dropdown-per-item UX the user asked for is what this component already does.

**`client.ts` needs a multipart path.** `request()` hardcodes
`'Content-Type': 'application/json'` (`client.ts:57`); when the body is `FormData`, skip
that header so the browser can set its own boundary.

**Reconciliation banner** above the item list when `reconciliation.ok` is false.

**Scan states:** idle → uploading → parsing (spinner + cancel, per D10) → review. Cancel
aborts via `AbortController`.

**Long receipts.** A 40-item list on a phone needs compact rows — the current
`minmax(180px, 1fr)` grid per line is too tall at that count. Rows collapse to
`description / amount / budget` on narrow viewports.

## 9. Failure modes

| Failure | Handling |
|---|---|
| Strix box asleep / unreachable | `502` after short connect timeout → pre-filled manual entry, banner names the cause |
| Inference exceeds timeout | Client `AbortController` + server `RECEIPT_OCR_TIMEOUT_MS` → manual entry |
| Model returns invalid JSON | Schema-constrained decoding should prevent it; on failure, one retry, then manual entry |
| Reconciliation mismatch | Draft returned with the banner; user corrects. Not blocking |
| No date found | `purchased_at` null → falls back to `NOW()`, date field flagged for review |
| Blurry / unreadable photo | Low confidence + failed reconciliation → banner suggests a retake, existing data kept |
| Oversize image | `413`; client downscale should make this unreachable |
| Partial commit | Impossible — single DB transaction (D12) |
| `RECEIPT_OCR_URL` unset | Scan button hidden; manual itemize behaves exactly as today |
| Host runs out of memory | Guarded at the header (3.9); service carries a hard memory limit so the container dies alone |
| **Extraction wrong but balanced** | **Not detected.** See below |

Reconciliation checks arithmetic, not completeness. A single line equal to the
printed subtotal satisfies both checks — items match the subtotal, and items plus
tax match the total — so an extraction that collapsed a six-item receipt into one
bogus line is reported as verified. This is the failure mode llama.cpp's chat
template fixed in practice (3.8), not one the validator can catch.

Since only reconciliation *failures* were logged, such a scan also left no trace at
all. When a production scan was reported as wrong there was no record of how many
items came back, which is the first thing worth knowing. Every scan now logs one
line with its item count and the geometry that produced it:

```
receipt scan ok in 31s: items=4 image=737x2048 cropped=true items_sum=70.66 \
  subtotal=70.66 tax=4.24 total=74.90 evidence=rate basis=marked merchant="Target"
```

Rejecting a suspiciously small item count outright is a candidate follow-up; it
needs a threshold that does not reject the many legitimate single-line receipts.

## 10. Configuration

Added to `internal/config/config.go`:

```
RECEIPT_OCR_URL=            # inference base URL, e.g. http://10.0.10.10:11435
                            # empty disables the feature
RECEIPT_OCR_API=llamacpp    # llamacpp (default) or ollama
RECEIPT_OCR_MODEL=qwen3.8-27b   # llama-server --alias; "qwen3.8:27b" on ollama
RECEIPT_OCR_TOKEN=          # bearer token, if fronted by a reverse proxy
RECEIPT_OCR_TIMEOUT_MS=240000
RECEIPT_OCR_NUM_CTX=32768   # MUST be set high: Ollama truncates silently at ~4096
RECEIPT_MAX_EDGE=2048       # LONG edge. Measured floor -- 1200 misreads prices
RECEIPT_MAX_IMAGE_BYTES=16777216
MEMORY_LIMIT_BYTES=268435456    # Go soft memory limit; 0 disables. See 3.9
```

Exposed to the frontend as a capability flag on an existing response (or a small
`GET /api/v1/capabilities`) so the UI knows whether to render the scan button.

Because the box is on a private network (D1), the Go API needs a route to it —
tailnet, VPN, or LAN. The bearer token is defence in depth, not the primary control;
the endpoint should not be publicly routable.

## 11. Implementation phases

**Phases 0–3 — BUILT AND TESTED.**

- `internal/receipt` — allocation, reconciliation, normalization, Ollama client (52 tests).
- `internal/store/receipt.go` — atomic `CommitReceipt`, history suggestions (10 tests
  against a real Postgres 11, including a rollback-on-inaccessible-budget case).
- `internal/server/handlers/receipts.go` — scan/commit/get plus the capability flag.
- Frontend — camera capture, normalization, review UI, single-request commit
  (15 tests on the payload builder).
- Verified live against Ollama and Postgres: scan returns a reconciled draft, and commit
  writes one transaction per budget (11.32 + 63.58 = 74.90), backdated, balances correct.

**Remaining:** prompt iteration against more real receipts (§3.4), and a 30+ item receipt
to settle §12's latency question.

**Phase 1 — allocation engine.** (Reference implementation validated against the Target
receipt: 424¢ of tax over a 7066¢ base distributes 42/22/180/180 by largest remainder,
summing to exactly 424¢ and 7490¢ total.)
 `internal/receipt`: cents parsing, tax allocation,
reconciliation. Pure functions, no I/O, heavily table-tested — hand-transcribed real
receipts as fixtures. Highest-value tests in the feature; every cent guarantee lives here.

**Phase 2 — server.** Config, inference client, `POST /receipts/scan`, schema tables,
`CommitReceipt`, `POST /receipts`. Handler tests use a fake inference server, following
the `helpers_test.go` pattern.

**Phase 3 — frontend.** Capture, downscale, multipart in `client.ts`, populate the modal,
reconciliation banner, compact rows.

**Phase 4 — suggestions.** `norm_key` + the history lookup. Deliberately last: it needs
committed receipts to have anything to learn from.

**Phase 5 — polish.** Receipt drill-down from a transaction, `GET /receipts/{id}`,
re-parse of a stored `parsed` blob.

## 12. Open risks

- **RETIRED: thinking suppression.** `think: false` is honoured by Ollama 0.32.13 for
  `qwen3.8:27b` — measured `thinking_chars=0` across every call. No need to abandon
  Ollama for `llama-server`. Still keep the inference client behind a small interface,
  since `llama-server --jinja` remains where the throughput wins live (30–36 t/s tuned
  versus ~12 t/s measured here).
- **Latency scales with item count, and long receipts may break D10.** The 4-item
  receipt is 49s end to end, comfortably inside a 60s sync budget. But `eval_count` was
  430 tokens for 4 items — roughly 60 tokens per item over ~190 fixed — so a 40-item
  grocery run projects to ~2600 output tokens and, at the ~12 t/s measured here,
  **3–4 minutes**. The image is not the driver; the item list is. Untested, because no
  40-item receipt was available.
  Mitigations in order of cost: (a) **drop `line_text` from the schema** — the largest
  per-item field, roughly duplicating `description` + `amount` + `marker`, so removing it
  should cut per-item tokens close to half, at the price of the verbatim audit trail and
  the `norm_key` source (derive `norm_key` from `description` instead); (b) tune the
  serving stack toward the 30–36 t/s figures; (c) switch D10 to async.
  Settle this against a real long receipt before Phase 3.
- **Portrait attention collapse is the core failure mode.** Silent, deterministic, and
  invisible without reconciliation. Any change to image normalization must be
  re-validated on a tall receipt, not a corner-store slip.

- **HEIC on iOS.** `accept="image/*"` usually yields JPEG, but this needs testing on a
  real device. A HEIC arriving server-side would need decoding or rejection.
- **Vendor benchmarks.** Qwen3.8-27B's published numbers are Qwen-reported. Phase 0 on
  your own receipts is the only benchmark that counts.
- **Wrapped descriptions.** Long item names wrap to a second physical line on narrow
  receipts. The model should merge them; reconciliation catches it when it doesn't.
- **Non-item lines.** Deposits, bottle fees, loyalty discounts, and store credit vary
  wildly by retailer. Expect prompt iteration driven by real failures.

## 13. Client-supplied extraction (MCP)

D3 says the model reports facts and the server does the arithmetic. Everything downstream of
that line — `Allocate`, the tax basis logic, `Reconciliation` — is a pure function of an
`Extraction`. Nothing in it knows or cares which model filled the struct.

So the extraction step is swappable, and for a caller that has already seen the photo,
swapping it is strictly better. Posting the image back would mean a multi-megabyte base64
round trip over JSON-RPC, the whole `RECEIPT_OCR_TIMEOUT` budget spent, and an answer from a
model chosen for what fits on the box rather than for what reads receipts best.

**The tool boundary is the `Extraction` JSON, not the image.**

- `draft_receipt` takes the extraction, runs the same `Allocate` and reconciliation as
  `POST /receipts/scan`, and returns a draft id.
- `commit_receipt` takes that draft id plus `{position, budget_id}` pairs.

The split between those two is not cosmetic. `POST /api/v1/receipts` accepts full item bodies
including `amount_cents`, because the browser is echoing back numbers this server computed
seconds earlier. Point that at a language model and it re-types every cent on the way back,
with a chance to corrupt each one silently. Holding the allocation server-side and taking only
budget choices on commit means the caller decides *which budget* and never *how much*.

**Reconciliation is what makes this safe at all.** It compares items-sum against printed
subtotal and computed against printed total, in cents. A client that misreads a line fails
that check exactly as the local model does — the validator did not weaken when the eyes
changed, which is the entire reason the Phase 0 confidence field was thrown away in favour of
it. `commit_receipt` refuses a failed draft unless `accept_unreconciled` is set.

The tool's description carries `extractPrompt` verbatim via `receipt.ExtractionRules()`, and
its `inputSchema` is `extractSchema` itself. A second copy of those rules written for MCP
clients would drift from the originals one edit at a time, and each of those rules exists
because its absence produced a specific wrong answer.

`receipts.extraction_source` records which path produced a row: `server_ocr` for this app's own
pipeline (hand entry and hand corrections included), `client_supplied` for an extraction that
arrived already structured. Deliberately not model names — `receipts.model` already holds the
server-side one, and the client-side model is neither observable from here nor stable enough to
be worth a column full of last year's name.
