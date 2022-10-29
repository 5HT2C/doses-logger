package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

var (
	caser  = cases.Title(language.English)
	client = http.DefaultClient

	prefsUrl = "http://localhost:6010/media/doses-prefs.json"
	dLayouts = []string{"2006/01/02", "2006-01-02", "01/02/2006", "01-02-2006"}
	tLayouts = []string{"3:04pm", "15:04", "3:04"}
	timeZero = time.Unix(0, 0)

	dosesUrl = flag.String("url", "http://localhost:6010/media/doses.json", "URL for doses.json")
	urlToken = flag.String("token", "", "token for fs-over-http")
	perFmt   = flag.Bool("perFmt", false, "Replace single percentage signs with two")

	add = flag.Bool("add", false, "Set to add a dose")
	rm  = flag.Bool("rm", false, "Set to remove the last dose")
	j   = flag.Bool("j", false, "Set for json output")
	g   = flag.String("g", "", "filter for text")
	n   = flag.Int("n", 5, "Show last n doses, -1 = all")

	aTimezone = flag.String("timezone", "", "Set timezone")
	aDate     = flag.String("date", "", "Set date (defaults to now)")
	aTime     = flag.String("time", "", "Set time (defaults to now)")
	aDosage   = flag.String("a", "", "Set dosage")
	aDrug     = flag.String("d", "", "Set drug name")
	aRoa      = flag.String("roa", "", "Set RoA")
	aNote     = flag.String("note", "", "Add note")
)

type MainPreferences struct {
	PendConversion []string                   `json:"pend_conversion,omitempty"`
	Preferences    map[string]UserPreferences `json:"preferences,omitempty"`
}

type UserPreferences struct {
	DateFmt string `json:"date_fmt,omitempty"`
	TimeFmt string `json:"time_fmt,omitempty"`
}

type Dose struct { // timezone,date,time,dosage,drug,roa,note
	Position  int64     `json:"position,omitempty"` // order added
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

	return pFmt(fmt.Sprintf("%s%s %s, %s%s", d.Timestamp.Format("2006/01/02 15:04"), dosage, d.Drug, d.RoA, note))
}

func main() {
	flag.Parse()

	var err error
	var doses []Dose
	var prefs MainPreferences

	err = getJsonFromUrl(&doses, *dosesUrl)
	if err != nil {
		return // already handled
	}

	err = getJsonFromUrl(&prefs, prefsUrl)
	if err != nil {
		return // already handled
	}

	for n1, p := range prefs.PendConversion {
		pUrl := "http://localhost:6010/media/" + p

		err = getJsonFromUrl(&doses, pUrl)
		if err != nil {
			return // already handled
		}

		for n2, dose := range doses {
			t, err := dose.ParsedTime()
			if err == nil {
				doses[n2].Timestamp = t
			} else {
				fmt.Printf("failed to fix dose: %v\n%s\n", err, dose.String())
			}
		}

		// Sort by date and time
		sort.Slice(doses, func(i, j int) bool {
			return doses[i].Timestamp.Unix() < doses[j].Timestamp.Unix()
		})

		if saveFile(doses, pUrl) {
			fmt.Printf("fixed %v\n", p)
		}

		if n1 == len(prefs.PendConversion)-1 {
			prefs.PendConversion = []string{}
			saveFile(prefs, prefsUrl)
			return
		}
	}

	mode := "get"

	if *add {
		mode = "add"
	} else if *rm {
		mode = "rm"
	}

	switch mode {
	case "get":
		if *g == "" {
			fmt.Printf("%s", getDoses(doses))
		} else {
			fmt.Printf("not implemented yet!")
		}
	case "rm":
		doses = SliceRemoveIndex(doses, len(doses)-1)

		if !saveFile(doses, *dosesUrl) {
			return
		}

		fmt.Printf("%s", getDoses(doses))
	case "add":
		if *aDrug == "" {
			fmt.Printf("`-drug` is not set!")
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
			fmt.Printf("failed to load location: %v", err)
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

		if *aRoa == "" {
			*aRoa = "Oral" // Default RoA. TODO: Proper handling / ask user for default.
		} else {
			*aRoa = caseFmt(*aRoa)
		}

		dose := Dose{
			Timestamp: t,
			Timezone:  timezone,
			Date:      *aDate,
			Time:      *aTime,
			Dosage:    *aDosage,
			Drug:      *aDrug,
			RoA:       *aRoa,
			Note:      *aNote,
		}

		doses = append(doses, dose)

		// Sort by date and time
		sort.Slice(doses, func(i, j int) bool {
			return doses[i].Timestamp.Unix() < doses[j].Timestamp.Unix()
		})

		if !saveFile(doses, *dosesUrl) {
			return
		}

		fmt.Printf("%s", getDoses(doses))
	default:
		fmt.Printf("Not a valid `mode`!")
	}
}

func getJsonFromUrl(v any, path string) error {
	response, err := http.Get(path)
	if err != nil {
		fmt.Printf("failed to read json: %v", err)
		return err
	}

	defer response.Body.Close()
	b, err := io.ReadAll(response.Body)
	if err != nil {
		fmt.Printf("failed to read body: %v", err)
		return err
	}

	err = json.Unmarshal(b, v)
	if err != nil {
		fmt.Printf("failed to unmarshal doses: \n%s\n%v", b, err)
		return err
	}

	return nil
}

func caseFmt(s string) string {
	if s == "" {
		return s
	}

	// If it Starts with a lowercase letter, uppercase it.
	// TODO: This will not work for something like 3-HO-PCP. Need better solution.
	// Simply checking for a number isn't enough, as 4-PrO-DMT wouldn't work.
	// A database of drug names or using the user's last casing when unsure would probably be the way to go.
	if s[:1] != strings.ToUpper(s[:1]) {
		return caser.String(s)
	}

	return s
}

func pFmt(s string) string {
	if !*perFmt || s == "" {
		return s
	}

	return strings.ReplaceAll(s, "%", "%%")
}

func jsonMarshal(content any) (string, error) {
	b, err := json.MarshalIndent(content, "", "    ")
	if err != nil {
		fmt.Printf("error marshalling json: %v", b)
	}
	return string(b), err
}

func saveFile(content any, path string) (r bool) {
	if *urlToken == "" {
		fmt.Printf("`-token` not set!")
		return false
	}

	u := strings.Replace(path, "media/", "public/media/", 1)

	j, err := jsonMarshal(content)
	if err != nil { // handled by jsonMarshal
		return false
	}

	req, err := http.NewRequest("POST", u, strings.NewReader(url.Values{"content": {j}}.Encode()))
	if err != nil {
		fmt.Printf("failed to make new request: %v", err)
		return false
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Auth", *urlToken)
	response, err := client.Do(req)
	if err != nil {
		fmt.Printf("error posting body: %s", j)
		return false
	}

	if response.StatusCode != 200 {
		defer response.Body.Close()
		b, err := io.ReadAll(response.Body)
		if err != nil {
			fmt.Printf("failed to read body (code %v): %v", response.StatusCode, err)
			return false
		}

		fmt.Printf("status code was %v:\n%s", response.StatusCode, b)
		return false
	}

	return true
}

func getDoses(doses []Dose) string {
	if *j {
		if *n > len(doses) || *n <= 0 {
			*n = len(doses)
		}

		j, err := jsonMarshal(doses[len(doses)-*n:])
		if err != nil {
			return ""
		}

		return j
	} else {
		dosesStr := ""
		for _, dose := range doses {
			dosesStr += dose.String() + "\n"
		}
		return Tail(dosesStr, *n)
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
