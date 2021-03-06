package main

import (
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	lib "devstats"

	"github.com/google/go-github/github"
)

type issueConfig struct {
	repo        string
	number      int
	issueID     int64
	pr          bool
	milestoneID *int64
	labels      string
	labelsMap   map[int64]string
	ghIssue     *github.Issue
}

// handlePossibleError - display error specific message, detect rate limit and abuse
func handlePossibleError(err error, cfg *issueConfig, info string) {
	if err != nil {
		_, rate := err.(*github.RateLimitError)
		_, abuse := err.(*github.AbuseRateLimitError)
		if abuse || rate {
			lib.Printf("Hit rate limit (%s) for %v\n", info, cfg)
		}
		lib.FatalOnError(err)
	}
}

// milestonesEvent - create artificial 'ArtificialEvent'
// creates new issue state, artificial event and its payload
func artificialEvent(
	c *sql.DB,
	ctx *lib.Ctx,
	iid, eid int64,
	milestone string,
	labels map[int64]string,
	labelsChanged bool,
	ghIssue *github.Issue,
) (err error) {
	if ctx.SkipPDB {
		if ctx.Debug > 0 {
			lib.Printf("Skipping write for issue_id: %d, event_id: %d, milestone_id: %s, labels(%v): %v\n", iid, eid, milestone, labelsChanged, labels)
		}
		return nil
	}
	// Create artificial event, add 2^48 to eid
	eventID := 281474976710656 + eid
	now := time.Now()

	// If no new milestone, just copy "milestone_id" from the source
	if milestone == "" {
		milestone = "milestone_id"
	}

	// Start transaction
	tc, err := c.Begin()
	lib.FatalOnError(err)

	// Create new issue state
	lib.ExecSQLTxWithErr(
		tc,
		ctx,
		fmt.Sprintf(
			"insert into gha_issues("+
				"id, event_id, assignee_id, body, closed_at, comments, created_at, "+
				"locked, milestone_id, number, state, title, updated_at, user_id, "+
				"dup_actor_id, dup_actor_login, dup_repo_id, dup_repo_name, dup_type, dup_created_at, "+
				"dup_user_login, dupn_assignee_login, is_pull_request) "+
				"select id, %s, assignee_id, body, %s, %s, created_at, "+
				"%s, %s, number, %s, title, %s, 0, "+
				"0, 'devstats-bot', dup_repo_id, dup_repo_name, 'ArtificialEvent', %s, "+
				"'devstats-bot', dupn_assignee_login, is_pull_request "+
				"from gha_issues where id = %s and event_id = %s",
			lib.NValue(1),
			lib.NValue(2),
			lib.NValue(3),
			lib.NValue(4),
			milestone,
			lib.NValue(5),
			lib.NValue(6),
			lib.NValue(7),
			lib.NValue(8),
			lib.NValue(9),
		),
		lib.AnyArray{
			eventID,
			lib.TimeOrNil(ghIssue.ClosedAt),
			lib.IntOrNil(ghIssue.Comments),
			lib.BoolOrNil(ghIssue.Locked),
			lib.StringOrNil(ghIssue.State),
			now,
			now,
			iid,
			eid,
		}...,
	)

	// Create artificial 'ArtificialEvent' event
	lib.ExecSQLTxWithErr(
		tc,
		ctx,
		fmt.Sprintf(
			"insert into gha_events("+
				"id, type, actor_id, repo_id, public, created_at, "+
				"dup_actor_login, dup_repo_name, org_id, forkee_id) "+
				"select %s, 'ArtificialEvent', 0, repo_id, public, %s, "+
				"'devstats-bot', dup_repo_name, org_id, forkee_id "+
				"from gha_events where id = %s",
			lib.NValue(1),
			lib.NValue(2),
			lib.NValue(3),
		),
		lib.AnyArray{
			eventID,
			now,
			eid,
		}...,
	)

	// Create artificial event's payload
	lib.ExecSQLTxWithErr(
		tc,
		ctx,
		fmt.Sprintf(
			"insert into gha_payloads("+
				"event_id, push_id, size, ref, head, befor, action, "+
				"issue_id, pull_request_id, comment_id, ref_type, master_branch, commit, "+
				"description, number, forkee_id, release_id, member_id, "+
				"dup_actor_id, dup_actor_login, dup_repo_id, dup_repo_name, dup_type, dup_created_at) "+
				"select %s, null, null, null, null, null, 'artificial', "+
				"issue_id, pull_request_id, null, null, null, null, "+
				"null, number, null, null, null, "+
				"0, 'devstats-bot', dup_repo_id, dup_repo_name, 'ArtificialEvent', %s "+
				"from gha_payloads where issue_id = %s and event_id = %s",
			lib.NValue(1),
			lib.NValue(2),
			lib.NValue(3),
			lib.NValue(4),
		),
		lib.AnyArray{
			eventID,
			now,
			iid,
			eid,
		}...,
	)

	// Add issue labels
	for label, labelName := range labels {
		lib.ExecSQLTxWithErr(
			tc,
			ctx,
			fmt.Sprintf(
				"insert into gha_issues_labels(issue_id, event_id, label_id, "+
					"dup_actor_id, dup_actor_login, dup_repo_id, dup_repo_name, "+
					"dup_type, dup_created_at, dup_issue_number, dup_label_name) "+
					"select %s, %s, %s, "+
					"0, 'devstats-bot', repo_id, dup_repo_name, "+
					"'ArtificialEvent', %s, "+
					"(select number from gha_issues where id = %s and event_id = %s limit 1), %s "+
					"from gha_events where id = %s",
				lib.NValue(1),
				lib.NValue(2),
				lib.NValue(3),
				lib.NValue(4),
				lib.NValue(5),
				lib.NValue(6),
				lib.NValue(7),
				lib.NValue(8),
			),
			lib.AnyArray{
				iid,
				eventID,
				label,
				now,
				iid,
				eid,
				labelName,
				eid,
			}...,
		)
	}

	// Final commit
	lib.FatalOnError(tc.Commit())
	//lib.FatalOnError(tc.Rollback())
	return
}

// Insert Postgres vars
func ghapi2db() {
	// Environment context parse
	var ctx lib.Ctx
	ctx.Init()

	if ctx.SkipGHAPI {
		return
	}

	// Connect to Postgres DB
	c := lib.PgConn(&ctx)
	defer func() { lib.FatalOnError(c.Close()) }()

	// Connect to GitHub API
	gctx, gc := lib.GHClient(&ctx)

	// Get RateLimits info
	_, rem, wait := lib.GetRateLimits(gctx, gc, true)

	// Get number of CPUs available
	thrN := lib.GetThreadsNum(&ctx)
	lib.Printf("ghapi2db.go: Running (on %d CPUs): %d API points available, resets in %v\n", thrN, rem, wait)

	// Local or cron mode?
	dataPrefix := lib.DataDir
	if ctx.Local {
		dataPrefix = "./"
	}
	// Get recently modified opened issues/PRs
	bytes, err := lib.ReadFile(
		&ctx,
		dataPrefix+"util_sql/open_issues_and_prs.sql",
	)
	lib.FatalOnError(err)
	sqlQuery := string(bytes)

	// Set range from a context
	sqlQuery = strings.Replace(sqlQuery, "{{period}}", ctx.RecentRange, -1)
	rows := lib.QuerySQLWithErr(c, &ctx, sqlQuery)
	defer func() { lib.FatalOnError(rows.Close()) }()

	// Get issues/PRs to check
	// repo, number, issueID, is_pr
	var issuesMutex = &sync.Mutex{}
	issues := make(map[int64]issueConfig)
	var (
		repo    string
		number  int
		issueID int64
		pr      bool
	)
	nIssues := 0
	for rows.Next() {
		lib.FatalOnError(rows.Scan(&repo, &number, &issueID, &pr))
		cfg := issueConfig{
			repo:    repo,
			number:  number,
			issueID: issueID,
			pr:      pr,
		}
		v, ok := issues[issueID]
		if ok {
			if ctx.Debug > 0 {
				lib.Printf("Warning: we already have issue config for id=%d: %v, skipped new config: %v\n", issueID, v, cfg)
			}
			continue
		}
		issues[issueID] = cfg
		nIssues++
		if ctx.Debug > 0 {
			lib.Printf("Open Issue ID '%d' --> '%v'\n", issueID, cfg)
		}
	}
	lib.FatalOnError(rows.Err())
	if ctx.Debug > 0 {
		lib.Printf("Got %d open issues for period %s\n", nIssues, ctx.RecentRange)
	}

	if len(ctx.OnlyIssues) > 0 {
		ary := []string{}
		for _, issue := range ctx.OnlyIssues {
			ary = append(ary, strconv.FormatInt(issue, 10))
		}
		onlyIssues := make(map[int64]issueConfig)
		nOnlyIssues := 0
		lib.Printf("Processing only selected %d %v issues for debugging\n", len(ctx.OnlyIssues), ctx.OnlyIssues)
		irows := lib.QuerySQLWithErr(
			c,
			&ctx,
			fmt.Sprintf(
				"select distinct dup_repo_name, number, id, is_pull_request from gha_issues where id in (%s)",
				strings.Join(ary, ","),
			),
		)
		defer func() { lib.FatalOnError(irows.Close()) }()
		for irows.Next() {
			lib.FatalOnError(irows.Scan(&repo, &number, &issueID, &pr))
			cfg := issueConfig{
				repo:    repo,
				number:  number,
				issueID: issueID,
				pr:      pr,
			}
			v, ok := onlyIssues[issueID]
			if ok {
				if ctx.Debug > 0 {
					lib.Printf("Warning: we already have issue config for id=%d: %v, skipped new config: %v\n", issueID, v, cfg)
				}
				continue
			}
			onlyIssues[issueID] = cfg
			nOnlyIssues++
			_, ok = issues[issueID]
			if ok {
				lib.Printf("Issue %d(%v) would also be processed by the default workflow\n", issueID, cfg)
			} else {
				lib.Printf("Issue %d(%v) would not be processed by the default workflow\n", issueID, cfg)
			}
		}
		lib.FatalOnError(irows.Err())
		lib.Printf("Processing %d/%d user provided issues\n", nOnlyIssues, len(ctx.OnlyIssues))
		issues = onlyIssues
		nIssues = nOnlyIssues
	}

	// GitHub paging config
	opt := &github.ListOptions{PerPage: 1000}
	// GitHub don't like MT quering - they say that:
	// 403 You have triggered an abuse detection mechanism. Please wait a few minutes before you try again
	// So let's get all GitHub stuff one-after-another (ugly and slow) and then spawn threads to speedup
	// Damn GitHub! - this could be working Number of CPU times faster! We're trying some hardcoded value: allowedThrN
	// Seems like GitHub is not detecting abuse when using 16 thread, but it detects when using 32.
	allowedThrN := 16
	if allowedThrN > thrN {
		allowedThrN = thrN
	}
	ch := make(chan bool)
	nThreads := 0
	dtStart := time.Now()
	lastTime := dtStart
	checked := 0
	lib.Printf("ghapi2db.go: Processing %d issues - GHAPI part\n", nIssues)
	for key := range issues {
		go func(ch chan bool, iid int64) {
			// Refer to current tag using index passed to anonymous function
			cfg := issues[iid]
			if ctx.Debug > 0 {
				lib.Printf("GitHub Issue ID (before) '%d' --> '%v'\n", iid, cfg)
			}
			// Get separate org and repo
			ary := strings.Split(cfg.repo, "/")
			if len(ary) != 2 {
				if ctx.Debug > 0 {
					lib.Printf("Warning: wrong repository name: %s\n", cfg.repo)
				}
				return
			}
			// Use Github API to get issue info
			for {
				_, rem, waitPeriod := lib.GetRateLimits(gctx, gc, true)
				if rem <= ctx.MinGHAPIPoints {
					if waitPeriod.Seconds() <= float64(ctx.MaxGHAPIWaitSeconds) {
						lib.Printf("API limit reached while getting issue data, waiting %v\n", waitPeriod)
						time.Sleep(time.Duration(1) * time.Second)
						time.Sleep(waitPeriod)
						continue
					} else {
						lib.Fatalf("API limit reached while getting issue data, aborting, don't want to wait %v\n", waitPeriod)
						return
					}
				}
				issue, _, err := gc.Issues.Get(gctx, ary[0], ary[1], cfg.number)
				handlePossibleError(err, &cfg, "Issues.Get")
				if issue.Milestone != nil {
					cfg.milestoneID = issue.Milestone.ID
				}
				cfg.ghIssue = issue
				break
			}

			// Use GitHub API to get labels info
			cfg.labelsMap = make(map[int64]string)
			var (
				resp   *github.Response
				labels []*github.Label
			)
			for {
				for {
					_, rem, waitPeriod := lib.GetRateLimits(gctx, gc, true)
					if rem <= ctx.MinGHAPIPoints {
						if waitPeriod.Seconds() <= float64(ctx.MaxGHAPIWaitSeconds) {
							lib.Printf("API limit reached while getting issue labels, waiting %v\n", waitPeriod)
							time.Sleep(time.Duration(1) * time.Second)
							time.Sleep(waitPeriod)
							continue
						} else {
							lib.Fatalf("API limit reached while getting issue labels, aborting, don't want to wait %v\n", waitPeriod)
							return
						}
					}
					labels, resp, err = gc.Issues.ListLabelsByIssue(gctx, ary[0], ary[1], cfg.number, opt)
					handlePossibleError(err, &cfg, "Issues.ListLabelsByIssue")
					for _, label := range labels {
						cfg.labelsMap[*label.ID] = *label.Name
					}
					break
				}

				// Handle eventual paging (shoudl not happen for labels)
				if resp.NextPage == 0 {
					break
				}
				opt.Page = resp.NextPage
			}
			labelsAry := lib.Int64Ary{}
			for label := range cfg.labelsMap {
				labelsAry = append(labelsAry, label)
			}
			sort.Sort(labelsAry)
			l := len(labelsAry)
			for i, label := range labelsAry {
				if i == l-1 {
					cfg.labels += fmt.Sprintf("%d", label)
				} else {
					cfg.labels += fmt.Sprintf("%d,", label)
				}
			}
			if ctx.Debug > 0 {
				lib.Printf("GitHub Issue ID (after) '%d' --> '%v'\n", iid, cfg)
			}

			// Finally update issues map, this must be protected by the mutex
			issuesMutex.Lock()
			issues[iid] = cfg
			issuesMutex.Unlock()

			// Synchronize go routine
			if ch != nil {
				ch <- true
			}
		}(ch, key)
		// go routine called with 'ch' channel to sync and tag index

		nThreads++
		if nThreads == allowedThrN {
			<-ch
			nThreads--
			checked++
			// Get RateLimits info
			_, rem, wait := lib.GetRateLimits(gctx, gc, true)
			lib.ProgressInfo(checked, nIssues, dtStart, &lastTime, time.Duration(10)*time.Second, fmt.Sprintf("API points: %d, resets in: %v", rem, wait))
		}
	}
	// Usually all work happens on '<-ch'
	lib.Printf("Final GHAPI threads join\n")
	for nThreads > 0 {
		<-ch
		nThreads--
		checked++
		// Get RateLimits info
		_, rem, wait := lib.GetRateLimits(gctx, gc, true)
		lib.ProgressInfo(checked, nIssues, dtStart, &lastTime, time.Duration(10)*time.Second, fmt.Sprintf("API points: %d, resets in: %v", rem, wait))
	}

	// Now iterate all issues/PR in MT mode
	ch = make(chan bool)
	nThreads = 0
	dtStart = time.Now()
	lastTime = dtStart
	checked = 0
	updates := 0
	lib.Printf("ghapi2db.go: Processing %d issues - GHA part\n", nIssues)
	// Use map key to pass to the closure
	for key := range issues {
		go func(ch chan bool, iid int64) {
			// Refer to current tag using index passed to anonymous function
			cfg := issues[iid]
			if ctx.Debug > 0 {
				lib.Printf("GHA Issue ID '%d' --> '%v'\n", iid, cfg)
			}
			var (
				ghaMilestoneID *int64
				ghaEventID     int64
			)

			// Process current milestone
			apiMilestoneID := cfg.milestoneID
			rowsM := lib.QuerySQLWithErr(
				c,
				&ctx,
				fmt.Sprintf("select milestone_id, event_id from gha_issues where id = %s order by updated_at desc, event_id desc limit 1", lib.NValue(1)),
				cfg.issueID,
			)
			defer func() { lib.FatalOnError(rowsM.Close()) }()
			for rowsM.Next() {
				lib.FatalOnError(rowsM.Scan(&ghaMilestoneID, &ghaEventID))
			}
			lib.FatalOnError(rowsM.Err())

			// newMilestone will be non-empty when we detect that something needs to be updated
			newMilestone := ""
			if apiMilestoneID == nil && ghaMilestoneID != nil {
				newMilestone = "null"
				if ctx.Debug > 0 {
					lib.Printf("Updating issue '%v' milestone to null, it was %d (event_id %d)\n", cfg, *ghaMilestoneID, ghaEventID)
				}
			}
			if apiMilestoneID != nil && (ghaMilestoneID == nil || *apiMilestoneID != *ghaMilestoneID) {
				newMilestone = fmt.Sprintf("%d", *apiMilestoneID)
				if ctx.Debug > 0 {
					if ghaMilestoneID != nil {
						lib.Printf("Updating issue '%v' milestone to %d, it was %d (event_id %d)\n", cfg, *apiMilestoneID, *ghaMilestoneID, ghaEventID)
					} else {
						lib.Printf("Updating issue '%v' milestone to %d, it was null (event_id %d)\n", cfg, *apiMilestoneID, ghaEventID)
					}
				}
			}
			// Process current labels
			rowsL := lib.QuerySQLWithErr(
				c,
				&ctx,
				fmt.Sprintf(
					"select coalesce(string_agg(sub.label_id::text, ','), '') from "+
						"(select label_id from gha_issues_labels where event_id = %s "+
						"order by label_id) sub",
					lib.NValue(1),
				),
				ghaEventID,
			)
			defer func() { lib.FatalOnError(rowsL.Close()) }()
			ghaLabels := ""
			for rowsL.Next() {
				lib.FatalOnError(rowsL.Scan(&ghaLabels))
			}
			lib.FatalOnError(rowsL.Err())
			if ctx.Debug > 0 && ghaLabels != cfg.labels {
				lib.Printf("Updating issue '%v' labels to '%s', they were: '%s' (event_id %d)\n", cfg, cfg.labels, ghaLabels, ghaEventID)
			}

			// Do the update if needed: wrong milestone or label set
			if newMilestone != "" || ghaLabels != cfg.labels {
				lib.FatalOnError(
					artificialEvent(
						c,
						&ctx,
						cfg.issueID,
						ghaEventID,
						newMilestone,
						cfg.labelsMap,
						ghaLabels != cfg.labels,
						cfg.ghIssue,
					),
				)
				updates++
			}

			// Synchronize go routine
			if ch != nil {
				ch <- true
			}
		}(ch, key)

		// go routine called with 'ch' channel to sync and tag index
		nThreads++
		if nThreads == thrN {
			<-ch
			nThreads--
			checked++
			lib.ProgressInfo(checked, nIssues, dtStart, &lastTime, time.Duration(10)*time.Second, "")
		}
	}
	// Usually all work happens on '<-ch'
	lib.Printf("Final GHA threads join\n")
	for nThreads > 0 {
		<-ch
		nThreads--
		checked++
		lib.ProgressInfo(checked, nIssues, dtStart, &lastTime, time.Duration(10)*time.Second, "")
	}
	// Get RateLimits info
	_, rem, wait = lib.GetRateLimits(gctx, gc, true)
	lib.Printf(
		"ghapi2db.go: Processed %d issues/PRs (%d updated): %d API points remain, resets in %v\n",
		checked, updates, rem, wait,
	)
}

func main() {
	dtStart := time.Now()
	ghapi2db()
	dtEnd := time.Now()
	lib.Printf("Time: %v\n", dtEnd.Sub(dtStart))
}
