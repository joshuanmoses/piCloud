package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/nodes"
	"github.com/joshuanmoses/piCloud/backend/internal/search"
)

// handleSearch finds files and folders by name.
//
// Visibility is enforced inside the query, not here. That is deliberate: a
// handler that fetched matches and then filtered them would report a total that
// counted other people's files, and would leak their existence through
// pagination even while hiding their names.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	user := MustUser(r.Context())
	query := r.URL.Query()

	text := strings.TrimSpace(query.Get("q"))
	if len([]rune(text)) < search.MinQueryLength {
		// Not an error. An empty search box is a normal state, and returning
		// 400 for it would make the interface flash a message as the user
		// types the first character.
		WriteJSON(w, r, http.StatusOK, searchResponse{
			Results: []searchResultResponse{},
			Query:   text,
		})
		return
	}

	q := search.Query{
		Text:      text,
		UserID:    user.ID,
		Limit:     parseIntDefault(query.Get("limit"), search.DefaultLimit),
		Offset:    parseIntDefault(query.Get("offset"), 0),
		OwnedByMe: query.Get("owned_by_me") == "1" || strings.EqualFold(query.Get("owned_by_me"), "true"),
	}

	if k := nodes.Kind(query.Get("kind")); k.Valid() {
		q.Kind = k
	}
	if mime := strings.TrimSpace(query.Get("type")); mime != "" {
		// A category such as "image" is expanded to the MIME prefix the
		// interface means by it, so the browser does not have to know that
		// "documents" is six different types.
		q.MimePrefix = mimePrefixFor(mime)
	}
	if raw := query.Get("in_folder"); raw != "" {
		folderID, err := uuid.Parse(raw)
		if err != nil {
			WriteError(w, r, ErrBadRequest("That folder identifier is not valid."))
			return
		}
		// Confirm the caller can see the folder before scoping to it.
		// Otherwise a search scoped to a stranger's folder would report zero
		// results for a folder that exists and zero for one that does not —
		// identical, but only by accident. Checking makes it deliberate.
		if _, apiErr := s.requireAccess(r, folderID, permissionRead(), false); apiErr != nil {
			WriteError(w, r, apiErr)
			return
		}
		q.InFolder = &folderID
	}
	if after, ok := parseTimeParam(query.Get("modified_after")); ok {
		q.ModifiedAfter = &after
	}
	if before, ok := parseTimeParam(query.Get("modified_before")); ok {
		q.ModifiedBefore = &before
	}
	if v, err := strconv.ParseInt(query.Get("min_size"), 10, 64); err == nil && v >= 0 {
		q.MinSize = &v
	}
	if v, err := strconv.ParseInt(query.Get("max_size"), 10, 64); err == nil && v >= 0 {
		q.MaxSize = &v
	}
	if tags := query["tag"]; len(tags) > 0 {
		q.Tags = normalizeTagFilter(tags)
	}

	results, err := s.search.Search(r.Context(), q)
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}

	// The same decoration as a folder listing: owners, effective permissions,
	// favourite state, thumbnail availability, and the caller's tags — all
	// batched. A search result that showed none of those would be visibly
	// poorer than the same file one click away in its folder.
	matched := make([]*nodes.Node, 0, len(results.Results))
	for i := range results.Results {
		matched = append(matched, results.Results[i].Node)
	}
	decorated, err := s.decorateNodes(r, matched)
	if err != nil {
		WriteError(w, r, err)
		return
	}

	items := make([]searchResultResponse, 0, len(decorated))
	for i := range decorated {
		item := searchResultResponse{nodeResponse: decorated[i]}
		if i < len(results.Results) {
			item.Score = results.Results[i].Score
		}
		items = append(items, item)
	}

	WriteJSON(w, r, http.StatusOK, searchResponse{
		Results: items,
		Total:   results.Total,
		Query:   text,
		Limit:   q.Limit,
		Offset:  q.Offset,
	})
}

// searchResponse is the body of GET /search.
type searchResponse struct {
	Results []searchResultResponse `json:"results"`
	Total   int                    `json:"total"`
	Query   string                 `json:"query"`
	Limit   int                    `json:"limit,omitempty"`
	Offset  int                    `json:"offset,omitempty"`
}

// searchResultResponse is a node plus its relevance.
type searchResultResponse struct {
	nodeResponse
	// Score is the trigram similarity, exposed so the interface can show a
	// relevance indicator. It is not a security-relevant value.
	Score float64 `json:"score"`
}

// mimePrefixFor translates a friendly category into a MIME prefix.
//
// The interface offers categories, not MIME types, because "image" is what a
// person means and "image/" is what the database matches. An unrecognised value
// is passed through, so a caller may still filter by a literal prefix.
func mimePrefixFor(category string) string {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "image", "images", "photo", "photos":
		return "image/"
	case "video", "videos":
		return "video/"
	case "audio", "music":
		return "audio/"
	case "pdf":
		return "application/pdf"
	case "text":
		return "text/"
	}
	return category
}

// parseTimeParam accepts RFC 3339, with or without the time component.
func parseTimeParam(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, true
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// normalizeTagFilter trims and bounds the tag filter.
func normalizeTagFilter(raw []string) []string {
	const maxTagFilters = 10
	out := make([]string, 0, len(raw))
	for _, t := range raw {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		out = append(out, t)
		if len(out) == maxTagFilters {
			break
		}
	}
	return out
}

// searchEnabled reports whether the search backend was supplied.
func (s *Server) searchEnabled() bool { return s.search != nil && s.filesEnabled() }
