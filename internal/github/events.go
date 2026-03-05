package github

type Repository struct {
	FullName string `json:"full_name"`
}

type User struct {
	Login string `json:"login"`
}

type Label struct {
	Name string `json:"name"`
}

type Issue struct {
	Number      int         `json:"number"`
	Title       string      `json:"title"`
	Body        string      `json:"body"`
	Labels      []Label     `json:"labels"`
	PullRequest interface{} `json:"pull_request"`
}

type Comment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
	User User   `json:"user"`
}

type PullRequest struct {
	Number int  `json:"number"`
	Merged bool `json:"merged"`
	Base   struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Head struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
}

type Review struct {
	ID    int64  `json:"id"`
	Body  string `json:"body"`
	State string `json:"state"`
	User  User   `json:"user"`
}

type ReviewComment struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	Path      string `json:"path"`
	Line      *int   `json:"line"`
	StartLine *int   `json:"start_line"`
	User      User   `json:"user"`
}

type IssuesEvent struct {
	Action     string     `json:"action"`
	Label      Label      `json:"label"`
	Issue      Issue      `json:"issue"`
	Repository Repository `json:"repository"`
	Sender     User       `json:"sender"`
}

type IssueCommentEvent struct {
	Action     string              `json:"action"`
	Issue      Issue               `json:"issue"`
	Comment    Comment             `json:"comment"`
	Changes    IssueCommentChanges `json:"changes"`
	Repository Repository          `json:"repository"`
	Sender     User                `json:"sender"`
}

type IssueCommentChanges struct {
	Body *IssueCommentBodyChange `json:"body"`
}

type IssueCommentBodyChange struct {
	From string `json:"from"`
}

type PullRequestReviewEvent struct {
	Action      string      `json:"action"`
	Review      Review      `json:"review"`
	PullRequest PullRequest `json:"pull_request"`
	Repository  Repository  `json:"repository"`
	Sender      User        `json:"sender"`
}

type PullRequestReviewCommentEvent struct {
	Action      string               `json:"action"`
	Comment     ReviewComment        `json:"comment"`
	Changes     ReviewCommentChanges `json:"changes"`
	PullRequest PullRequest          `json:"pull_request"`
	Repository  Repository           `json:"repository"`
	Sender      User                 `json:"sender"`
}

type ReviewCommentChanges struct {
	Body *IssueCommentBodyChange `json:"body"`
}

type PullRequestEvent struct {
	Action      string      `json:"action"`
	PullRequest PullRequest `json:"pull_request"`
	Repository  Repository  `json:"repository"`
	Sender      User        `json:"sender"`
}
