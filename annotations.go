package devstats

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Annotations contain list of annotations
type Annotations struct {
	Annotations []Annotation
}

// Annotation contain each annotation data
type Annotation struct {
	Name        string
	Description string
	Date        time.Time
}

// AnnotationsByDate annotations Sort interface
type AnnotationsByDate []Annotation

func (a AnnotationsByDate) Len() int {
	return len(a)
}
func (a AnnotationsByDate) Swap(i, j int) {
	a[i], a[j] = a[j], a[i]
}
func (a AnnotationsByDate) Less(i, j int) bool {
	return a[i].Date.Before(a[j].Date)
}

// GetFakeAnnotations - returns 'startDate - joinDate' and 'joinDate - now' annotations
func GetFakeAnnotations(startDate, joinDate time.Time) (annotations Annotations) {
	if !joinDate.After(startDate) {
		return
	}
	annotations.Annotations = append(
		annotations.Annotations,
		Annotation{
			Name:        "Project start",
			Description: ToYMDDate(startDate) + " - project starts",
			Date:        startDate,
		},
	)
	annotations.Annotations = append(
		annotations.Annotations,
		Annotation{
			Name:        "First CNCF project join date",
			Description: ToYMDDate(joinDate),
			Date:        joinDate,
		},
	)
	return
}

// GetAnnotations queries uses `git` to get `orgRepo` all tags list
// for all tags and returns those matching `annoRegexp`
func GetAnnotations(ctx *Ctx, orgRepo, annoRegexp string) (annotations Annotations) {
	// Get org and repo from orgRepo
	ary := strings.Split(orgRepo, "/")
	if len(ary) != 2 {
		Fatalf("main repository format must be 'org/repo', found '%s'", orgRepo)
	}

	// Compile annotation regexp if present, if no regexp then return all tags
	var re *regexp.Regexp
	if annoRegexp != "" {
		re = regexp.MustCompile(annoRegexp)
	}

	// Local or cron mode?
	cmdPrefix := ""
	if ctx.Local {
		cmdPrefix = LocalGitScripts
	}

	// We need this to capture 'git_tags.sh' output.
	ctx.ExecOutput = true

	// Get tags is using shell script that does 'chdir'
	// We cannot chdir because this is a multithreaded app
	// And all threads share CWD (current working directory)
	if ctx.Debug > 0 {
		Printf("Getting tags for repo %s\n", orgRepo)
	}
	dtStart := time.Now()
	rwd := ctx.ReposDir + orgRepo
	tagsStr, err := ExecCommand(
		ctx,
		[]string{cmdPrefix + "git_tags.sh", rwd},
		map[string]string{"GIT_TERMINAL_PROMPT": "0"},
	)
	dtEnd := time.Now()
	FatalOnError(err)

	tags := strings.Split(tagsStr, "\n")
	nTags := 0

	for _, tagData := range tags {
		data := strings.TrimSpace(tagData)
		if data == "" {
			continue
		}
		// Use '♂♀' separator to avoid any character that can appear inside tag name or description
		tagDataAry := strings.Split(data, "♂♀")
		if len(tagDataAry) != 3 {
			Fatalf("invalid tagData returned for repo: %s: '%s'", orgRepo, data)
		}
		tagName := tagDataAry[0]
		if re != nil && !re.MatchString(tagName) {
			continue
		}
		unixTimeStamp, err := strconv.ParseInt(tagDataAry[1], 10, 64)
		if err != nil {
			Printf("Invalid time returned for repo: %s, tag: %s: '%s'\n", orgRepo, tagName, data)
		}
		FatalOnError(err)
		creatorDate := time.Unix(unixTimeStamp, 0)
		message := tagDataAry[2]
		if len(message) > 40 {
			message = message[0:40]
		}
		replacer := strings.NewReplacer("\n", " ", "\r", " ", "\t", " ")
		message = replacer.Replace(message)

		annotations.Annotations = append(
			annotations.Annotations,
			Annotation{
				Name:        tagName,
				Description: message,
				Date:        creatorDate,
			},
		)
		nTags++
	}

	if ctx.Debug > 0 {
		Printf("Got %d tags for %s, took %v\n", nTags, orgRepo, dtEnd.Sub(dtStart))
	}

	return
}

// ProcessAnnotations Creates IfluxDB annotations and quick_series
func ProcessAnnotations(ctx *Ctx, annotations *Annotations, joinDate *time.Time) {
	// Connect to InfluxDB
	ic := IDBConn(ctx)
	defer func() { FatalOnError(ic.Close()) }()

	// Get BatchPoints
	var pts IDBBatchPointsN
	bp := IDBBatchPoints(ctx, &ic)
	pts.NPoints = 0
	pts.Points = &bp

	// Annotations must be sorted to create quick ranges
	sort.Sort(AnnotationsByDate(annotations.Annotations))

	// Iterate annotations
	for _, annotation := range annotations.Annotations {
		fields := map[string]interface{}{
			"title":       annotation.Name,
			"description": annotation.Description,
		}
		// Add batch point
		if ctx.Debug > 0 {
			Printf(
				"Series: %v: Date: %v: '%v', '%v'\n",
				"annotations",
				ToYMDDate(annotation.Date),
				annotation.Name,
				annotation.Description,
			)
		}
		pt := IDBNewPointWithErr(ctx, "annotations", nil, fields, annotation.Date)
		IDBAddPointN(ctx, &ic, &pts, pt)
	}

	// Join CNCF (additional annotation not used in quick ranges)
	if joinDate != nil {
		fields := map[string]interface{}{
			"title":       "CNCF join date",
			"description": ToYMDDate(*joinDate) + " - joined CNCF",
		}
		// Add batch point
		if ctx.Debug > 0 {
			Printf(
				"CNCF join date: %v: '%v', '%v'\n",
				ToYMDDate(*joinDate),
				fields["title"],
				fields["description"],
			)
		}
		pt := IDBNewPointWithErr(ctx, "annotations", nil, fields, *joinDate)
		IDBAddPointN(ctx, &ic, &pts, pt)
	}

	// Special ranges
	periods := [][3]string{
		{"d", "Last day", "1 day"},
		{"w", "Last week", "1 week"},
		{"d10", "Last 10 days", "10 days"},
		{"m", "Last month", "1 month"},
		{"q", "Last quarter", "3 months"},
		{"y", "Last year", "1 year"},
		{"y10", "Last decade", "10 years"},
	}

	// tags:
	// suffix: will be used as InfluxDB series name suffix and Grafana drop-down value (non-dsplayed)
	// name: will be used as Grafana drop-down value name
	// data: is suffix;period;from;to
	// period: only for special values listed here, last ... week, day, quarter, devade etc - will be passed to Postgres
	// from: only filled when using annotations range - exact date from
	// to: only filled when using annotations range - exact date to
	tags := make(map[string]string)
	// No fields value needed
	fields := map[string]interface{}{"value": 0.0}

	// Add special periods
	tagName := "quick_ranges"
	tm := TimeParseAny("2014-01-01")

	// Last "..." periods
	for _, period := range periods {
		tags[tagName+"_suffix"] = period[0]
		tags[tagName+"_name"] = period[1]
		tags[tagName+"_data"] = period[0] + ";" + period[2] + ";;"
		if ctx.Debug > 0 {
			Printf(
				"Series: %v: %+v\n",
				tagName,
				tags,
			)
		}
		// Add batch point
		pt := IDBNewPointWithErr(ctx, tagName, tags, fields, tm)
		IDBAddPointN(ctx, &ic, &pts, pt)
		tm = tm.Add(time.Hour)
	}

	// Add '(i) - (i+1)' annotation ranges
	lastIndex := len(annotations.Annotations) - 1
	for index, annotation := range annotations.Annotations {
		if index == lastIndex {
			sfx := fmt.Sprintf("anno_%d_now", index)
			tags[tagName+"_suffix"] = sfx
			tags[tagName+"_name"] = fmt.Sprintf("%s - now", annotation.Name)
			tags[tagName+"_data"] = fmt.Sprintf("%s;;%s;%s", sfx, ToYMDHMSDate(annotation.Date), ToYMDHMSDate(NextDayStart(time.Now())))
			if ctx.Debug > 0 {
				Printf(
					"Series: %v: %+v\n",
					tagName,
					tags,
				)
			}
			// Add batch point
			pt := IDBNewPointWithErr(ctx, tagName, tags, fields, tm)
			IDBAddPointN(ctx, &ic, &pts, pt)
			tm = tm.Add(time.Hour)
			break
		}
		nextAnnotation := annotations.Annotations[index+1]
		sfx := fmt.Sprintf("anno_%d_%d", index, index+1)
		tags[tagName+"_suffix"] = sfx
		tags[tagName+"_name"] = fmt.Sprintf("%s - %s", annotation.Name, nextAnnotation.Name)
		tags[tagName+"_data"] = fmt.Sprintf("%s;;%s;%s", sfx, ToYMDHMSDate(annotation.Date), ToYMDHMSDate(nextAnnotation.Date))
		if ctx.Debug > 0 {
			Printf(
				"Series: %v: %+v\n",
				tagName,
				tags,
			)
		}
		// Add batch point
		pt := IDBNewPointWithErr(ctx, tagName, tags, fields, tm)
		IDBAddPointN(ctx, &ic, &pts, pt)
		tm = tm.Add(time.Hour)
	}

	// Write the batch
	if !ctx.SkipIDB {
		if ctx.IDBDrop {
			QueryIDB(ic, ctx, "delete from \"quick_ranges\"")
		}
		FatalOnError(IDBWritePointsN(ctx, &ic, &pts))
	} else if ctx.Debug > 0 {
		Printf("Skipping annotations series write\n")
	}
}
