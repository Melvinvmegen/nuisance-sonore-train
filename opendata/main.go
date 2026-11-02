// Solution de secours sans clé API : passages à Paris Gare de Lyon depuis l'open data SNCF et Trenitalia.
//
//   - Horaires théoriques (GTFS, mis à jour chaque jour) :
//     SNCF Voyageurs (TGV INOUI, OUIGO, Intercités, TER, Lyria), Transilien (RER D, ligne R…,
//     les TER de ce fichier sont ignorés : déjà dans le flux SNCF) et Trenitalia France (Frecciarossa).
//   - Temps réel (GTFS-RT trip updates, ~toutes les 2 min) : retards et suppressions.
//     D'après la SNCF, seuls TGV et Intercités sont garantis ; les autres trains restent souvent à l'heure prévue.
//     Trenitalia publie son propre flux (identifiants de trajet identiques à son GTFS).
//
// Écrit un JSON au même format que les trains renvoyés par public/data.js (fetchKind), pour la journée
// en cours (00h → 03h le lendemain, heure de Paris). Bibliothèque standard Go uniquement, binaire statique.
//
//	opendata --out data/opendata.json              # une fois
//	opendata --out data/opendata.json --loop 120   # en continu (toutes les 2 min)
package main

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // fuseau Europe/Paris intégré au binaire (pas besoin de tzdata dans l'image)
)

const (
	userAgent      = "rails-semprun/1.0 (+https://github.com/Melvinvmegen/nuisance-sonore-train)"
	staticMaxAge   = 6 * time.Hour // re-télécharge les GTFS au plus toutes les 6 h (publiés 1×/jour)
	sncfGDL        = "87686006"    // code UIC de Paris Gare de Lyon (grandes lignes)
	transilienGDL  = "IDFM:73626"  // zone d'arrêt Gare de Lyon dans le GTFS Transilien
	trenitaliaGDL  = "10007"       // Paris-Gare-de-Lyon dans le GTFS Trenitalia France
	cosLat         = 0.658         // cos(48,84°) : degrés de longitude → distance
	streetDirNorth = -0.40         // vecteur (nord, est) unitaire : gare → voies qui longent la rue (sud-est)
	streetDirEast  = 0.92
)

var (
	// rt : flux temps réel dont les trajets du GTFS relèvent (voir rtURLs)
	feeds = []struct{ name, url, rt string }{
		{"sncf", "https://eu.ftp.opendatasoft.com/sncf/plandata/Export_OpenData_SNCF_GTFS_NewTripId.zip", "sncf"},
		{"transilien", "https://eu.ftp.opendatasoft.com/sncf/gtfs/transilien-gtfs.zip", "sncf"},
		{"trenitalia", "https://www.data.gouv.fr/api/1/datasets/r/bdecea2c-ebc9-4f22-812d-927e4a2e4bad", "trenitalia"},
	}
	rtURLs = []struct{ name, url string }{
		{"sncf", "https://proxy.transport.data.gouv.fr/resource/sncf-gtfs-rt-trip-updates"},
		{"trenitalia", "https://proxy.transport.data.gouv.fr/resource/trenitalia-gtfs-rt"},
	}
	sncfModes          = map[string]string{"TGV INOUI": "TGV INOUI", "OUIGO": "OUIGO", "INTERCITES": "Intercités", "Train TER": "TER", "Lyria": "TGV Lyria"}
	transilienAgencies = map[string]bool{"RER": true, "Transilien": true}
	excludeLines       = map[string]bool{"RER A": true} // souterrain, ne longe pas la rue (comme public/data.js)
	gdlCoord           = [2]float64{48.8443, 2.3739}
	shortRTID          = regexp.MustCompile(`^OCESN(\d+)F`)
	paris              *time.Location
	client             = &http.Client{}
)

func logf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "%s [opendata] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, a...))
}

func httpGet(url string, timeout time.Duration) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	c := *client
	c.Timeout = timeout
	r, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", r.StatusCode)
	}
	return io.ReadAll(r.Body)
}

// ---------- Horaires théoriques ----------

type stop struct {
	Seq, Arr, Dep int
	ID            string
}

type trip struct {
	ID                 string // identifiant GTFS (identique au temps réel quand il correspond)
	Mode, Line, Number string
	Color              *string
	Dates              []string // dates de circulation (AAAAMMJJ) parmi les jours utiles
	GDL                int      // index de Gare de Lyon dans Stops
	Kinds              []string // passages comptés : arrival et/ou departure
	Origin, Terminus   string
	Stops              []stop
	service            string
	rt                 string         // flux temps réel à consulter
	loc                *time.Location // fuseau des heures GTFS (Trenitalia : UTC, d'après son temps réel)
	seqIdx             map[int]int    // Trenitalia : stop_sequence d'origine → Seq renumérotée (voir buildIndex)
}

// rows lit un CSV du zip ligne à ligne ; get(col) renvoie "" pour une colonne absente.
func rows(z *zip.Reader, name string, fn func(get func(string) string) error) error {
	var f *zip.File
	for _, x := range z.File {
		if x.Name == name {
			f = x
		}
	}
	if f == nil {
		return os.ErrNotExist
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	r := csv.NewReader(rc)
	r.FieldsPerRecord, r.LazyQuotes, r.ReuseRecord = -1, true, true
	head, err := r.Read()
	if err != nil {
		return err
	}
	cols := map[string]int{}
	for i, h := range head {
		cols[strings.TrimPrefix(h, "\ufeff")] = i
	}
	var rec []string
	get := func(c string) string {
		if i, ok := cols[c]; ok && i < len(rec) {
			return rec[i]
		}
		return ""
	}
	for {
		rec, err = r.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := fn(get); err != nil {
			return err
		}
	}
}

func hasFile(z *zip.Reader, name string) bool {
	for _, f := range z.File {
		if f.Name == name {
			return true
		}
	}
	return false
}

func gtfsSeconds(s string) int {
	p := strings.Split(s, ":")
	if len(p) != 3 {
		return 0
	}
	h, _ := strconv.Atoi(p[0])
	m, _ := strconv.Atoi(p[1])
	sec, _ := strconv.Atoi(p[2])
	return h*3600 + m*60 + sec
}

func streetSide(c [2]float64) bool {
	north, east := c[0]-gdlCoord[0], (c[1]-gdlCoord[1])*cosLat
	return north*streetDirNorth+east*streetDirEast > 0
}

// passageKinds : un train qui traverse la gare (RER D) ne longe la rue qu'une fois,
// avant son arrivée s'il vient du sud-est, après son départ s'il y repart.
func passageKinds(stops []stop, i int, coords map[string][2]float64) []string {
	if i == 0 {
		return []string{"departure"}
	}
	if i == len(stops)-1 {
		return []string{"arrival"}
	}
	prev, okP := coords[stops[i-1].ID]
	next, okN := coords[stops[i+1].ID]
	if okP && okN {
		p, n := streetSide(prev), streetSide(next)
		if p && !n {
			return []string{"arrival"}
		}
		if n && !p {
			return []string{"departure"}
		}
	}
	return []string{"arrival", "departure"} // cas indéterminé : on garde les deux
}

// buildIndex : trajets passant à Gare de Lyon, avec leurs dates de circulation parmi days.
func buildIndex(feed, rtFeed, path string, days []time.Time) ([]*trip, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	z := &zr.Reader

	names, coords, gdl := map[string]string{}, map[string][2]float64{}, map[string]bool{}
	err = rows(z, "stops.txt", func(get func(string) string) error {
		id := get("stop_id")
		names[id] = get("stop_name")
		lat, e1 := strconv.ParseFloat(get("stop_lat"), 64)
		lon, e2 := strconv.ParseFloat(get("stop_lon"), 64)
		if e1 == nil && e2 == nil {
			coords[id] = [2]float64{lat, lon}
		}
		if (feed == "sncf" && strings.HasSuffix(id, "-"+sncfGDL)) || (feed == "transilien" && get("parent_station") == transilienGDL) ||
			(feed == "trenitalia" && id == trenitaliaGDL) {
			gdl[id] = true
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("stops.txt : %w", err)
	}

	agencies := map[string]string{}
	if err := rows(z, "agency.txt", func(get func(string) string) error {
		agencies[get("agency_id")] = get("agency_name")
		return nil
	}); err != nil {
		return nil, fmt.Errorf("agency.txt : %w", err)
	}
	type route struct{ id, agency, short, color string }
	routes := map[string]route{}
	if err := rows(z, "routes.txt", func(get func(string) string) error {
		routes[get("route_id")] = route{get("route_id"), get("agency_id"), get("route_short_name"), get("route_color")}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("routes.txt : %w", err)
	}

	// Passe 1 : trajets qui desservent Gare de Lyon
	gdlTrips := map[string]bool{}
	if err := rows(z, "stop_times.txt", func(get func(string) string) error {
		if gdl[get("stop_id")] {
			gdlTrips[get("trip_id")] = true
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("stop_times.txt : %w", err)
	}

	trips, order := map[string]*trip{}, []string{}
	if err := rows(z, "trips.txt", func(get func(string) string) error {
		id := get("trip_id")
		if !gdlTrips[id] {
			return nil
		}
		r := routes[get("route_id")]
		t := &trip{ID: id, service: get("service_id"), rt: rtFeed, loc: paris}
		switch feed {
		case "trenitalia":
			// Ni numéro ni destination dans trips.txt : route_id porte le numéro commercial (6653, 9287…),
			// une seule route par horaire, un seul trajet par (route, date) → identifiant stable d'un jour à l'autre.
			t.Mode, t.Number, t.loc = "Frecciarossa", r.id, time.UTC
			if r.color == "" {
				r.color = "c00000" // comme public/data.js
			}
		case "transilien":
			agency := agencies[r.agency]
			if !transilienAgencies[agency] {
				return nil
			}
			t.Mode, t.Line, t.Number = agency, r.short, get("trip_short_name")
			if t.Number == "" {
				t.Number = get("trip_headsign")
			}
			if excludeLines[strings.TrimSpace(t.Mode+" "+t.Line)] {
				return nil
			}
		default:
			t.Number = get("trip_headsign") // mode déduit de l'arrêt (ci-dessous)
		}
		if r.color != "" {
			c := "#" + r.color
			t.Color = &c
		}
		trips[id] = t
		order = append(order, id)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("trips.txt : %w", err)
	}

	// Passe 2 : tous les arrêts de ces trajets (origine, terminus, retards propagés)
	if err := rows(z, "stop_times.txt", func(get func(string) string) error {
		t := trips[get("trip_id")]
		if t == nil {
			return nil
		}
		seq, _ := strconv.Atoi(get("stop_sequence"))
		t.Stops = append(t.Stops, stop{seq, gtfsSeconds(get("arrival_time")), gtfsSeconds(get("departure_time")), get("stop_id")})
		return nil
	}); err != nil {
		return nil, fmt.Errorf("stop_times.txt : %w", err)
	}

	// Dates de circulation (calendar + calendar_dates), limitées aux jours utiles
	wanted := map[string]bool{}
	for _, d := range days {
		wanted[d.Format("20060102")] = true
	}
	active := map[string]map[string]bool{}
	for _, t := range trips {
		active[t.service] = map[string]bool{}
	}
	if hasFile(z, "calendar.txt") {
		if err := rows(z, "calendar.txt", func(get func(string) string) error {
			a := active[get("service_id")]
			if a == nil {
				return nil
			}
			for _, d := range days {
				ds := d.Format("20060102")
				if get("start_date") <= ds && ds <= get("end_date") && get(strings.ToLower(d.Weekday().String())) == "1" {
					a[ds] = true
				}
			}
			return nil
		}); err != nil {
			return nil, fmt.Errorf("calendar.txt : %w", err)
		}
	}
	if err := rows(z, "calendar_dates.txt", func(get func(string) string) error {
		a, date := active[get("service_id")], get("date")
		if a != nil && wanted[date] {
			if get("exception_type") == "1" {
				a[date] = true
			} else {
				delete(a, date)
			}
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("calendar_dates.txt : %w", err)
	}

	out := []*trip{}
	for _, id := range order {
		t := trips[id]
		for d := range active[t.service] {
			t.Dates = append(t.Dates, d)
		}
		if len(t.Dates) == 0 || len(t.Stops) == 0 {
			continue
		}
		sort.Strings(t.Dates)
		if feed == "trenitalia" {
			// stop_sequence parfois incohérent (ex. 9281 : Oulx 5, Torino 24, Milano 38, Paris 39…) : ordre par heure,
			// séquences renumérotées ; le temps réel (par stop_sequence d'origine) passe par seqIdx.
			sort.SliceStable(t.Stops, func(i, j int) bool { return t.Stops[i].Arr < t.Stops[j].Arr })
			t.seqIdx = map[int]int{}
			for i := range t.Stops {
				t.seqIdx[t.Stops[i].Seq] = i
				t.Stops[i].Seq = i
			}
		} else {
			sort.Slice(t.Stops, func(i, j int) bool { return t.Stops[i].Seq < t.Stops[j].Seq })
		}
		t.GDL = -1
		for i, s := range t.Stops {
			if gdl[s.ID] {
				t.GDL = i
				break
			}
		}
		if t.GDL < 0 {
			continue
		}
		if t.Mode == "" { // SNCF : "StopPoint:OCETGV INOUI-87686006" → "TGV INOUI"
			raw := t.Stops[t.GDL].ID
			if i := strings.Index(raw, "OCE"); i >= 0 {
				raw = raw[i+3:]
			}
			if i := strings.LastIndex(raw, "-"); i >= 0 {
				raw = raw[:i]
			}
			t.Mode = raw
			if m, ok := sncfModes[raw]; ok {
				t.Mode = m
			}
		}
		t.Kinds = passageKinds(t.Stops, t.GDL, coords)
		t.Origin, t.Terminus = names[t.Stops[0].ID], names[t.Stops[len(t.Stops)-1].ID]
		out = append(out, t)
	}
	return out, nil
}

// ensureZip télécharge le GTFS s'il manque ou a plus de staticMaxAge ; renvoie true s'il a changé.
func ensureZip(url, path string) (bool, error) {
	if st, err := os.Stat(path); err == nil && time.Since(st.ModTime()) < staticMaxAge {
		return false, nil
	}
	logf("téléchargement %s…", filepath.Base(path))
	data, err := httpGet(url, 5*time.Minute)
	if err != nil {
		return false, err
	}
	if _, err := zip.NewReader(bytes.NewReader(data), int64(len(data))); err != nil {
		return false, fmt.Errorf("zip invalide : %w", err)
	}
	if err := os.WriteFile(path+".tmp", data, 0o644); err != nil {
		return false, err
	}
	return true, os.Rename(path+".tmp", path)
}

// static garde l'index en mémoire ; reconstruit un flux si son GTFS change ou si le jour change.
type static struct {
	day   string
	feeds map[string][]*trip
}

func (s *static) load(cacheDir string, days []time.Time) ([]*trip, error) {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, err
	}
	day := days[1].Format("20060102")
	rebuilt := false
	for _, f := range feeds {
		path := filepath.Join(cacheDir, f.name+".zip")
		changed, err := ensureZip(f.url, path)
		if err != nil {
			logf("GTFS %s indisponible : %v", f.name, err) // on garde le zip ou l'index déjà connus
		}
		if _, statErr := os.Stat(path); statErr != nil || (!changed && s.feeds[f.name] != nil && s.day == day) {
			continue
		}
		t, err := buildIndex(f.name, f.rt, path, days)
		if err != nil {
			logf("GTFS %s illisible : %v", f.name, err)
			continue
		}
		logf("GTFS %s : %d trajets à Gare de Lyon", f.name, len(t))
		s.feeds[f.name] = t
		rebuilt = true
	}
	if rebuilt {
		s.day = day
	}
	var all []*trip
	for _, f := range feeds {
		all = append(all, s.feeds[f.name]...)
	}
	if len(all) == 0 {
		return nil, errors.New("aucun horaire théorique disponible")
	}
	return all, nil
}

// ---------- Temps réel : décodeur GTFS-RT minimal (protobuf) ----------

type event struct {
	hasDelay, hasTime bool
	delay, time       int64
}

type stopUpdate struct {
	seq      int
	hasSeq   bool
	stop     string
	arr, dep event
	skipped  bool
}

type tripUpdate struct {
	date     string
	canceled bool
	stops    []stopUpdate
}

func varint(b []byte, i int) (uint64, int, error) {
	var x uint64
	for s := uint(0); s < 64; s += 7 {
		if i >= len(b) {
			return 0, i, io.ErrUnexpectedEOF
		}
		c := b[i]
		i++
		x |= uint64(c&0x7f) << s
		if c < 0x80 {
			return x, i, nil
		}
	}
	return 0, i, errors.New("protobuf : varint trop long")
}

// fields parcourt les champs d'un message protobuf : (numéro, valeur varint, octets si longueur délimitée).
func fields(b []byte, fn func(f int, v uint64, bs []byte) error) error {
	for i := 0; i < len(b); {
		tag, j, err := varint(b, i)
		if err != nil {
			return err
		}
		i = j
		f, w := int(tag>>3), tag&7
		var v uint64
		var bs []byte
		switch w {
		case 0:
			if v, i, err = varint(b, i); err != nil {
				return err
			}
		case 2:
			n, j, err := varint(b, i)
			if err != nil || j+int(n) > len(b) {
				return io.ErrUnexpectedEOF
			}
			bs, i = b[j:j+int(n)], j+int(n)
		case 1:
			i += 8
		case 5:
			i += 4
		default:
			return fmt.Errorf("protobuf : type %d non géré", w)
		}
		if i > len(b) {
			return io.ErrUnexpectedEOF
		}
		if err := fn(f, v, bs); err != nil {
			return err
		}
	}
	return nil
}

func parseEvent(b []byte) (e event, err error) { // StopTimeEvent : 1 delay (s), 2 time (epoch)
	err = fields(b, func(f int, v uint64, _ []byte) error {
		switch f {
		case 1:
			e.hasDelay, e.delay = true, int64(v) // int32/int64 : complément à 2 sur 64 bits
		case 2:
			e.hasTime, e.time = true, int64(v)
		}
		return nil
	})
	return
}

func parseTripUpdates(data []byte) (map[string]*tripUpdate, int64, error) {
	out, stamp := map[string]*tripUpdate{}, int64(0)
	err := fields(data, func(f int, _ uint64, msg []byte) error {
		switch f {
		case 1: // header
			return fields(msg, func(hf int, hv uint64, _ []byte) error {
				if hf == 3 {
					stamp = int64(hv)
				}
				return nil
			})
		case 2: // entity
			return fields(msg, func(ef int, _ uint64, ev []byte) error {
				if ef != 3 { // trip_update
					return nil
				}
				tu, tripID := &tripUpdate{}, ""
				err := fields(ev, func(tf int, _ uint64, tv []byte) error {
					switch tf {
					case 1: // TripDescriptor
						return fields(tv, func(df int, dv uint64, ds []byte) error {
							switch {
							case df == 1:
								tripID = string(ds)
							case df == 3:
								tu.date = string(ds)
							case df == 4 && dv == 3: // CANCELED
								tu.canceled = true
							}
							return nil
						})
					case 2: // StopTimeUpdate
						var st stopUpdate
						err := fields(tv, func(sf int, sv uint64, ss []byte) error {
							var err error
							switch {
							case sf == 1:
								st.seq, st.hasSeq = int(sv), true
							case sf == 4:
								st.stop = string(ss)
							case sf == 2:
								st.arr, err = parseEvent(ss)
							case sf == 3:
								st.dep, err = parseEvent(ss)
							case sf == 5 && sv == 1: // SKIPPED
								st.skipped = true
							}
							return err
						})
						tu.stops = append(tu.stops, st)
						return err
					}
					return nil
				})
				if tripID != "" {
					out[tripID] = tu
				}
				return err
			})
		}
		return nil
	})
	return out, stamp, err
}

// serviceBase : origine des heures GTFS = midi - 12 h dans le fuseau du flux (correct les jours de changement d'heure).
// Les heures sont ensuite écrites en ISO UTC ; le navigateur les affiche en heure de Paris.
func serviceBase(date string, loc *time.Location) int64 {
	d, _ := time.ParseInLocation("20060102", date, loc)
	return time.Date(d.Year(), d.Month(), d.Day(), 12, 0, 0, 0, loc).Unix() - 12*3600
}

// delayAt : retard (s) au passage à Gare de Lyon. ok=false : pas de temps réel ; skip : arrêt supprimé.
// byNumber : trajet retrouvé par son numéro (identifiants d'arrêts différents) → seul l'arrêt Gare de Lyon compte.
func delayAt(t *trip, tu *tripUpdate, base int64, kind string, byNumber bool) (delay int64, ok, skip bool) {
	gdlSeq, gdlStop := t.Stops[t.GDL].Seq, t.Stops[t.GDL].ID
	var best *stopUpdate
	bestSeq := 0
	for i := range tu.stops {
		st := &tu.stops[i]
		seq, has := st.seq, st.hasSeq
		if has && t.seqIdx != nil {
			seq, has = t.seqIdx[seq]
		}
		if byNumber {
			if !strings.HasSuffix(st.stop, sncfGDL) { // StopArea:OCE87686006 ou StopPoint:OCE…-87686006
				continue
			}
			seq, has = gdlSeq, true
		} else if !has && st.stop != "" {
			if strings.HasSuffix(st.stop, sncfGDL) {
				seq, has = gdlSeq, true
			} else {
				for _, s := range t.Stops {
					if s.ID == st.stop {
						seq, has = s.Seq, true
						break
					}
				}
			}
		}
		if !has || seq > gdlSeq {
			continue
		}
		if (seq == gdlSeq || st.stop == gdlStop) && st.skipped {
			return 0, false, true
		}
		if best == nil || seq > bestSeq {
			best, bestSeq = st, seq
		}
	}
	if best == nil {
		return 0, false, false
	}
	// À l'arrêt Gare de Lyon : l'événement voulu ; avant : le retard au départ de l'arrêt précédent se propage
	evs := []struct {
		e   event
		arr bool
	}{{best.dep, false}, {best.arr, true}}
	if bestSeq == gdlSeq && kind == "arrival" {
		evs[0], evs[1] = evs[1], evs[0]
	}
	for _, x := range evs {
		if x.e.hasDelay {
			return x.e.delay, true, false
		}
		if x.e.hasTime {
			for _, s := range t.Stops {
				if s.Seq == bestSeq {
					sched := s.Dep
					if x.arr {
						sched = s.Arr
					}
					return x.e.time - (base + int64(sched)), true, false
				}
			}
		}
	}
	return 0, false, false
}

type numberDate struct{ number, date string }

// indexByNumber : certains trajets temps réel ont un identifiant court "OCESN<numéro>F" (dont des RER D / ligne R) :
// on les retrouve par (numéro de train, date).
func indexByNumber(rt map[string]*tripUpdate) map[numberDate]*tripUpdate {
	out := map[numberDate]*tripUpdate{}
	for id, tu := range rt {
		if m := shortRTID.FindStringSubmatch(id); m != nil && tu.date != "" {
			k := numberDate{m[1], tu.date}
			if _, dup := out[k]; !dup {
				out[k] = tu
			}
		}
	}
	return out
}

func matchRT(rt map[string]*tripUpdate, byNum map[numberDate]*tripUpdate, t *trip, date string) (*tripUpdate, bool) {
	if tu := rt[t.ID]; tu != nil && (tu.date == "" || tu.date == date) {
		return tu, false
	}
	for _, n := range strings.Split(t.Number, "-") { // Transilien : "17769-17768" (numéro pair/impair)
		if tu := byNum[numberDate{n, date}]; tu != nil {
			return tu, true
		}
	}
	return nil, false
}

// ---------- Assemblage ----------

type item struct {
	ID        string  `json:"id"`
	Type      string  `json:"type"`
	Mode      string  `json:"mode"`
	Line      string  `json:"line"`
	Number    string  `json:"number"`
	Color     *string `json:"color"`
	Time      string  `json:"time"`
	BaseTime  string  `json:"baseTime"`
	DelayMin  int     `json:"delayMin"`
	Direction string  `json:"direction"`
	Realtime  bool    `json:"realtime"`
}

type payload struct {
	Day           string  `json:"day"`
	GeneratedAt   string  `json:"generatedAt"`
	RealtimeAt    *string `json:"realtimeAt"`
	RealtimeError *string `json:"realtimeError"`
	RealtimeCount int     `json:"realtimeCount"`
	Items         []item  `json:"items"`
}

func iso(ts int64) string { return time.Unix(ts, 0).UTC().Format("2006-01-02T15:04:05.000Z") }

func build(st *static, cacheDir string) (*payload, error) {
	now := time.Now().In(paris)
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, paris)
	days := []time.Time{time.Date(y, m, d-1, 0, 0, 0, 0, paris), today, time.Date(y, m, d+1, 0, 0, 0, 0, paris)}
	winStart, winEnd := today.Unix(), days[2].Unix()+3*3600

	trips, err := st.load(cacheDir, days)
	if err != nil {
		return nil, err
	}

	p := &payload{Day: today.Format("20060102"), Items: []item{}}
	rt := map[string]map[string]*tripUpdate{}
	var rtErrs []string
	for _, src := range rtURLs {
		rt[src.name] = map[string]*tripUpdate{}
		data, err := httpGet(src.url, time.Minute)
		var parsed map[string]*tripUpdate
		var stamp int64
		if err == nil {
			parsed, stamp, err = parseTripUpdates(data)
		}
		if err != nil {
			rtErrs = append(rtErrs, src.name+" : "+err.Error())
			logf("temps réel %s indisponible : %v", src.name, err)
			continue
		}
		rt[src.name] = parsed
		if src.name == "sncf" && stamp > 0 { // horodatage affiché : celui du flux principal
			s := iso(stamp)
			p.RealtimeAt = &s
		}
	}
	if len(rtErrs) > 0 {
		msg := strings.Join(rtErrs, " ; ")
		p.RealtimeError = &msg
	}
	byNum := map[string]map[numberDate]*tripUpdate{"sncf": indexByNumber(rt["sncf"])} // numéros courts : flux SNCF seulement

	for _, t := range trips {
		g := t.Stops[t.GDL]
		through := t.GDL > 0 && t.GDL < len(t.Stops)-1
		for _, date := range t.Dates {
			tu, byNumber := matchRT(rt[t.rt], byNum[t.rt], t, date)
			if tu != nil && tu.canceled {
				continue
			}
			base := serviceBase(date, t.loc)
			for _, kind := range t.Kinds {
				secs := g.Dep
				if kind == "arrival" {
					secs = g.Arr
				}
				planned := base + int64(secs)
				if planned < winStart-3600 || planned >= winEnd {
					continue
				}
				var delay int64
				realtime := false
				if tu != nil {
					dl, ok, skip := delayAt(t, tu, base, kind, byNumber)
					if skip {
						continue
					}
					delay, realtime = dl, ok
				}
				actual := planned + delay
				if actual < winStart || actual >= winEnd {
					continue
				}
				if realtime {
					p.RealtimeCount++
				}
				direction := "de " + t.Origin
				if kind == "departure" || through {
					direction = t.Terminus
				}
				p.Items = append(p.Items, item{
					ID: kind + "-" + t.Number + "-" + iso(planned), Type: kind,
					Mode: t.Mode, Line: t.Line, Number: t.Number, Color: t.Color,
					Time: iso(actual), BaseTime: iso(planned), DelayMin: int(math.Round(float64(delay) / 60)),
					Direction: direction, Realtime: realtime,
				})
			}
		}
	}
	sort.SliceStable(p.Items, func(i, j int) bool { return p.Items[i].Time < p.Items[j].Time })
	p.GeneratedAt = iso(time.Now().Unix())
	return p, nil
}

func write(path string, p *payload) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(p); err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

func main() {
	out := flag.String("out", "", "fichier JSON servi par nginx (ex. data/opendata.json)")
	cache := flag.String("cache", "", "dossier des GTFS téléchargés (défaut : <dossier de --out>/cache)")
	loop := flag.Int("loop", 0, "recalcule en continu toutes les N secondes (0 : une seule fois)")
	flag.Parse()
	if *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	if *cache == "" {
		abs, _ := filepath.Abs(*out)
		*cache = filepath.Join(filepath.Dir(abs), "cache")
	}
	var err error
	if paris, err = time.LoadLocation("Europe/Paris"); err != nil {
		logf("fuseau Europe/Paris introuvable : %v", err)
		os.Exit(1)
	}
	st := &static{feeds: map[string][]*trip{}}
	for {
		t0 := time.Now()
		p, err := build(st, *cache)
		if err == nil {
			err = write(*out, p)
		}
		if err != nil {
			logf("erreur : %v", err)
			if *loop <= 0 {
				os.Exit(1)
			}
		} else {
			logf("%d passages, %d en temps réel (%.1f s)", len(p.Items), p.RealtimeCount, time.Since(t0).Seconds())
		}
		if *loop <= 0 {
			return
		}
		p = nil
		debug.FreeOSMemory() // rendre au système la mémoire des GTFS / du temps réel entre deux calculs
		time.Sleep(time.Duration(*loop) * time.Second)
	}
}
