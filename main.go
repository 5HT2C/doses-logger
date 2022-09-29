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
	"strings"
	"time"
)

var (
	caser  = cases.Title(language.English)
	client = http.DefaultClient

	dosesUrl = flag.String("url", "http://localhost:6010/media/doses.json", "URL for doses.json")
	urlToken = flag.String("token", "", "token for fs-over-http")

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

type Dose struct { // timezone,date,time,dosage,drug,roa,note
	Timezone string `json:"timezone,omitempty"`
	Date     string `json:"date,omitempty"`
	Time     string `json:"time,omitempty"`
	Dosage   string `json:"dosage,omitempty"`
	Drug     string `json:"drug,omitempty"`
	RoA      string `json:"roa,omitempty"`
	Note     string `json:"note,omitempty"`
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

	return fmt.Sprintf("%s %s%s %s, %s%s", d.Date, d.Time, dosage, d.Drug, d.RoA, note)
}

func (d Dose) Json() string {
	return fmt.Sprintf(`{"timezone": "%s", "date": "%s", "time": "%s", "dosage": "%s", "drug": "%s", "roa": "%s", "note": "%s"}`, d.Timezone, d.Date, d.Time, d.Dosage, d.Drug, d.RoA, d.Note)
}

func main() {
	flag.Parse()
	response, err := http.Get(*dosesUrl)
	if err != nil {
		fmt.Printf("failed to read json: %v", err)
		return
	}

	defer response.Body.Close()
	b, err := io.ReadAll(response.Body)
	if err != nil {
		fmt.Printf("failed to read body: %v", err)
		return
	}

	var doses []Dose
	err = json.Unmarshal(b, &doses)
	if err != nil {
		fmt.Printf("failed to unmarshal doses: \n%s\n%v", b, err)
		return
	}

	mode := "get"

	if *add {
		mode = "add"
	} else if *rm {
		mode = "rm"
	}

	switch mode {
	case "get":
		dosesStr := getDoses(doses)

		if *g == "" {
			fmt.Printf("%s", dosesStr)
		} else {
			fmt.Printf("not implemented yet!")
		}
	case "rm":
		doses = SliceRemoveIndex(doses, len(doses)-1)
		fmt.Printf("%s", getDoses(doses))

		if !saveFile(doses) {
			return
		}
	case "add":
		if *aDrug == "" {
			fmt.Printf("`-drug` is not set!")
			return
		} else {
			if *aDrug != strings.ToUpper(*aDrug) {
				*aDrug = caser.String(*aDrug)
			}
		}

		timezone := "America/Toronto"
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
		}

		if *aDate == "" {
			*aDate = time.Now().In(loc).Format("2006/01/02")
		}

		if *aTime == "" {
			*aTime = time.Now().In(loc).Format("15:04")
		}

		if *aRoa == "" {
			*aRoa = "Oral"
		} else {
			*aRoa = caser.String(*aRoa)
		}

		dose := Dose{
			Timezone: timezone,
			Date:     *aDate,
			Time:     *aTime,
			Dosage:   *aDosage,
			Drug:     *aDrug,
			RoA:      *aRoa,
			Note:     *aNote,
		}

		doses = append(doses, dose)

		if !saveFile(doses) {
			return
		}

		fmt.Printf("%s", getDoses(doses))
	default:
		fmt.Printf("Not a valid `mode`!")
	}
}

func jsonDoses(doses []Dose) (string, error) {
	b, err := json.MarshalIndent(doses, "", "    ")
	if err != nil {
		fmt.Printf("error marshalling json: %v", b)
	}
	return string(b), err
}

func saveFile(doses []Dose) (r bool) {
	if *urlToken == "" {
		fmt.Printf("`-token` not set!")
		return false
	}

	u := strings.Replace(*dosesUrl, "media", "public/media", 1)

	j, err := jsonDoses(doses)
	if err != nil { // handled by jsonDoses
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

		j, err := jsonDoses(doses[len(doses)-*n:])
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
	if n == -1 {
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
