package gotato

import (
	"context"
	"math"
	"slices"
	"sort"
	"strings"
	"unicode"
)

// bm25Searcher is the built-in ToolSearcher: BM25 over each Tool's ID and
// description. A term in the ID counts three times as much as one in the
// description.
type bm25Searcher struct{}

const (
	bm25K1       = 1.2
	bm25B        = 0.75
	bm25IDWeight = 3
)

func (bm25Searcher) Search(_ context.Context, query string, tools []ToolSpec) ([]string, error) {
	terms := uniqueTerms(tokenize(query))
	if len(terms) == 0 || len(tools) == 0 {
		return nil, nil
	}
	docs, average := bm25Documents(tools)
	if average == 0 {
		return nil, nil
	}
	scores := make([]float64, len(docs))
	for _, term := range terms {
		bm25AddTerm(term, docs, average, scores)
	}
	return bm25Ranked(docs, scores), nil
}

// bm25Document is one Tool as BM25 sees it: its ID, its weighted term counts
// and its weighted length.
type bm25Document struct {
	id     string
	counts map[string]float64
	length float64
}

// bm25Documents counts the terms of every Tool, and returns them with their
// average length.
func bm25Documents(tools []ToolSpec) ([]bm25Document, float64) {
	docs := make([]bm25Document, len(tools))
	var total float64
	for i, spec := range tools {
		doc := bm25Document{id: spec.ID, counts: map[string]float64{}}
		for _, term := range tokenize(spec.ID) {
			doc.counts[term] += bm25IDWeight
			doc.length += bm25IDWeight
		}
		for _, term := range tokenize(spec.Description) {
			doc.counts[term]++
			doc.length++
		}
		total += doc.length
		docs[i] = doc
	}
	return docs, total / float64(len(docs))
}

// bm25AddTerm adds the score of one query term to the score of every document
// that holds it.
func bm25AddTerm(term string, docs []bm25Document, average float64, scores []float64) {
	n := float64(len(docs))
	containing := 0.0
	for _, doc := range docs {
		if doc.counts[term] > 0 {
			containing++
		}
	}
	if containing == 0 {
		return
	}
	idf := math.Log(1 + (n-containing+0.5)/(containing+0.5))
	for i, doc := range docs {
		tf := doc.counts[term]
		if tf == 0 {
			continue
		}
		scores[i] += idf * tf * (bm25K1 + 1) / (tf + bm25K1*(1-bm25B+bm25B*doc.length/average))
	}
}

// bm25Ranked returns the IDs of the documents that scored, best first, ties
// by ID.
func bm25Ranked(docs []bm25Document, scores []float64) []string {
	order := make([]int, 0, len(docs))
	for i, score := range scores {
		if score > 0 {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(a, b int) bool {
		if scores[order[a]] != scores[order[b]] {
			return scores[order[a]] > scores[order[b]]
		}
		return docs[order[a]].id < docs[order[b]].id
	})
	out := make([]string, len(order))
	for i, index := range order {
		out[i] = docs[index].id
	}
	return out
}

// tokenize lower-cases text and splits it at every character that is not a
// letter or a digit, so "orders.list_open" is "orders", "list", "open".
func tokenize(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func uniqueTerms(terms []string) []string {
	var out []string
	for _, term := range terms {
		if !slices.Contains(out, term) {
			out = append(out, term)
		}
	}
	return out
}
