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

type PullRequestRef struct {
	URL string `json:"url"`
}

type Issue struct {
	Number      int             `json:"number"`
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	State       string          `json:"state"`
	Labels      []Label         `json:"labels"`
	PullRequest *PullRequestRef `json:"pull_request"`
}

type Comment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
	User User   `json:"user"`
}

type PullRequest struct {
	Number int    `json:"number"`
	Merged bool   `json:"merged"`
	State  string `json:"state"`
	Draft  bool   `json:"draft"`
	Base   struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Head struct {
		Ref string `json:"ref"`
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

type ReviewThread struct {
	ID        int64           `json:"id"`
	Path      string          `json:"path"`
	Line      *int            `json:"line"`
	StartLine *int            `json:"start_line"`
	Comments  []ReviewComment `json:"comments"`
}

type PullRequestReviewThreadEvent struct {
	Action      string       `json:"action"`
	Thread      ReviewThread `json:"thread"`
	PullRequest PullRequest  `json:"pull_request"`
	Repository  Repository   `json:"repository"`
	Sender      User         `json:"sender"`
}

type PullRequestEvent struct {
	Action      string      `json:"action"`
	PullRequest PullRequest `json:"pull_request"`
	Repository  Repository  `json:"repository"`
	Sender      User        `json:"sender"`
}

type CheckPullRequest struct {
	Number int `json:"number"`
	Base   struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Head struct {
		Ref string `json:"ref"`
	} `json:"head"`
}

type CheckOutput struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Text    string `json:"text"`
}

type CheckSuiteRef struct {
	HeadBranch string `json:"head_branch"`
	HeadSHA    string `json:"head_sha"`
}

type CheckRun struct {
	Name         string             `json:"name"`
	Status       string             `json:"status"`
	Conclusion   string             `json:"conclusion"`
	HeadSHA      string             `json:"head_sha"`
	HTMLURL      string             `json:"html_url"`
	DetailsURL   string             `json:"details_url"`
	Output       CheckOutput        `json:"output"`
	CheckSuite   CheckSuiteRef      `json:"check_suite"`
	PullRequests []CheckPullRequest `json:"pull_requests"`
}

type CheckSuite struct {
	Status       string             `json:"status"`
	Conclusion   string             `json:"conclusion"`
	HeadBranch   string             `json:"head_branch"`
	HeadSHA      string             `json:"head_sha"`
	PullRequests []CheckPullRequest `json:"pull_requests"`
}

type CheckRunEvent struct {
	Action     string     `json:"action"`
	CheckRun   CheckRun   `json:"check_run"`
	Repository Repository `json:"repository"`
	Sender     User       `json:"sender"`
}

type CheckSuiteEvent struct {
	Action     string     `json:"action"`
	CheckSuite CheckSuite `json:"check_suite"`
	Repository Repository `json:"repository"`
	Sender     User       `json:"sender"`
}
