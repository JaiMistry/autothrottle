package web

import (
	"html/template"
	"net/url"
)

// pagedLog is the Logs page's paged event card.
type pagedLog struct {
	Title    string
	TargetID string
	Endpoint string
	// Query is appended to every control's URL, already prefixed with
	// "&" so it can follow a parameter; QueryNoAmp is the same without
	// the leading separator, for a URL that starts with it.
	Query      template.URL
	QueryNoAmp template.URL

	Events     []logRowView
	PerPage    int
	PerOptions []int
	Page       int
	Pages      int
	Total      int
	RangeFrom  int
	RangeTo    int
	HasPrev    bool
	HasNext    bool
	PrevPage   int
	NextPage   int
	// ClearEndpoint is where Clear posts to. Empty leaves the button
	// out entirely, for a view that has no business deleting anything.
	// ClearTarget is what that response replaces — the same element the
	// live refresh swaps, never the card itself: the confirmation form
	// lives inside the card, and a swap that removes the element the
	// form sits in leaves htmx with nothing to re-arm the poll on.
	ClearEndpoint string
	ClearTarget   string
	CSRFToken     string
}

// clampPage keeps page within 1..pages, where an empty log is one page.
func clampPage(page, total, perPage int) (int, int) {
	pages := max(1, (total+perPage-1)/perPage)
	return min(max(page, 1), pages), pages
}

func newPagedLog(title, targetID, endpoint string, events []logRowView, total, perPage, page int, extra url.Values) pagedLog {
	page, pages := clampPage(page, total, perPage)

	query := ""
	if encoded := extra.Encode(); encoded != "" {
		query = "&" + encoded
	}

	p := pagedLog{
		Title:      title,
		TargetID:   targetID,
		Endpoint:   endpoint,
		Query:      template.URL(query),
		QueryNoAmp: template.URL(extra.Encode()),
		Events:     events,
		PerPage:    perPage,
		PerOptions: []int{10, 20, 50},
		Page:       page,
		Pages:      pages,
		Total:      total,
		HasPrev:    page > 1,
		HasNext:    page < pages,
		PrevPage:   page - 1,
		NextPage:   page + 1,
	}
	if total > 0 && len(events) > 0 {
		p.RangeFrom = (page-1)*perPage + 1
		p.RangeTo = p.RangeFrom + len(events) - 1
	}
	return p
}
