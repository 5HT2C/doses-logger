package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
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
	dosageRegex = regexp.MustCompile("([0-9.]+)([ -_]+)?([μµ]g|mg|g|kg|u|x|mL|)?")

	//prefsUrl = "http://localhost:6010/media/doses-prefs.json"
	options  = &DisplayOptions{}
	dLayouts = []string{"2006/01/02", "2006-01-02", "01/02/2006", "01-02-2006"}
	tLayouts = []string{"3:04pm", "15:04", "3:04", "1504"}
	timeZero = time.Unix(0, 0)

	dosesUrl = flag.String("url", "http://localhost:6010/media/doses.json", "URL for doses.json")
	urlToken = flag.String("token", "", "token for fs-over-http (default $FOH_TOKEN or $FOH_SERVER_AUTH from env)")

	optAdd = flag.Bool("add", false, "Set to add a dose")
	optRm  = flag.Bool("rm", false, "Set to remove the *last added* dose")
	optSav = flag.Bool("save", false, "Run a manual save to re-generate the .txt format after a manual edit")
	optTop = flag.Bool("stat-top", false, "Set to view top statistics")
	optAvg = flag.Bool("stat-avg", false, "Set to view average dose statistics")
	optJ   = flag.Bool("j", false, "Set for json output")
	optU   = flag.Bool("u", false, "Show UNIX timestamp in non-json mode")
	optT   = flag.Bool("t", false, "Show dottime format in non-json mode")
	optR   = flag.Bool("r", false, "Show in reverse order")
	optS   = flag.Bool("s", false, "Start reading doses from top (applies before anything else)")
	optV   = flag.Bool("v", false, "Inverse filter for text")
	optG   = flag.String("g", "", "Filter for text (does not apply to -add or -rm)")
	optN   = flag.Int("n", 0, "Show last n doses, -1 = all (applied after filters)")

	aTimezone = flag.String("timezone", "", "Set timezone")
	aDate     = flag.String("date", "", "Set date (default \"time.Now()\")")
	aTime     = flag.String("time", "", "Set time (default \"time.Now()\")")
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
	ModeSave
	ModeStatTop
	ModeStatAvg
)

func (m Mode) String() string {
	switch m {
	case ModeGet:
		return "-get"
	case ModeAdd:
		return "-add"
	case ModeRm:
		return "-rm"
	case ModeSave:
		return "-save"
	case ModeStatTop:
		return "-stat-top"
	case ModeStatAvg:
		return "-stat-avg"
	default:
		return "-default"
	}
}

type DisplayOptions struct {
	Mode
	Json         bool
	Unix         bool
	DotTime      bool
	Reversed     bool
	StartAtTop   bool
	FilterInvert bool
	Filter       string
	FilterRegex  *regexp.Regexp
	Show         int
}

func (d *DisplayOptions) Parse() {
	var mode Mode
	switch {
	case *optAdd:
		mode = ModeAdd
	case *optRm:
		mode = ModeRm
	case *optSav:
		mode = ModeSave
	case *optTop:
		mode = ModeStatTop
	case *optAvg:
		mode = ModeStatAvg
	default:
		mode = ModeGet
	}

	// If we're not in a stat mode and the user hasn't set showLast, set it to 5 as a sane default
	showLast := *optN
	if showLast == 0 && mode != ModeStatTop && mode != ModeStatAvg {
		showLast = 5
	}

	options = &DisplayOptions{
		Mode:         mode,
		Json:         *optJ,
		Unix:         *optU,
		DotTime:      *optT,
		Reversed:     *optR,
		StartAtTop:   *optS,
		FilterInvert: *optV,
		Filter:       *optG,
		Show:         showLast,
	}
}

func (d *DisplayOptions) String() string {
	j, err := json.MarshalIndent(d, "", "    ")
	if err != nil {
		j = []byte(fmt.Sprintf("error marshalling json: %v", err))
	}

	return fmt.Sprintf("%s", j)
}

func (d *DisplayOptions) MarshalJSON() ([]byte, error) {
	type Alias DisplayOptions
	return json.Marshal(&struct {
		Mode string
		*Alias
	}{
		Mode:  d.Mode.String(),
		Alias: (*Alias)(d),
	})
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

func (d Dose) StringOptions(options *DisplayOptions) string {
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

func (d Dose) String() string {
	return d.StringOptions(options)
}

type DoseUnitSize int64

const (
	DoseUnitSizeDefault    DoseUnitSize = 1
	DoseUnitSizeMilliliter DoseUnitSize = 0
	DoseUnitSizeMilligram  DoseUnitSize = 1000
	DoseUnitSizeGram       DoseUnitSize = 1000 * 1000
	DoseUnitSizeKilogram   DoseUnitSize = 1000 * 1000 * 1000
	DoseUnitSizeEthanol    DoseUnitSize = 0.1 * 0.7893 * 1000 * 1000 // 0.1mL = 1u of EtOH (g/mL) * to get micrograms
	DoseUnitSizeGHB        DoseUnitSize = 1120.0 * 1000              // 1mL = 1120.0mg of GHB at 25°C * to get μg
	DoseUnitSizeGBL        DoseUnitSize = 1129.6 * 1000              // 1mL = 1129.6mg of GBL at 20°C * to get μg
	DoseUnitSizeBDO        DoseUnitSize = 1017.3 * 1000              // 1mL = 1017.3mg of 1,4-BDO at 25°C * to get μg
)

type DoseStat struct {
	Drug        string
	IsSpecial   bool
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

	if u == "mL" {
		switch s.Drug {
		case "GHB":
			s.UnitSize = DoseUnitSizeGHB
		case "GBL":
			s.UnitSize = DoseUnitSizeGBL
		case "BDO":
			s.UnitSize = DoseUnitSizeBDO
		default:
			s.UnitSize = DoseUnitSizeMilliliter
		}
		return s
	}

	switch u {
	case "kg":
		s.UnitSize = DoseUnitSizeKilogram
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

	// TODO: Make offset dynamic
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
	loadEnv()

	if options.FilterInvert && options.Filter == "" {
		fmt.Printf("-v is set but no -g filter is set? Can't invert filter without a filter to invert!\n")
		return
	}

	// We do not filter in ModeRm and ModeAdd for performance reasons
	if options.Filter != "" && options.Mode != ModeRm && options.Mode != ModeAdd {
		if filter, err := regexp.Compile(fmt.Sprintf("(?i)%s", options.Filter)); err != nil {
			fmt.Printf("-g is set but failed to compile regex: %s\n", err)
			return
		} else {
			options.FilterRegex = filter
		}
	}

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
	case ModeSave:
		if !saveFileWrapper(doses) {
			fmt.Printf("Failed to save one or more doses files!\n")
		}
	case ModeGet:
		fmt.Printf("%s", getDosesFmt(doses))
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

		if !saveFileWrapper(doses) {
			return
		}

		fmt.Printf("%s", getDosesFmt(doses))
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
			if len(*aTime) != len(l) { // slight efficiency boost instead of waiting for time.ParseInLocation to fail
				continue
			}

			if tp, err := time.ParseInLocation("2006/01/02"+l, t.Format("2006/01/02")+*aTime, loc); err == nil {
				t = tp
				break
			}

			if n1 == len(tLayouts)-1 {
				fmt.Printf("failed to parse \"%s\" with any of the layouts \"[%s]\"\n", *aTime, strings.Join(tLayouts, ", "))
				return
			}
		}

		// Replace dosage U+00B5 with U+03BC
		dosage := strings.ReplaceAll(*aDosage, "µ", "μ")

		// Replace dosage ml with mL
		if strings.HasSuffix(dosage, "ml") {
			dosage = strings.TrimSuffix(dosage, "ml")
			dosage += "mL"
		}

		if *aRoa == "" {
			*aRoa = "Oral" // Default RoA. TODO: Proper handling / ask user for default.
		} else {
			*aRoa = caseFmt(*aRoa)
		}

		dose := Dose{
			Position:  lastPosition(doses),
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

		if !saveFileWrapper(doses) {
			return
		}

		fmt.Printf("%s", getDosesFmt(doses))
	case ModeStatTop, ModeStatAvg:
		doses = getDoses(doses)

		stats := make(map[string]DoseStat)
		statTotal := DoseStat{Drug: "Total", IsSpecial: true}
		statTotal = statTotal.UpdateUnit("μg")

		if options.Mode == ModeStatAvg {
			statTotal.Drug = "Average"
		}

		//
		// increment total doses and total amount for each drug
		for _, d := range doses {
			stat := stats[d.Drug]
			stat.Drug = d.Drug
			stat.TotalDoses += 1
			statTotal.TotalDoses += 1
			stats[d.Drug] = stat // we still want to save the stat, so we can increment the total doses even if the dosage is not set or fails to parse

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

			switch stat.UnitSize {
			case DoseUnitSizeMilliliter, DoseUnitSizeEthanol, DoseUnitSizeGHB, DoseUnitSizeGBL, DoseUnitSizeBDO:
				stat.TotalAmount += amount
			default:
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
			case "kg", "g", "mg", "μg", "µg":
				if v.TotalAmount >= 1000 {
					v = v.UpdateUnit("mg")
					v.TotalAmount = v.TotalAmount / float64(DoseUnitSizeMilligram)
				}

				if v.TotalAmount >= 1000 {
					v = v.UpdateUnit("g")
					v.TotalAmount = v.TotalAmount / float64(DoseUnitSizeMilligram)
				}

				if v.TotalAmount >= 1000 {
					v = v.UpdateUnit("kg")
					v.TotalAmount = v.TotalAmount / float64(DoseUnitSizeMilligram)
				}
			}

			// Now we can finally append to be sorted
			doseStats = append(doseStats, v)
		}

		// If total doses is the same, sort by total amount
		// Then sort by total doses
		sort.SliceStable(doseStats, func(i, j int) bool {
			if doseStats[i].IsSpecial && !doseStats[j].IsSpecial {
				return false
			}

			if !doseStats[i].IsSpecial && doseStats[j].IsSpecial {
				return true
			}

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
	for _, d := range doses {
		if d.Position > n {
			n = d.Position
		}
	}

	return n + 1
}

func saveFileWrapper(doses []Dose) (r bool) {
	optionsJson := &DisplayOptions{Json: true}
	optionsTxt := &DisplayOptions{
		DotTime:    true,
		Reversed:   true,
		StartAtTop: true,
	}

	if content, err := getDosesFmtOptions(doses, optionsJson); err == nil {
		if !saveFile(content, *dosesUrl) {
			return false
		}

		// Don't try to save a .txt if saving the main db failed, we don't want to imply to the user that the db is fine
		if content, err := getDosesFmtOptions(doses, optionsTxt); err == nil {
			return saveFile(content, strings.TrimSuffix(*dosesUrl, ".json")+".txt")
		}
	}

	return false
}

func saveFile(content string, path string) (r bool) {
	if *urlToken == "" {
		fmt.Printf("`-token` not set!\n")
		return false
	}

	u := strings.Replace(path, "media/", "public/media/", 1)

	req, err := http.NewRequest("POST", u, strings.NewReader(url.Values{"content": {content}}.Encode()))
	if err != nil {
		fmt.Printf("failed to make new request: %v\n", err)
		return false
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Auth", *urlToken)
	response, err := client.Do(req)
	if err != nil {
		fmt.Printf("error posting body: %v\n%s\n", err, content)
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

func loadEnv() {
	isFlagPassed := func(name string) bool {
		found := false
		flag.Visit(func(f *flag.Flag) {
			if f.Name == name {
				found = true
			}
		})
		return found
	}

	loadVar := func(k string, fn func(v string)) bool {
		token, ok := os.LookupEnv(k)
		if ok {
			fn(token)
		}

		return ok
	}

	if !isFlagPassed("token") {
		loadToken := func(v string) {
			*urlToken = v
		}

		if loadVar("FOH_TOKEN", loadToken) {
			return
		}

		if loadVar("FOH_SERVER_AUTH", loadToken) {
			return
		}

		if loadVar("TOKEN", loadToken) {
			return
		}
	}
}

func getDosesFmt(doses []Dose) string {
	content, _ := getDosesFmtOptions(doses, options)
	return content
}

func getDosesFmtOptions(doses []Dose, options *DisplayOptions) (string, error) {
	d := getDosesOptions(doses, options)

	if options.Json {
		j, err := json.MarshalIndent(d, "", "    ")
		if err != nil {
			fmt.Printf("Failed to format doses: %v\n", err)
		}

		return string(j) + "\n", err
	} else {
		dosesStr := ""

		for _, dose := range d {
			dosesStr += dose.StringOptions(options) + "\n"
		}

		return Tail(dosesStr, options.Show), nil
	}
}

func getDoses(doses []Dose) []Dose {
	return getDosesOptions(doses, options)
}

func getDosesOptions(doses []Dose, options *DisplayOptions) []Dose {
	dosesTrans := make([]Dose, 0)
	dosesTrans = append(dosesTrans, doses...)

	if options.StartAtTop {
		SliceReverse(dosesTrans)
	}

	dosesFiltered := make([]Dose, 0)

	if options.FilterRegex == nil {
		dosesFiltered = append(dosesFiltered, dosesTrans...)
	} else {
		for _, d := range dosesTrans {
			if options.FilterInvert != options.FilterRegex.MatchString(d.StringOptions(options)) {
				dosesFiltered = append(dosesFiltered, d)
			}
		}
	}

	// Set limit of show length (number of doses to display, if less than 0 display all)
	if options.Show <= 0 || options.Show > len(dosesFiltered) {
		options.Show = len(dosesFiltered)
	}

	// slice range to limit of show length
	dosesCut := dosesFiltered[len(dosesFiltered)-options.Show:]

	if (options.StartAtTop && !options.Reversed) || (!options.StartAtTop && options.Reversed) {
		SliceReverse(dosesCut)
	}

	return dosesCut
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
