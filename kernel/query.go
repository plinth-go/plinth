package kernel

import (
	"errors"
	"net/url"
	"strings"
)

const (
	inlineQueryValueLimit = 8
	maxDefaultQueryParams = 10000
)

type queryValue struct {
	first string
	all   []string
	count int
}

func (v *queryValue) add(value string) {
	switch v.count {
	case 0:
		v.first = value
	case 1:
		v.all = []string{v.first, value}
	default:
		v.all = append(v.all, value)
	}
	v.count++
}

// scanQuery mirrors url.ParseQuery for queries within the standard library's
// default parameter limit while storing only parameters declared by the route.
// The caller falls back to url.ParseQuery for larger queries. A lower
// GODEBUG=urlmaxqueryparams setting is not observed on this fast path.
func scanQuery(raw string, index map[string]int, values []queryValue) error {
	for raw != "" {
		segment, rest, _ := strings.Cut(raw, "&")
		raw = rest
		if strings.Contains(segment, ";") {
			return errors.New("invalid semicolon separator in query")
		}
		if segment == "" {
			continue
		}
		key, value, _ := strings.Cut(segment, "=")
		key, err := url.QueryUnescape(key)
		if err != nil {
			return err
		}
		value, err = url.QueryUnescape(value)
		if err != nil {
			return err
		}
		if slot, ok := index[key]; ok {
			values[slot].add(value)
		}
	}
	return nil
}
