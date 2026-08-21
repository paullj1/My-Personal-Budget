package receipt

import (
	"fmt"
	"sort"
	"strings"
)

// baseTolerance absorbs the rounding slack in a printed tax base. A receipt
// printing "6.00000 on $70.66" implies 70.6666..., so an exact match is not
// available even when the receipt is perfectly consistent.
const baseTolerance = 2

// Allocate distributes tax and unattached discounts across the extracted items
// and reports whether the result reconciles with the printed totals.
//
// The guarantee: sum(line.TotalCents) == sum(items) + tax + adjustments,
// exactly, with no cent lost to rounding. Whether that equals the printed total
// is a separate question, answered by Reconciliation.
func Allocate(ex Extraction) Allocation {
	lines := make([]Line, 0, len(ex.Items))
	for i, it := range ex.Items {
		pos := it.Position
		if pos == 0 {
			pos = i + 1
		}
		marker := ""
		if it.Marker != nil {
			marker = strings.TrimSpace(*it.Marker)
		}
		desc := strings.TrimSpace(it.Description)
		if desc == "" {
			desc = strings.TrimSpace(it.LineText)
		}
		// Prefer line_text for the match key: it carries the retailer's stable
		// abbreviation, where description may be a model paraphrase.
		keySrc := it.LineText
		if strings.TrimSpace(keySrc) == "" {
			keySrc = desc
		}
		lines = append(lines, Line{
			Position:    pos,
			LineText:    strings.TrimSpace(it.LineText),
			NormKey:     NormKey(keySrc),
			Description: desc,
			Marker:      marker,
			Taxable:     it.Taxable,
			AmountCents: Cents(it.Amount),
		})
	}

	alloc := Allocation{
		Lines:         lines,
		SubtotalCents: centsPtr(ex.Subtotal),
		TotalCents:    centsPtr(ex.Total),
		TaxEvidence:   ex.TaxEvidence,
		TaxBasis:      BasisNoTax,
		Currency:      "USD",
		Notes:         append([]string(nil), ex.Notes...),
		PurchasedAt:   ParsePurchasedAt(ex.PurchasedAt),
	}
	if ex.Merchant != nil {
		alloc.Merchant = strings.TrimSpace(*ex.Merchant)
	}
	if ex.Currency != nil && strings.TrimSpace(*ex.Currency) != "" {
		alloc.Currency = strings.ToUpper(strings.TrimSpace(*ex.Currency))
	}

	itemsSum := 0
	for _, l := range lines {
		itemsSum += l.AmountCents
	}

	// Attached discounts reduce their own line; unattached ones prorate below.
	var floatingAdjust int
	byPosition := make(map[int]int, len(lines))
	for i, l := range lines {
		byPosition[l.Position] = i
	}
	for _, adj := range ex.Adjustments {
		c := Cents(adj.Amount)
		if c == 0 {
			continue
		}
		if adj.AppliesToPosition != nil {
			if idx, ok := byPosition[*adj.AppliesToPosition]; ok {
				lines[idx].AdjustCents += c
				alloc.AdjustCents += c
				continue
			}
		}
		floatingAdjust += c
	}
	if floatingAdjust != 0 && itemsSum != 0 {
		weights := make([]int, len(lines))
		for i, l := range lines {
			weights[i] = l.AmountCents
		}
		for i, share := range largestRemainder(floatingAdjust, weights) {
			lines[i].AdjustCents += share
		}
		alloc.AdjustCents += floatingAdjust
	}

	// Each tax line is allocated over its own taxable set, so a receipt with
	// separate food and general-merchandise rates lands correctly.
	for _, tl := range ex.TaxLines {
		taxCents := Cents(tl.Amount)
		alloc.TaxCents += taxCents
		if taxCents == 0 {
			continue
		}
		idxs, basis := taxableSet(lines, itemsSum, ex.TaxEvidence, tl)
		// With several tax lines the bases can differ, and keeping only the last
		// one persists a description that misstates how the rest was allocated.
		if alloc.TaxBasis == BasisNoTax || alloc.TaxBasis == basis {
			alloc.TaxBasis = basis
		} else {
			alloc.TaxBasis = BasisMixed
		}
		weights := make([]int, len(idxs))
		base := 0
		for i, idx := range idxs {
			weights[i] = lines[idx].AmountCents
			base += weights[i]
		}
		if base == 0 {
			// Nothing to prorate against (zero-priced items only). Park the tax
			// on the first line rather than silently dropping cents.
			if len(lines) > 0 {
				lines[0].TaxCents += taxCents
			}
			continue
		}
		for i, share := range largestRemainder(taxCents, weights) {
			lines[idxs[i]].TaxCents += share
		}
	}

	for i := range lines {
		lines[i].TotalCents = lines[i].AmountCents + lines[i].TaxCents + lines[i].AdjustCents
	}
	alloc.Lines = lines
	alloc.Reconciliation = reconcile(itemsSum, alloc)
	return alloc
}

// taxableSet picks which items a tax line applies to, strongest evidence first.
// A printed base beats every marker heuristic: a Target receipt reading
// "MD TAX 6.00000 on $70.66" proves all items were taxed even though two carry
// "TF" and two carry "T".
func taxableSet(lines []Line, itemsSum int, evidence string, tl ExTaxLine) ([]int, string) {
	all := make([]int, len(lines))
	for i := range lines {
		all[i] = i
	}
	marked := make([]int, 0, len(lines))
	markedSum := 0
	for i, l := range lines {
		if l.Taxable != nil && *l.Taxable {
			marked = append(marked, i)
			markedSum += l.AmountCents
		}
	}

	if tl.Base != nil {
		base := Cents(*tl.Base)
		if abs(base-itemsSum) <= baseTolerance {
			return all, BasisPrintedBaseAll
		}
		if len(marked) > 0 && abs(base-markedSum) <= baseTolerance {
			return marked, BasisPrintedBaseSubset
		}
		// Base matches neither set; markers are still better than nothing.
		if len(marked) > 0 {
			return marked, BasisPrintedBaseSubset
		}
		return all, BasisPrintedBaseAll
	}

	if evidence == EvidencePerLineFlags && len(marked) > 0 {
		return marked, BasisMarkers
	}

	// No printed base: derive one from the rate and see which set it matches.
	if tl.Rate != nil && *tl.Rate > 0 {
		implied := Cents(tl.Amount / *tl.Rate)
		if abs(implied-itemsSum) <= baseTolerance {
			return all, BasisImpliedRate
		}
		if len(marked) > 0 && abs(implied-markedSum) <= baseTolerance {
			return marked, BasisImpliedRate
		}
	}

	if len(marked) > 0 {
		return marked, BasisMarkers
	}
	// Nothing to go on: spread across everything so no cent is orphaned.
	return all, BasisAllItems
}

// reconcile runs the two independent checks that gate the fallback to manual
// entry: items must equal the printed subtotal, and subtotal plus tax plus
// adjustments must equal the printed total.
func reconcile(itemsSum int, a Allocation) Reconciliation {
	// Plenty of receipts -- coffee, fuel, fast food -- print a TOTAL and no
	// SUBTOTAL line. Treating a missing subtotal as zero made every one of them
	// fail reconciliation and drop the user into manual entry for no reason, so
	// fall back to the item sum and only compare against the subtotal when the
	// receipt actually printed one.
	effectiveSubtotal := a.SubtotalCents
	if effectiveSubtotal == 0 && itemsSum != 0 {
		effectiveSubtotal = itemsSum
	}

	r := Reconciliation{
		ItemsSumCents: itemsSum,
		SubtotalCents: a.SubtotalCents,
		PrintedCents:  a.TotalCents,
		ComputedCents: effectiveSubtotal + a.TaxCents + a.AdjustCents,
	}
	r.ItemsDeltaCents = itemsSum - a.SubtotalCents
	r.TotalDeltaCents = r.ComputedCents - r.PrintedCents

	var problems []string
	if a.SubtotalCents == 0 && a.TotalCents == 0 {
		problems = append(problems, "receipt has no readable subtotal or total")
	} else {
		if a.SubtotalCents != 0 && r.ItemsDeltaCents != 0 {
			problems = append(problems, fmt.Sprintf("items sum to %s but the subtotal reads %s",
				money(itemsSum), money(a.SubtotalCents)))
		}
		if a.TotalCents != 0 && r.TotalDeltaCents != 0 {
			problems = append(problems, fmt.Sprintf("items plus tax come to %s but the total reads %s",
				money(r.ComputedCents), money(r.PrintedCents)))
		}
	}
	if len(problems) == 0 {
		r.OK = true
		return r
	}
	r.Message = strings.Join(problems, "; ") + "."
	return r
}

// largestRemainder splits amount across weights so the parts sum to exactly
// amount. Leftover cents go to the largest fractional remainders, ties broken by
// index, so the result is deterministic for a given input.
func largestRemainder(amount int, weights []int) []int {
	out := make([]int, len(weights))
	total := 0
	for _, w := range weights {
		total += w
	}
	if total == 0 || len(weights) == 0 {
		return out
	}

	// Work on the magnitude so discounts distribute like charges.
	sign := 1
	if amount < 0 {
		sign, amount = -1, -amount
	}

	type rem struct {
		idx int
		val int
	}
	rems := make([]rem, len(weights))
	assigned := 0
	for i, w := range weights {
		exact := amount * w
		out[i] = exact / total
		rems[i] = rem{idx: i, val: exact % total}
		assigned += out[i]
	}

	left := amount - assigned
	sort.SliceStable(rems, func(i, j int) bool {
		if rems[i].val != rems[j].val {
			return rems[i].val > rems[j].val
		}
		return rems[i].idx < rems[j].idx
	})
	for i := 0; i < left && i < len(rems); i++ {
		out[rems[i].idx]++
	}

	if sign < 0 {
		for i := range out {
			out[i] = -out[i]
		}
	}
	return out
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func money(cents int) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}
