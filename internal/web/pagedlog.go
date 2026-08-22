package web

import (
	"fmt"
	"html/template"
	"net/url"
)

// pagedLog is the shared paged event card used by the Dashboard's
// recent activity and the Logs page.
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
	Cleared    bool
	EmptyNote  string
	ExtraHref  string
	ExtraLabel string
}

// newPagedLog does the paging arithmetic once, so the two callers can
// disagree about page size and filters but not about how paging works.
func newPagedLog(title, targetID, endpoint string, events []logRowView, total, perPage, page int, extra url.Values) pagedLog {
	pages := (total + perPage - 1) / perPage
	if pages < 1 {
		pages = 1
	}
	if page > pages {
		page = pages
	}
	if page < 1 {
		page = 1
	}

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

func (p pagedLog) String() string { return fmt.Sprintf("pagedLog(%s)", p.Title) }
