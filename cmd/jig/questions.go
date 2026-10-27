package main

import (
	"cmp"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kinged007/jigz/internal/collector"
	"github.com/kinged007/jigz/internal/config"
	"github.com/kinged007/jigz/internal/store"
)

func itemKey(project string, n int) string { return project + "#" + strconv.Itoa(n) }

// questionRow is one question on the page. Suggested is the initial text
// of the answer box. OpenURL is the question's own page on the tracker.
type questionRow struct {
	store.Question
	Title       string
	URL         string
	OpenURL     string
	TrackerName string
	FromBody    bool
}

// questionItem is a scanned item with problems.
type questionItem struct {
	store.QuestionItem
	Problems  []collector.ItemProblem
	Malformed bool
}

type questionsView struct {
	pageData
	On bool
	// Projects are the projects the scanner covers. With one, the project
	// filter is fixed to it.
	Projects  []string
	Single    bool
	Active    bool
	Notices   []string
	NextIn    int
	Filter    store.QuestionFilter
	Questions []questionRow
	Items     []questionItem
}

// questionFilter reads the page's filters. Unknown values are dropped, and
// the state defaults to open.
func questionFilter(q url.Values) store.QuestionFilter {
	f := store.QuestionFilter{Project: q.Get("project"), State: "open"}
	if v := q.Get("audience"); v == "product" || v == "project" {
		f.Audience = v
	}
	if v := q.Get("blocking"); v == "true" || v == "false" {
		f.Blocking = v
	}
	if q.Get("state") == "answered" {
		f.State = "answered"
	}
	return f
}

// projectQuestionsOn reports whether github.questions is on for a project.
// A project whose config cannot be read uses the app-level block.
func (a *app) projectQuestionsOn(project string) bool {
	for _, r := range a.projects() {
		if r.Repo.Repo != project {
			continue
		}
		if g, err := config.LoadGitHub(r.Dir); err == nil {
			return g.QuestionsOn()
		}
		break
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.github.QuestionsOn()
}

// questionsOn reports whether the scanner and the page are on: the store is
// open and at least one registered project has github.questions on.
func (a *app) questionsOn() bool {
	if a.questions == nil {
		return false
	}
	ps := a.projects()
	if len(ps) == 0 {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.github.QuestionsOn()
	}
	for _, r := range ps {
		if a.projectQuestionsOn(r.Repo.Repo) {
			return true
		}
	}
	return false
}

// trackerName names the tracker that serves rawURL.
func trackerName(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "issue tracker"
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case strings.Contains(host, "github"):
		return "GitHub"
	case strings.Contains(host, "gitlab"):
		return "GitLab"
	}
	return "issue tracker"
}

// openURL is the page of question q on the tracker: its comment, or the
// item when the question is in the description. The comment's own URL is
// used as the tracker returned it; the comment anchor is added only when
// that URL has none.
func openURL(q store.Question, itemURL string) string {
	if q.AskedCommentID == 0 {
		return cmp.Or(itemURL, q.AskedCommentURL)
	}
	u := cmp.Or(q.AskedCommentURL, itemURL)
	if u == "" || strings.Contains(u, "#") {
		return u
	}
	return u + "#issuecomment-" + strconv.FormatInt(q.AskedCommentID, 10)
}

// questionsData collects the questions and problem items for f.
func (a *app) questionsData(f store.QuestionFilter) ([]questionRow, []questionItem, error) {
	if !a.questionsOn() || a.store == nil {
		return nil, nil, nil
	}
	items, err := a.questions.Items()
	if err != nil {
		return nil, nil, err
	}
	byKey := map[string]store.QuestionItem{}
	var withProblems []questionItem
	for _, it := range items {
		byKey[itemKey(it.Project, it.Number)] = it
		ps := collector.Problems(it)
		if len(ps) == 0 || f.Project != "" && it.Project != f.Project {
			continue
		}
		withProblems = append(withProblems, questionItem{QuestionItem: it, Problems: ps, Malformed: collector.Malformed(ps)})
	}
	qs, err := a.store.Questions(f)
	if err != nil {
		return nil, nil, err
	}
	rows := make([]questionRow, 0, len(qs))
	for _, q := range qs {
		row := questionRow{Question: q, FromBody: q.AskedCommentID == 0}
		if q.IgnoredAnswers == nil {
			row.IgnoredAnswers = []store.IgnoredAnswer{}
		}
		if it, ok := byKey[itemKey(q.Project, q.Number)]; ok {
			row.Title, row.URL = it.Title, it.URL
		} else if it, err := a.store.QuestionItem(q.Project, q.Number); err == nil {
			row.Title, row.URL = it.Title, it.URL
		}
		row.OpenURL, row.TrackerName = openURL(q, row.URL), trackerName(cmp.Or(row.URL, q.AskedCommentURL))
		rows = append(rows, row)
	}
	return rows, withProblems, nil
}

// scanState lists the projects the scanner covers, the problems of its last
// pass as short notices, and the seconds until the next pass (-1 when no
// pass has run). A pass that is overdue is reported as due in the time
// between two checks of the scanner.
func (a *app) scanState() (projects, notices []string, nextIn int) {
	for _, t := range a.questions.Targets() {
		if !a.projectQuestionsOn(t.Project) {
			continue
		}
		projects = append(projects, t.Project)
		st := a.questions.Status(t.Project)
		switch {
		case t.Token == "":
			notices = append(notices, t.Project+": no GitHub token")
		case !st.At.IsZero() && !st.OK:
			notices = append(notices, t.Project+": last scan failed: "+st.Err)
		}
	}
	slices.Sort(projects)
	next := a.questions.NextPass()
	if next.IsZero() {
		return projects, notices, -1
	}
	return projects, notices, max(int(time.Until(next).Seconds()), scanCheck)
}

// scanCheck is how often the scanner checks whether a pass is due, in
// seconds.
const scanCheck = 15

func (a *app) questionsPage(w http.ResponseWriter, r *http.Request) {
	v := questionsView{pageData: a.dataFor(r, "", ""), On: a.questionsOn(), Filter: questionFilter(r.URL.Query())}
	v.Nav, v.Title = "questions", "Questions"
	if v.On {
		v.Projects, v.Notices, v.NextIn = a.scanState()
		v.Single = len(v.Projects) == 1
		switch {
		case v.Single:
			v.Filter.Project = v.Projects[0]
		case !slices.Contains(v.Projects, v.Filter.Project):
			v.Filter.Project = ""
		}
		f := v.Filter
		v.Active = f.Audience != "" || f.Blocking != "" || f.State != "open" || (!v.Single && f.Project != "")
		var err error
		if v.Questions, v.Items, err = a.questionsData(v.Filter); err != nil {
			a.fail(w, "questions", err)
			return
		}
	}
	execPage(w, "questions", v)
}
