package main

import (
	"encoding/json"
	"errors"
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
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

var (
	caser       = cases.Title(language.English)
	client      = http.DefaultClient
	dosageRegex = regexp.MustCompile("([0-9.]+)([ -_]+)?([μµ]g|mg|g|kg|u|x|mL|)?")

	//prefsUrl = "http://localhost:6010/media/doses-prefs.json"
	options = &DisplayOptions{}

	dosesUrl = flag.String("url", "http://localhost:6010/media/doses.json", "URL for doses.json")
	urlToken = flag.String("token", "", "token for fs-over-http (default $FOH_TOKEN or $FOH_SERVER_AUTH from env)")

	optAdd = flag.Bool("add", false, "Set to add a dose")
	optRm  = flag.Bool("rm", false, "Set to remove the *last added* dose")
	optRmP = flag.Int("rmp", -1, "Set to remove dose *by position*")
	optSav = flag.Bool("save", false, "Run a manual save to re-generate the .txt format after a manual edit")
	optTop = flag.Bool("stat-top", false, "Set to view top statistics")
	optAvg = flag.Bool("stat-avg", false, "Set to view average dose statistics")
	optNts = flag.Bool("ignore-notes", false, "Set to hide notes (applies before filters)")
	optJ   = flag.Bool("j", false, "Set for json output")
	optU   = flag.Bool("u", false, "Show UNIX timestamp in non-json mode")
	optT   = flag.Bool("t", false, "Show dottime format in non-json mode")
	optR   = flag.Bool("r", false, "Show in reverse order")
	optS   = flag.Bool("s", false, "Start reading doses from top (applies before anything else)")
	optV   = flag.Bool("v", false, "Inverse filter for text")
	optG   = flag.String("g", "", "Filter for text (does not apply to -add or -rm)")
	optN   = flag.Int("n", 0, "Show last n doses, -1 = all (applied after filters)")

	aChangeTz = flag.String("change-tz", "", "Change timezone (retain literal date / time) (applies to last -n doses)")
	aConvTz   = flag.String("convert-tz", "", "Convert timezone (shift relative date / time) (applies to last -n doses)")
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
	ModeGet Mode = iota
	ModeAdd
	ModeRm
	ModeRmPosition
	ModeTzChange
	ModeTzConvert
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
	case ModeRmPosition:
		return "-rmp"
	case ModeTzChange:
		return "-change-tz"
	case ModeTzConvert:
		return "-convert-tz"
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

type LayoutFormat string
type WrapFormat struct {
	Prefix string
	Suffix string
}

type TimestampLayout struct {
	Formats []LayoutFormat
	Layout  WrapFormat
	Value   WrapFormat
}

type DisplayOptions struct {
	Mode
	Json         bool
	Unix         bool
	DotTime      bool
	IgnoreNotes  bool
	Reversed     bool
	StartAtTop   bool
	FilterInvert bool
	Filter       string
	FilterRegex  *regexp.Regexp // generated from Filter
	LastAddedPos int            // when Mode is ModeAdd this is set after adding a dose
	Show         int
	RmPosition   int
	Timezone     string
}

func (d *DisplayOptions) Parse() {
	var mode Mode
	switch {
	case *optAdd:
		mode = ModeAdd
	case *optRm:
		mode = ModeRm
	case *optRmP > -1:
		mode = ModeRmPosition
	case *aChangeTz != "":
		mode = ModeTzChange
	case *aConvTz != "":
		mode = ModeTzConvert
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

	timezone := ""
	switch {
	case *aChangeTz != "":
		timezone = *aChangeTz
	case *aConvTz != "":
		timezone = *aConvTz
	case *aTimezone != "":
		timezone = *aTimezone
	}

	options = &DisplayOptions{
		Mode:         mode,
		Json:         *optJ,
		Unix:         *optU,
		DotTime:      *optT,
		IgnoreNotes:  *optNts,
		Reversed:     *optR,
		StartAtTop:   *optS,
		FilterInvert: *optV,
		Filter:       *optG,
		//FilterRegex: set after Parse(),
		LastAddedPos: -1,
		Show:         showLast,
		RmPosition:   *optRmP,
		Timezone:     timezone,
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
	timeZero := time.Unix(0, 0)
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
	if !options.IgnoreNotes && d.Note != "" {
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

// DoseUnitSize represents a dose unit as a factor of micrograms equivalent
type DoseUnitSize int64

const (
	DoseUnitSizeDefault    DoseUnitSize = 0
	DoseUnitSizeMicrogram  DoseUnitSize = 1
	DoseUnitSizeMilliliter DoseUnitSize = -1 // TODO: FIX
	DoseUnitSizeMilligram  DoseUnitSize = 1000
	DoseUnitSizeGram       DoseUnitSize = 1000 * 1000
	DoseUnitSizeKilogram   DoseUnitSize = 1000 * 1000 * 1000
	DoseUnitSizeAlcohol    DoseUnitSize = DoseUnitSizeEthanol / 10 // 1u  = 0.1mL of EtOH = 1 SI unit of Alcohol
	DoseUnitSizeEthanol    DoseUnitSize = 789.45 * 1000            // 1mL = 789.45mg EtOH at 20°C * to get micrograms
	DoseUnitSizeGHB        DoseUnitSize = 1120.0 * 1000            // 1mL = 1120.0mg of GHB at 25°C * to get μg
	DoseUnitSizeGBL        DoseUnitSize = 1129.6 * 1000            // 1mL = 1129.6mg of GBL at 20°C * to get μg
	DoseUnitSizeBDO        DoseUnitSize = 1017.3 * 1000            // 1mL = 1017.3mg of 1,4-BDO at 25°C * to get μg
)

func (u DoseUnitSize) String() string {
	switch u {
	case DoseUnitSizeMicrogram:
		return "μg"
	case DoseUnitSizeMilligram, DoseUnitSizeGHB:
		return "mg"
	case DoseUnitSizeGram:
		return "g"
	case DoseUnitSizeKilogram:
		return "kg"
	case DoseUnitSizeEthanol, DoseUnitSizeGBL, DoseUnitSizeBDO:
		return "mL"
	case DoseUnitSizeMilliliter, DoseUnitSizeAlcohol:
		return "u"
	default:
		return ""
	}
}

func (u DoseUnitSize) F() float64 {
	return float64(u)
}

type DoseStat struct {
	Drug         string
	TotalDoses   int64
	TotalAmount  float64 // in micrograms
	UnitLabel    string  // See UnitOrLabel(): only set if no unit is known
	Unit         DoseUnitSize
	OriginalUnit DoseUnitSize
}

// ToUnit converts the TotalAmount to a new DoseUnitSize
// TODO: Generify and use in normal doses
func (s *DoseStat) ToUnit(u DoseUnitSize) {
	if u == DoseUnitSizeDefault || u == s.Unit {
		return
	}

	if s.Unit == DoseUnitSizeMicrogram {
		if s.Unit < u {
			s.TotalAmount = s.TotalAmount / u.F()
		} else {
			s.TotalAmount = s.TotalAmount * u.F()
		}
	} else {
		if s.Unit < u {
			s.TotalAmount = s.TotalAmount * s.Unit.F() / u.F()
		} else {
			s.TotalAmount = s.TotalAmount / s.Unit.F() * u.F()
		}
	}

	s.Unit = u
}

// ToSensibleUnit will convert to a larger weight unit if it's value is larger than 1000.
func (s *DoseStat) ToSensibleUnit() {
	for _, u := range []DoseUnitSize{DoseUnitSizeMicrogram, DoseUnitSizeMilligram, DoseUnitSizeGram} {
		switch s.Unit {
		case u:
			if s.TotalAmount >= 1000 {
				s.ToUnit(u * 1000)
			}
		}
	}
}

// UnitOrLabel will return Unit as a string, or UnitLabel if it's Unit is not known.
func (s *DoseStat) UnitOrLabel() string {
	if s.Unit.String() != "" {
		return s.Unit.String()
	}

	return s.UnitLabel
}

func ParseUnit(d, u string) DoseUnitSize {
unit:
	switch u {
	case "", "?":
		break unit
	case "u":
		switch d {
		case "Alcohol":
			return DoseUnitSizeAlcohol
		default:
			break unit
		}
	case "mL":
		switch d {
		case "EtOH", "Ethanol":
			return DoseUnitSizeEthanol
		case "GHB":
			return DoseUnitSizeGHB
		case "GBL":
			return DoseUnitSizeGBL
		case "BDO":
			return DoseUnitSizeBDO
		default:
			return DoseUnitSizeMilliliter
		}
	case "kg":
		return DoseUnitSizeKilogram
	case "g":
		return DoseUnitSizeGram
	case "mg":
		return DoseUnitSizeMilligram
	default:
		return DoseUnitSizeMicrogram
	}

	return DoseUnitSizeDefault
}

func (s *DoseStat) Format(n1, n2 int) string {
	offset := 0
	if strings.ContainsAny(s.UnitOrLabel(), "μµ") {
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
	) + s.UnitOrLabel()

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

	// ModeGet, ModeTzChange, ModeTzConvert, ModeStatTop, ModeStatAvg
	// We do not filter in ModeRm and ModeAdd for performance reasons
	if options.Filter != "" {
		if options.Mode == ModeSave {
			fmt.Printf("-g is set but mode is %s, filter will be ignored!\n", options.Mode)
		} else {
			if filter, err := regexp.Compile(fmt.Sprintf("(?i)%s", options.Filter)); err != nil {
				fmt.Printf("-g is set but failed to compile regex: %s\n", err)
				return
			} else {
				options.FilterRegex = filter
			}
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
		saveFileWrapper(doses, true)
	case ModeGet:
		fmt.Printf("%s", getDosesFmt(doses))
	case ModeRm:
		if len(doses) == 0 {
			fmt.Printf("`%s` is set but there are no doses to remove?\n", ModeRm)
			return
		}

		pos, posIndex := lastPosition(doses)

		// No doses found, shouldn't be possible but try anyway?
		if pos == -1 || posIndex == -1 {
			doses = SliceRemoveIndex(doses, len(doses)-1)
		} else if len(doses) > posIndex {
			doses = SliceRemoveIndex(doses, posIndex)
		}

		if !saveFileWrapper(doses, false) {
			return
		}

		fmt.Printf("%s", getDosesFmt(doses))
	case ModeRmPosition:
		if len(doses) == 0 {
			fmt.Printf("`%s` is set but there are no doses to remove?\n", ModeRmPosition)
			return
		}

		posIndex := -1
		for n, d := range doses {
			if d.Position == options.RmPosition {
				posIndex = n
			}
		}

		if posIndex == -1 {
			fmt.Printf("`%s`: couldn't find dose matching position \"%v\"\n", ModeRmPosition, options.RmPosition)
			return
		}

		doses = SliceRemoveIndex(doses, posIndex)

		if !saveFileWrapper(doses, false) {
			return
		}

		fmt.Printf("%s", getDosesFmt(doses))
	case ModeAdd:
		// Ensure -drug is set
		if *aDrug == "" {
			fmt.Printf("`-drug` is not set!\n")
			return
		} else {
			*aDrug = caseFmt(*aDrug)
		}

		//
		// Get timezone from most chronologically-recent dose, if flag isn't set
		if options.Timezone == "" {
			if len(doses) > 0 {
				options.Timezone = doses[len(doses)-1].Timezone
			} else {
				fmt.Printf("`-timezone` is not set and no doses with a timezone were found! You must set a timezone to add doses first\n")
				return
			}
		}

		loc, err := time.LoadLocation(options.Timezone)
		if err != nil {
			fmt.Printf("`%s`: failed to load location: %v\n", ModeAdd, err)
			return
		}

		//
		// Parse provided -date and -time flags, using pre-defined valid layouts
		t := time.Now().In(loc)
		pDate := "00000101"
		pTime := "0000"

		switch len(*aDate) {
		case 5: // 2006-01-02
			pDate = t.Format("2006-") + *aDate
		case 4: // 20060102
			pDate = t.Format("2006") + *aDate
		case 0: // 20060102
			pDate = t.Format("20060102")
		default: // determined by user, will try to parse
			pDate = *aDate
		}

		switch len(*aTime) {
		case 0: // 1504
			pTime = t.Format("1504")
		default: // determined by user, will try to parse
			pTime = *aTime
		}

		parseLayout := func(p string, l *TimestampLayout) (*time.Time, error) {
			for _, f := range l.Formats {
				// faster than waiting for time.ParseInLocation to fail
				if len(p) != len(f) {
					continue
				}

				if ts, err := time.ParseInLocation(
					fmt.Sprintf("%s%s%s", l.Layout.Prefix, f, l.Layout.Suffix),
					fmt.Sprintf("%s%s%s", l.Value.Prefix, p, l.Value.Suffix),
					loc,
				); err == nil {
					return &ts, nil
				}
			}

			return nil, errors.New(fmt.Sprintf(
				"`%s`: failed to parse \"%s\" using layouts: %s",
				ModeAdd, p, strings.Join(strings.Fields(fmt.Sprint(l.Formats)), ", "),
			))
		}

		// Parse -date flag, using 00:00 as the suffix
		if ts, err := parseLayout(pDate, &TimestampLayout{
			[]LayoutFormat{"2006/01/02", "2006-01-02", "01/02/2006", "01-02-2006", "20060102", "01-02", "0102"},
			WrapFormat{Suffix: "1504"}, WrapFormat{Suffix: "0000"},
		}); err != nil {
			fmt.Printf("%v\n", err)
			return
		} else {
			t = *ts
		}

		// Parse -time flag, using the date we found as a prefix
		if ts, err := parseLayout(pTime, &TimestampLayout{
			[]LayoutFormat{"3:04pm", "15:04", "3:04", "1504"},
			WrapFormat{Prefix: "20060102"}, WrapFormat{Prefix: t.Format("20060102")},
		}); err != nil {
			fmt.Printf("%v\n", err)
			return
		} else {
			t = *ts
		}

		//
		// Parse -a and -d flags for dosage and drug
		// Replace mathematical symbols in dosage with their greek variation:
		dosage := *aDosage
		dosage = strings.ReplaceAll(dosage, "µ", "μ") // U+00B5 → U+03BC
		dosage = strings.ReplaceAll(dosage, "∆", "Δ") // U+2206 → U+0394

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

		pos, _ := lastPosition(doses)
		dose := Dose{
			Position:  pos + 1,
			Timestamp: t,
			Timezone:  options.Timezone,
			Date:      t.Format("2006/01/02"),
			Time:      t.Format("15:04"),
			Dosage:    dosage,
			Drug:      *aDrug,
			RoA:       *aRoa,
			Note:      *aNote,
		}

		doses = append(doses, dose)
		options.LastAddedPos = dose.Position

		// Re-sort by chronological date and time, to handle adding a dose in the past
		sort.Slice(doses, func(i, j int) bool {
			return doses[i].Timestamp.Unix() < doses[j].Timestamp.Unix()
		})

		if !saveFileWrapper(doses, false) {
			return
		}

		fmt.Printf("%s", getDosesFmt(doses))
	case ModeTzChange, ModeTzConvert:
		if len(doses) == 0 {
			fmt.Printf("`%s` is set but there are no doses to modify?\n", options.Mode)
			return
		}

		loc, err := time.LoadLocation(options.Timezone)
		if err != nil {
			fmt.Printf("`%s`: failed to load location: %v\n", ModeAdd, err)
			return
		}

		dosesFiltered := getDosesOptions(doses, options)
		dosePositions := make(map[string]int) // [position]index

		for n, d := range doses {
			dosePositions[strconv.Itoa(d.Position)] = n
		}

		for _, d := range dosesFiltered {
			switch options.Mode {
			case ModeTzChange:
				d.Timestamp = time.Date(
					d.Timestamp.Year(), d.Timestamp.Month(), d.Timestamp.Day(),
					d.Timestamp.Hour(), d.Timestamp.Minute(), d.Timestamp.Second(), d.Timestamp.Nanosecond(),
					loc)
				d.Timezone = options.Timezone
			case ModeTzConvert:
				d.Timestamp = d.Timestamp.In(loc)
				d.Timezone = options.Timezone
				d.Date = d.Timestamp.Format("2006/01/02")
				d.Time = d.Timestamp.Format("15:04")
			default:
				fmt.Printf("`%s`: modifying dose in non-supported mode?? how?\n", options.Mode)
				return
			}

			if n, ok := dosePositions[strconv.Itoa(d.Position)]; ok {
				doses[n] = d
			}
		}

		if !saveFileWrapper(doses, false) {
			return
		}

		fmt.Printf("%s", getDosesFmt(doses))
	case ModeStatTop, ModeStatAvg:
		doses = getDosesOptions(doses, options)

		stats := make(map[string]DoseStat)
		statTotal := DoseStat{Drug: "Total"}

		if options.Mode == ModeStatAvg {
			statTotal.Drug = "Average"
		}

		//
		// stat.TotalAmount is in MICROGRAMS right now
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

			// Get unitSize for current dose
			unitLabel := units[3]
			unitSize := ParseUnit(stat.Drug, unitLabel)
			stat.Unit = unitSize

			// Only set original unit if it hasn't been set before
			if stat.OriginalUnit == DoseUnitSizeDefault {
				stat.OriginalUnit = unitSize

				if stat.UnitLabel == "" {
					stat.UnitLabel = unitLabel
				}
			}

			// Nothing else to do, skip
			if amount == 0 {
				stats[d.Drug] = stat
				continue
			}

			// We want to set the unit size of this stat if it isn't default.
			// We also want to set total specifically here, in case we have a scenario where no doses have any units to go off of
			if unitSize != DoseUnitSizeDefault {
				stat.Unit = DoseUnitSizeMicrogram
				statTotal.Unit = DoseUnitSizeMicrogram
				statTotal.OriginalUnit = DoseUnitSizeMicrogram

				// Convert amount to micrograms, set unit, so it is converted back to original later
				amount = amount * unitSize.F()
			} else if statTotal.UnitLabel == "" {
				statTotal.UnitLabel = unitLabel // Add a fallback label if it is a default unit size
			}

			stat.TotalAmount += amount
			statTotal.TotalAmount += amount
			stats[d.Drug] = stat
		}

		//
		// go through each stat and convert smaller units to larger ones when appropriate
		statsOrdered := make([]DoseStat, 0)
		for _, v := range stats {
			statsOrdered = append(statsOrdered, v)
		}

		// Sort by total doses
		// If total doses is the same, sort by total amount
		// If total amount is the same, sort by drug name being alphabetical
		// If drug name starts with a unicode character, sort first
		// Always ensures that the Total / Average stat is always at the bottom
		sort.SliceStable(statsOrdered, func(i, j int) bool {
			if statsOrdered[i].TotalDoses == statsOrdered[j].TotalDoses {
				if statsOrdered[i].TotalAmount*statsOrdered[i].Unit.F() == statsOrdered[j].TotalAmount*statsOrdered[j].Unit.F() {
					greekI := unicode.Is(unicode.Greek, []rune(statsOrdered[i].Drug)[0])
					greekJ := unicode.Is(unicode.Greek, []rune(statsOrdered[j].Drug)[0])
					if greekI && !greekJ {
						return true
					}
					if !greekI && greekJ {
						return false
					}

					return strings.Compare(statsOrdered[i].Drug, statsOrdered[j].Drug) <= 0
				}

				return statsOrdered[i].TotalAmount*statsOrdered[i].Unit.F() < statsOrdered[j].TotalAmount*statsOrdered[j].Unit.F()
			}

			return statsOrdered[i].TotalDoses < statsOrdered[j].TotalDoses
		})

		statsOrdered = append(statsOrdered, statTotal)

		// stat.TotalAmount is in MICROGRAMS right now
		for k, v := range statsOrdered {
			// convert total amount in MICROGRAMS to correct unit
			v.ToUnit(v.OriginalUnit)

			// convert total amount to average amount
			if options.Mode == ModeStatAvg {
				v.TotalAmount = v.TotalAmount / float64(v.TotalDoses)
			}

			// convert from micrograms to larger units if too big
			v.ToSensibleUnit()

			statsOrdered[k] = v
		}
		// stat.TotalAmount is **NOT IN MICROGRAMS ANYMORE**

		// get the longest len to use for spacing later, format lines
		highestLen := len(fmt.Sprintf("%v", statTotal.TotalDoses)) + 1
		lines := ""

		for _, s := range statsOrdered {
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
		sr := []rune(s)

		for i, c := range sr {
			if unicode.Is(unicode.Greek, c) {
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

func lastPosition(doses []Dose) (int, int) {
	pos, posIndex := -1, -1

	for n, d := range doses {
		if d.Position > pos {
			pos = d.Position
			posIndex = n
		}
	}

	return pos, posIndex
}

func saveFileWrapper(doses []Dose, printSuccess bool) bool {
	ok, u := saveDoseFiles(doses)

	if !ok || printSuccess {
		fmt.Printf("`%s`: saved files:\n- %s\n", options.Mode, strings.Join(u, "\n- "))
	}

	if !ok {
		fmt.Printf("`%s`: failed to save one or more doses files!\n", options.Mode)
	}

	return ok
}

func saveDoseFiles(doses []Dose) (r bool, p []string) {
	optionsJson := &DisplayOptions{Json: true}
	optionsTxt := &DisplayOptions{
		DotTime:    true,
		Reversed:   true,
		StartAtTop: true,
	}

	if content, err := getDosesFmtOptions(doses, optionsJson); err == nil {
		if ok, u := saveFile(content, *dosesUrl); ok {
			p = append(p, u)
		} else {
			return
		}

		// Don't try to save a .txt if saving the main db failed, we don't want to imply to the user that the db is fine
		if content, err := getDosesFmtOptions(doses, optionsTxt); err == nil {
			if ok, u := saveFile(content, strings.TrimSuffix(*dosesUrl, ".json")+".txt"); ok {
				r = ok
				p = append(p, u)
			}
		}
	}

	return
}

func saveFile(content string, path string) (r bool, u string) {
	if *urlToken == "" {
		fmt.Printf("`-token` not set!\n")
		return
	}

	u = strings.Replace(path, "media/", "public/media/", 1)

	req, err := http.NewRequest("POST", u, strings.NewReader(url.Values{"content": {content}}.Encode()))
	if err != nil {
		fmt.Printf("failed to make new request: %v\n", err)
		return
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Auth", *urlToken)
	response, err := client.Do(req)
	if err != nil {
		fmt.Printf("error posting body: %v\n%s\n", err, content)
		return
	}

	if response.StatusCode != 200 {
		defer response.Body.Close()
		b, err := io.ReadAll(response.Body)
		if err != nil {
			fmt.Printf("failed to read body (code %v): %v\n", response.StatusCode, err)
			return
		}

		fmt.Printf("status code was %v:\n%s\n", response.StatusCode, b)
		return
	}

	return true, u
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
			if (options.LastAddedPos != -1 && options.LastAddedPos == d.Position) ||
				options.FilterInvert != options.FilterRegex.MatchString(d.StringOptions(options)) {
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
