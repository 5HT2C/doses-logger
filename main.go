package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

var (
	caser       = cases.Title(language.English)
	client      = http.DefaultClient
	dosageRegex = regexp.MustCompile("([0-9.]+)([ -_]+)?([μµ]g|mg|g|u|x|)?")

	//prefsUrl = "http://localhost:6010/media/doses-prefs.json"
	options  = DisplayOptions{}
	options2 = DisplayOptions{}
	dLayouts = []string{"2006/01/02", "2006-01-02", "01/02/2006", "01-02-2006"}
	tLayouts = []string{"3:04pm", "15:04", "3:04"}
	timeZero = time.Unix(0, 0)

	dosesUrl = flag.String("url", "http://localhost:6010/media/doses.json", "URL for doses.json")
	urlToken = flag.String("token", "", "token for fs-over-http")

	optAdd = flag.Bool("add", false, "Set to add a dose")
	optRm  = flag.Bool("rm", false, "Set to remove the *last added* dose")
	optTop = flag.Bool("stat-top", false, "Set to view top statistics")
	optAvg = flag.Bool("stat-avg", false, "Set to view average dose statistics")
	optJ   = flag.Bool("j", false, "Set for json output")
	optU   = flag.Bool("u", false, "Show UNIX timestamp in non-json mode")
	optT   = flag.Bool("t", false, "Show dottime format in non-json mode")
	optR   = flag.Bool("r", false, "Show in reverse order")
	optG   = flag.String("g", "", "filter for text")
	optN   = flag.Int("n", 0, "Show last n doses, -1 = all")

	aTimezone = flag.String("timezone", "", "Set timezone")
	aDate     = flag.String("date", "", "Set date (defaults to now)")
	aTime     = flag.String("time", "", "Set time (defaults to now)")
	aDosage   = flag.String("a", "", "Set dosage")
	aDrug     = flag.String("d", "", "Set drug name")
	aRoa      = flag.String("roa", "", "Set RoA")
	aNote     = flag.String("note", "", "Add note")
)

//type MainPreferences struct {
//	Preferences map[string]UserPreferences `json:"preferences,omitempty"`
//}

type UserPreferences struct {
	DateFmt string `json:"date_fmt,omitempty"`
	TimeFmt string `json:"time_fmt,omitempty"`
}

type Mode int64

const (
	ModeGet = iota
	ModeAdd
	ModeRm
	ModeStatTop
	ModeStatAvg
)

type DisplayOptions struct {
	Mode
	Json     bool
	Unix     bool
	DotTime  bool
	Reversed bool
	Filter   string
	Show     int
}

func (d *DisplayOptions) Parse() {
	var mode Mode = ModeGet
	if *optAdd {
		mode = ModeAdd
	} else if *optRm {
		mode = ModeRm
	} else if *optTop {
		mode = ModeStatTop
	} else if *optAvg {
		mode = ModeStatAvg
	}

	showLast := *optN
	if showLast == 0 {
		showLast = 5
	}

	options = DisplayOptions{Mode: mode, Json: *optJ, Unix: *optU, DotTime: *optT, Reversed: *optR, Filter: *optG, Show: showLast}
}

func (d *DisplayOptions) Stash() {
	options2 = options
}

func (d *DisplayOptions) Pop() {
	options = options2
}

type Dose struct { // timezone,date,time,dosage,drug,roa,note
	Position  int       `json:"position"` // order added
	Timestamp time.Time `json:"timestamp,omitempty"`
	Timezone  string    `json:"timezone,omitempty"`
	Date      string    `json:"date,omitempty"`
	Time      string    `json:"time,omitempty"`
	Dosage    string    `json:"dosage,omitempty"`
	Drug      string    `json:"drug,omitempty"`
	RoA       string    `json:"roa,omitempty"`
	Note      string    `json:"note,omitempty"`
}

func (d Dose) ParsedTime() (time.Time, error) {
	loc, err := time.LoadLocation(d.Timezone)
	if err != nil {
		return timeZero, err
	}

	if pt, err := time.ParseInLocation("2006/01/0215:04", d.Date+d.Time, loc); err == nil {
		return pt, nil
	} else {
		return timeZero, err
	}
}

func (d Dose) String() string {
	note := ""
	if d.Note != "" {
		note = ", Note: " + d.Note
	}

	dosage := ""
	if d.Dosage != "" {
		dosage = " " + d.Dosage
	}

	unix := ""
	if options.Unix {
		unix = fmt.Sprintf("%v ", d.Timestamp.Unix())
	}

	// print dottime format
	if options.DotTime {
		zone := d.Timestamp.Format("Z07")
		if zone == "Z" {
			zone = "+00"
		}

		return fmt.Sprintf("%s%s%s %s, %s%s", unix, d.Timestamp.UTC().Format("2006-01-02 15·04")+zone, dosage, d.Drug, d.RoA, note)
	}

	// print regular format
	return fmt.Sprintf("%s%s%s %s, %s%s", unix, d.Timestamp.Format("2006/01/02 15:04"), dosage, d.Drug, d.RoA, note)
}

type DoseUnitSize int64

const (
	DoseUnitSizeDefault   DoseUnitSize = 1
	DoseUnitSizeMilligram DoseUnitSize = 1000
	DoseUnitSizeGram      DoseUnitSize = 1000 * 1000
	DoseUnitSizeEthanol   DoseUnitSize = 0.1 * 0.7893 * 1000 * 1000 // 0.1mL = 1u of EtOH * density of EtOH at g/mL * to get micrograms
)

type DoseStat struct {
	Drug        string
	TotalDoses  int64
	TotalAmount float64 // in micrograms
	Unit        string
	UnitSize    DoseUnitSize
}

func (s DoseStat) UpdateUnit(u string) DoseStat {
	s.Unit = u

	if u == "u" && s.Drug == "Alcohol" {
		s.UnitSize = DoseUnitSizeEthanol
		return s
	}

	switch u {
	case "g":
		s.UnitSize = DoseUnitSizeGram
	case "mg":
		s.UnitSize = DoseUnitSizeMilligram
	default:
		s.UnitSize = DoseUnitSizeDefault
	}

	return s
}

func (s DoseStat) Format(n1, n2 int) string {
	offset := 0
	if strings.ContainsAny(s.Unit, "μµ") {
		offset = 1
	}

	f1 := fmt.Sprintf(
		"%v",
		s.TotalDoses,
	)
	f1 += strings.Repeat(" ", n1-len(f1))

	f2 := strings.TrimRight(
		strings.TrimRight(
			fmt.Sprintf(
				"%.2f",
				s.TotalAmount,
			), "0",
		), ".",
	) + s.Unit

	offset = n2 - len(f2) + offset
	if offset < 1 {
		offset = 1
	}
	f2 += strings.Repeat(" ", offset)

	return f1 + f2 + s.Drug
}

func main() {
	flag.Parse()
	options.Parse()

	var err error
	var doses []Dose
	//var prefs MainPreferences

	err = getJsonFromUrl(&doses, *dosesUrl)
	if err != nil {
		return // already handled
	}

	//err = getJsonFromUrl(&prefs, prefsUrl)
	//if err != nil {
	//	return // already handled
	//}

	switch options.Mode {
	case ModeGet:
		if options.Filter == "" {
			fmt.Printf("%s", getDoses(doses))
		} else {
			if filter, err := regexp.Compile(fmt.Sprintf("(?i)%s", options.Filter)); err != nil {
				fmt.Printf("Couldn't compile regex: %s\n", err)
			} else {
				fmt.Printf("%s", getDosesFiltered(doses, filter))
			}
		}
	case ModeRm:
		pos, posIndex := -1, -1

		for n1, dose := range doses {
			if dose.Position > pos {
				pos = dose.Position
				posIndex = n1
			}
		}

		if pos == -1 || posIndex == -1 {
			doses = SliceRemoveIndex(doses, len(doses)-1)
		} else if len(doses) > posIndex {
			doses = SliceRemoveIndex(doses, posIndex)
		}

		if !saveFileWrapper(doses, *dosesUrl) {
			return
		}

		fmt.Printf("%s", getDoses(doses))
	case ModeAdd:
		if *aDrug == "" {
			fmt.Printf("`-drug` is not set!\n")
			return
		} else {
			*aDrug = caseFmt(*aDrug)
		}

		timezone := "America/Toronto" // Default timezone. TODO: Proper handling / ask user for default.
		if *aTimezone == "" {
			if len(doses) > 0 {
				timezone = doses[len(doses)-1].Timezone
			}
		} else {
			timezone = *aTimezone
		}

		loc, err := time.LoadLocation(timezone)
		if err != nil {
			fmt.Printf("failed to load location: %v\n", err)
			return
		}

		t := time.Now().In(loc)

		if *aDate == "" {
			*aDate = time.Now().In(loc).Format("2006/01/02")
		}

		for n1, l := range dLayouts {
			if tp, err := time.ParseInLocation(l+"15:04", *aDate+"00:00", loc); err == nil {
				t = tp
				break
			}

			if n1 == len(dLayouts)-1 {
				fmt.Printf("failed to parse \"%s\" with any of the layouts \"[%s]\"\n", *aDate, strings.Join(dLayouts, ", "))
				return
			}
		}

		if *aTime == "" {
			*aTime = time.Now().In(loc).Format("15:04")
		}

		for n1, l := range tLayouts {
			if tp, err := time.ParseInLocation("2006/01/02"+l, t.Format("2006/01/02")+*aTime, loc); err == nil {
				t = tp
				break
			}

			if n1 == len(tLayouts)-1 {
				fmt.Printf("failed to parse \"%s\" with any of the layouts \"[%s]\"\n", *aTime, strings.Join(tLayouts, ", "))
				return
			}
		}

		dosage := strings.ReplaceAll(*aDosage, "µ", "μ")

		if *aRoa == "" {
			*aRoa = "Oral" // Default RoA. TODO: Proper handling / ask user for default.
		} else {
			*aRoa = caseFmt(*aRoa)
		}

		dose := Dose{
			Position:  lastPosition(doses) + 1,
			Timestamp: t,
			Timezone:  timezone,
			Date:      t.Format("2006/01/02"),
			Time:      t.Format("15:04"),
			Dosage:    dosage,
			Drug:      *aDrug,
			RoA:       *aRoa,
			Note:      *aNote,
		}

		doses = append(doses, dose)

		// Sort by date and time
		sort.Slice(doses, func(i, j int) bool {
			return doses[i].Timestamp.Unix() < doses[j].Timestamp.Unix()
		})

		if !saveFileWrapper(doses, *dosesUrl) {
			return
		}

		fmt.Printf("%s", getDoses(doses))
	case ModeStatTop, ModeStatAvg:
		dosesFiltered := make([]Dose, 0)

		if options.Filter == "" {
			dosesFiltered = append(dosesFiltered, doses...)
		} else {
			if filter, err := regexp.Compile(fmt.Sprintf("(?i)%s", options.Filter)); err != nil || filter == nil {
				fmt.Printf("Couldn't compile regex: %s\n", err)
			} else {
				for _, d := range doses {
					if filter.MatchString(d.String()) {
						dosesFiltered = append(dosesFiltered, d)
					}
				}
			}
		}

		stats := make(map[string]DoseStat)
		statTotal := DoseStat{Drug: "Total"}
		statTotal = statTotal.UpdateUnit("μg")

		if options.Mode == ModeStatAvg {
			statTotal.Drug = "Average"
		}

		//
		// increment total doses and total amount for each drug
		for _, d := range dosesFiltered {
			stat := stats[d.Drug]
			stat.Drug = d.Drug
			stat.TotalDoses += 1
			statTotal.TotalDoses += 1

			units := dosageRegex.FindStringSubmatch(d.Dosage)
			if len(units) != 4 {
				continue
			}

			amount, err := strconv.ParseFloat(units[1], 64)
			if err != nil {
				continue
			}

			stat = stat.UpdateUnit(units[3])
			amountUg := amount * float64(stat.UnitSize)

			if stat.UnitSize == DoseUnitSizeEthanol {
				stat.TotalAmount += amount
			} else {
				stat.TotalAmount += amountUg
			}

			statTotal.TotalAmount += amountUg

			stats[d.Drug] = stat
		}

		// get the longest len to use for spacing later
		highestLen := len(fmt.Sprintf("%v", statTotal.TotalDoses)) + 1

		//
		// go through each stat and convert smaller units to larger ones when appropriate
		doseStats := make([]DoseStat, 0)
		stats["Total"] = statTotal

		for _, v := range stats {
			// convert for average stats
			if options.Mode == ModeStatAvg {
				v.TotalAmount = v.TotalAmount / float64(v.TotalDoses)
			}

			// convert from micrograms to larger units if too big
			switch v.Unit {
			case "g", "mg", "μg", "µg":
				if v.TotalAmount >= 1000 {
					v = v.UpdateUnit("mg")
					v.TotalAmount = v.TotalAmount / float64(DoseUnitSizeMilligram)
				}

				if v.TotalAmount >= 1000 {
					v = v.UpdateUnit("g")
					v.TotalAmount = v.TotalAmount / float64(DoseUnitSizeMilligram)
				}
			}

			// Now we can finally append to be sorted
			doseStats = append(doseStats, v)
		}

		// If total doses is the same, sort by total amount
		// Then sort by total doses
		sort.Slice(doseStats, func(i, j int) bool {
			if doseStats[i].TotalDoses == doseStats[j].TotalDoses {
				return doseStats[i].TotalAmount*float64(doseStats[i].UnitSize) < doseStats[j].TotalAmount*float64(doseStats[j].UnitSize)
			}
			return doseStats[i].TotalDoses < doseStats[j].TotalDoses
		})

		lines := ""
		for _, s := range doseStats {
			lines += fmt.Sprintf("%s\n", s.Format(highestLen, 9))
		}

		fmt.Printf("%s", lines)
	default:
		fmt.Printf("Not a valid `mode`!\n")
	}
}

func getJsonFromUrl(v any, path string) error {
	response, err := http.Get(path)
	if err != nil {
		fmt.Printf("failed to read json: %v\n", err)
		return err
	}

	defer response.Body.Close()
	b, err := io.ReadAll(response.Body)
	if err != nil {
		fmt.Printf("failed to read body: %v\n", err)
		return err
	}

	err = json.Unmarshal(b, v)
	if err != nil {
		fmt.Printf("failed to unmarshal doses: \n%s\n%v\n", b, err)
		return err
	}

	return nil
}

func caseFmt(s string) string {
	if s == "" {
		return s
	}

	// Removes greek characters, which means strings with lowercase greek and uppercase latin will be treated as all uppercase.
	// This is useful for something like α-PHP, where otherwise caser.String(s) would return A-Php, which is not what we want.
	// Initially I implemented this as a function that replaced lowercase greek with upper, but it's more efficient to simply remove the greek.
	removeGreek := func(s string) string {
		greek := map[rune]bool{'α': true}
		sr := []rune(s)

		for i, c := range sr {
			if ok := greek[c]; ok {
				sr = append(sr[0:i], sr[i+1:]...)
			}
		}

		return string(sr)
	}

	// If it Starts with a lowercase letter, uppercase it.
	// TODO: This will not work for something like 3-HO-PCP. Need better solution.
	// Simply checking for a number isn't enough, as 4-PrO-DMT wouldn't work.
	// A database of drug names or using the user's last casing when unsure would probably be the way to go.
	if removeGreek(s[:1]) != removeGreek(strings.ToUpper(s[:1])) {
		return caser.String(s)
	}

	return s
}

func lastPosition(doses []Dose) int {
	n := -1
	for n1 := range doses {
		if n1 > n {
			n = n1
		}
	}
	if n == -1 {
		return 0
	}

	return n
}

func jsonMarshal(content any) (string, error) {
	b, err := json.MarshalIndent(content, "", "    ")
	if err != nil {
		fmt.Printf("error marshalling json: %v\n", b)
	}
	return string(b), err
}

func saveFileWrapper(content any, path string) (r bool) {
	var r1, r2 bool
	r1 = saveFile(content, path)

	txtPath := strings.TrimSuffix(path, "/doses.json") + "/doses.txt"

	// faster way of checking if path changed correctly. if it didn't have the right suffix, txtPath will be longer
	if r1 && len(txtPath) < len(path) {

		switch t := content.(type) {
		case []Dose:
			options.Stash()
			options.DotTime = true
			//options.Reversed = true

			r2 = saveFile(getDoses(t), txtPath)
			options.Pop()
		default:
			fmt.Printf("content.(type) is not a []Dose and we're saving /doses.json! If you're reading this, blame frogg.ie\n")
		}

	} else { // don't try to save non-default file with a .txt
		r2 = true
	}

	return r1 && r2
}

func saveFile(content any, path string) (r bool) {
	if *urlToken == "" {
		fmt.Printf("`-token` not set!\n")
		return false
	}

	u := strings.Replace(path, "media/", "public/media/", 1)

	j, err := jsonMarshal(content)
	if err != nil { // handled by jsonMarshal
		return false
	}

	req, err := http.NewRequest("POST", u, strings.NewReader(url.Values{"content": {j + "\n"}}.Encode()))
	if err != nil {
		fmt.Printf("failed to make new request: %v\n", err)
		return false
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Auth", *urlToken)
	response, err := client.Do(req)
	if err != nil {
		fmt.Printf("error posting body: %s\n", j)
		return false
	}

	if response.StatusCode != 200 {
		defer response.Body.Close()
		b, err := io.ReadAll(response.Body)
		if err != nil {
			fmt.Printf("failed to read body (code %v): %v\n", response.StatusCode, err)
			return false
		}

		fmt.Printf("status code was %v:\n%s\n", response.StatusCode, b)
		return false
	}

	return true
}

func getDoses(doses []Dose) string {
	return getDosesFiltered(doses, nil)
}

func getDosesFiltered(doses []Dose, filter *regexp.Regexp) string {
	if options.Reversed {
		SliceReverse(doses)
	}

	if options.Json {
		dosesFiltered := make([]Dose, 0)

		if filter == nil {
			dosesFiltered = append(dosesFiltered, doses...)
		} else {
			for _, d := range doses {
				if filter.MatchString(d.String()) {
					dosesFiltered = append(dosesFiltered, d)
				}
			}
		}

		if options.Show > len(dosesFiltered) || options.Show <= 0 {
			options.Show = len(dosesFiltered)
		}

		j, err := jsonMarshal(dosesFiltered[len(dosesFiltered)-options.Show:])
		if err != nil {
			return ""
		}

		return j
	} else {
		dosesStr := ""
		for _, dose := range doses {
			dStr := dose.String() + "\n"

			if filter == nil || (filter != nil && filter.MatchString(dStr)) { // options.Filter
				dosesStr += dStr
			}
		}
		return Tail(dosesStr, options.Show)
	}
}

func SliceRemoveIndex[T comparable](s []T, i int) []T {
	return append(s[:i], s[i+1:]...)
}

func Tail(s string, n int) string {
	if n < 0 {
		return s
	}

	lines := strings.Split(s, "\n")
	SliceReverse(lines)

	newLines := make([]string, 0)

	i := 0
	for _, line := range lines {
		i++
		newLines = append(newLines, line)
		if i > n {
			break
		}
	}

	SliceReverse(newLines)

	return strings.Join(newLines, "\n")
}

// SliceReverse will reverse the order of s
func SliceReverse[S ~[]T, T any](s S) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}
