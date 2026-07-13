package awxfacade

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// envelope is AWX's DRF list wrapper. next/previous are absolute URLs (awxkit
// follows them verbatim) or null.
type envelope struct {
	Count    int     `json:"count"`
	Next     *string `json:"next"`
	Previous *string `json:"previous"`
	Results  []any   `json:"results"`
}

const (
	defaultPageSize = 25
	maxPageSize     = 200
)

// named is anything the façade lists that has a filterable/sortable name+id.
type named struct {
	id   int64
	name string
	obj  any
}

// paginate applies AWX filters (name / name__icontains / name__in), order_by,
// then page/page_size, and renders the envelope with absolute next/previous
// built from the incoming request URL.
func paginate(r *http.Request, items []named) envelope {
	q := r.URL.Query()

	// Filter.
	if v := q.Get("name"); v != "" {
		items = filter(items, func(n named) bool { return n.name == v })
	}
	if v := q.Get("name__icontains"); v != "" {
		lv := strings.ToLower(v)
		items = filter(items, func(n named) bool { return strings.Contains(strings.ToLower(n.name), lv) })
	}
	if v := q.Get("name__in"); v != "" {
		set := map[string]bool{}
		for _, s := range strings.Split(v, ",") {
			set[s] = true
		}
		items = filter(items, func(n named) bool { return set[n.name] })
	}

	// Order (default: name asc). "-" prefix = desc. Supports name and id.
	orderBy := q.Get("order_by")
	desc := strings.HasPrefix(orderBy, "-")
	field := strings.TrimPrefix(orderBy, "-")
	sort.SliceStable(items, func(i, j int) bool {
		less := items[i].name < items[j].name
		if field == "id" {
			less = items[i].id < items[j].id
		}
		if desc {
			return !less
		}
		return less
	})

	count := len(items)
	page := atoiDefault(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	size := atoiDefault(q.Get("page_size"), defaultPageSize)
	if size < 1 {
		size = defaultPageSize
	}
	if size > maxPageSize {
		size = maxPageSize
	}

	start := (page - 1) * size
	if start > count {
		start = count
	}
	end := start + size
	if end > count {
		end = count
	}
	results := make([]any, 0, end-start)
	for _, n := range items[start:end] {
		results = append(results, n.obj)
	}

	env := envelope{Count: count, Results: results}
	if end < count {
		env.Next = pageURL(r, page+1)
	}
	if page > 1 && start < count {
		env.Previous = pageURL(r, page-1)
	}
	return env
}

func filter(items []named, keep func(named) bool) []named {
	out := items[:0:0]
	for _, n := range items {
		if keep(n) {
			out = append(out, n)
		}
	}
	return out
}

// pageURL renders an absolute URL for the given page, preserving other query
// params. Scheme/host come from the request (X-Forwarded-* honored) so awxkit
// can re-fetch it.
func pageURL(r *http.Request, page int) *string {
	u := url.URL{Scheme: scheme(r), Host: reqHost(r), Path: r.URL.Path}
	q := r.URL.Query()
	q.Set("page", strconv.Itoa(page))
	u.RawQuery = q.Encode()
	s := u.String()
	return &s
}

func scheme(r *http.Request) string {
	if s := r.Header.Get("X-Forwarded-Proto"); s != "" {
		return s
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func reqHost(r *http.Request) string {
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		return h
	}
	return r.Host
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
