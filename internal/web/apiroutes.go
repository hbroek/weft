package web

import "net/http"

// An apiRoute is one address of the REST API and the OAuth scope a token needs for it. The account's own API token
// is not limited by scopes. A route that is not in this table is not reachable with an OAuth token at all.
type apiRoute struct {
	scope, route string
	h            func(http.ResponseWriter, *http.Request, *req)
}

func apiRoutes(s *Server) []apiRoute {
	return []apiRoute{
		{"post", "POST /api/media", s.apiMediaUpload},
		{"post", "POST /api/avatar", s.apiAvatar},
		{"like", "POST /api/likes", s.apiLike},
		{"like", "DELETE /api/likes", s.apiUnlike},
		{"read", "GET /api/likes/received", s.apiLikesReceived},
		{"read", "GET /api/replies", s.apiReplies},
		{"moderate", "POST /api/replies/{id}/status", s.apiModerate},
		{"read", "GET /api/thread", s.apiThread},
		{"read", "GET /api/timeline", s.apiTimeline},
		{"read", "GET /api/following", s.apiFollowing},
		{"follow", "POST /api/following", s.apiFollow},
		{"follow", "DELETE /api/following", s.apiUnfollow},
		{"read", "GET /api/posts", s.apiListPosts},
		{"post", "POST /api/posts", s.apiCreate},
		{"read", "GET /api/posts/{ref}", s.apiGetPost},
		{"post", "PUT /api/posts/{ref}", s.apiUpdate},
		{"post", "DELETE /api/posts/{ref}", s.apiDelete},
		{"post", "PUT /posts/{slug}", s.putPost},
	}
}
