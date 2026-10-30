// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"errors"
	"fmt"
	"math"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/geotag"
	"github.com/alonsovidales/otc/i18n"
	imagesquality "github.com/alonsovidales/otc/images_quality"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// On this day (Memories): the photos taken around today in earlier years,
// the best of each year. Choosing them is a query and arithmetic - the
// photos of those days with their measures (quality.go), tags, people,
// collections and posts - plus a few small thumbnails read to tell shots of
// a burst apart. The answer for a date, period and time zone is kept until
// the day changes or something it depends on does (memoriesState.changed).

// ErrMemoriesOff is GetMemories while the user has On this day off
// (settings.memories_enabled): the apps hide it.
var ErrMemoriesOff = errors.New("on this day is off") // i18n-ignore: the reply's code and text are the websocket layer's

// memoriesState is On this day's switch, read once from the database, its
// background measuring, and the answers kept for the day.
type memoriesState struct {
	mu   sync.Mutex
	read bool // on was read from the database
	on   bool

	backfillMu sync.Mutex  // one BackfillQuality at a time
	measuring  atomic.Bool // BackfillQuality is running

	gen     atomic.Uint64 // bumped by anything that changes an answer
	cacheMu sync.Mutex
	cache   map[string]memoriesKept
}

type memoriesKept struct {
	gen    uint64
	day    string // the device's UTC date it was made on
	answer *pb.Memories
}

// changed drops every answer kept: a photo deleted, measured, hidden, a
// person fixed, the switch turned.
func (s *memoriesState) changed() { s.gen.Add(1) }

// MemoriesEnabled is whether On this day is on for this library. Read once
// from the database; a database before the column reads as off.
func (mg *Manager) MemoriesEnabled() bool {
	if mg == nil || mg.dao == nil {
		return false
	}
	s := &mg.memories
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.read {
		on, err := mg.dao.MemoriesEnabled()
		switch {
		case errors.Is(err, dao.ErrSchemaBehind):
			s.read, s.on = true, false
		case err != nil:
			log.Error("on this day: could not read whether it is on, taking it as off for now:", err)
			return false
		default:
			s.read, s.on = true, on
		}
	}
	return s.on
}

// SetMemoriesEnabled turns On this day on or off for this library. Nothing
// is deleted either way; turning it on measures what isn't yet, in the
// background, with this session's key.
func (mg *Manager) SetMemoriesEnabled(ses *session.Session, on bool) error {
	if err := mg.dao.SetMemoriesEnabled(on); err != nil {
		return err
	}
	s := &mg.memories
	s.mu.Lock()
	s.read, s.on = true, on
	s.mu.Unlock()
	s.changed()
	if on && ses != nil {
		go mg.BackfillQuality(ses)
	}
	return nil
}

// HideMemory hides h from the memories, or shows it again (show).
func (mg *Manager) HideMemory(h dao.HiddenMemory, show bool) error {
	if !validHidden(h) {
		return devtext.New(i18n.DevFilesMemoriesBadHide())
	}
	var err error
	if show {
		err = mg.dao.ShowMemory(h)
	} else {
		err = mg.dao.HideMemory(h)
	}
	if err == nil {
		mg.memories.changed()
	}
	return err
}

// ShowAllMemories shows everything hidden again.
func (mg *Manager) ShowAllMemories() error {
	err := mg.dao.ShowAllMemories()
	if err == nil {
		mg.memories.changed()
	}
	return err
}

// validHidden is whether h names something that can be hidden: a content
// hash, a day, a memory's key or a person's id, each as the device writes
// them.
func validHidden(h dao.HiddenMemory) bool {
	v := h.Value
	switch h.Kind {
	case dao.HideMemoryPhoto:
		return dao.IsContentHash(v)
	case dao.HideMemoryDay:
		_, err := time.Parse(time.DateOnly, v)
		return err == nil
	case dao.HideMemoryMemory:
		_, _, ok := parseMemoryKey(v)
		return ok
	case dao.HideMemoryPerson:
		return v != "" && len(v) <= 64 && !strings.ContainsAny(v, " \t\n")
	}
	return false
}

// The periods' names in a memory's key.
var periodNames = map[pb.MemoryPeriod]string{pb.MemoryPeriod_MemoryDay: "day", pb.MemoryPeriod_MemoryWeek: "week", pb.MemoryPeriod_MemoryMonth: "month"}

// memoryKey is Memory.key: the period and the memory's first day ("month"
// keeps only the month).
func memoryKey(period pb.MemoryPeriod, start time.Time) string {
	if period == pb.MemoryPeriod_MemoryMonth {
		return "month:" + start.Format("2006-01")
	}
	return periodNames[period] + ":" + start.Format(time.DateOnly)
}

// parseMemoryKey reads a Memory.key back.
func parseMemoryKey(key string) (period pb.MemoryPeriod, start time.Time, ok bool) {
	name, date, found := strings.Cut(key, ":")
	if !found {
		return 0, time.Time{}, false
	}
	for p, n := range periodNames {
		if n != name {
			continue
		}
		layout := time.DateOnly
		if p == pb.MemoryPeriod_MemoryMonth {
			layout = "2006-01"
		}
		t, err := time.Parse(layout, date)
		return p, t, err == nil
	}
	return 0, time.Time{}, false
}

// memoryLocation is the client's time zone: its IANA name when the device
// knows it, else its offset from UTC.
func memoryLocation(name string, offsetMinutes int32) *time.Location {
	if name != "" {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	if offsetMinutes < -16*60 || offsetMinutes > 16*60 {
		offsetMinutes = 0
	}
	return time.FixedZone("", int(offsetMinutes)*60)
}

// Memories answers GetMemories.
func (mg *Manager) Memories(ses *session.Session, req *pb.GetMemories) (*pb.Memories, error) {
	if !mg.MemoriesEnabled() {
		return nil, ErrMemoriesOff
	}
	today, err := time.Parse(time.DateOnly, req.Date)
	if err != nil || today.Year() < 1971 {
		return nil, devtext.New(i18n.DevFilesMemoriesBadDate())
	}
	if _, ok := periodNames[req.Period]; !ok {
		return nil, devtext.New(i18n.DevFilesMemoriesBadDate())
	}
	loc := memoryLocation(req.TimeZone, req.UtcOffsetMinutes)
	faces := mg.faceRecognitionOn()
	// Read before the answers' generation: reading them the first time
	// counts as a change of them (reloadOutOfImagesLocked).
	excluded, err := mg.OutOfImagesFolders()
	if err != nil {
		return nil, err
	}
	s := &mg.memories
	key := fmt.Sprintf("%s|%d|%s|%d|%t", req.Date, req.Period, req.TimeZone, req.UtcOffsetMinutes, faces)
	gen := s.gen.Load()
	day := time.Now().UTC().Format(time.DateOnly)
	s.cacheMu.Lock()
	kept, ok := s.cache[key]
	s.cacheMu.Unlock()
	if ok && kept.gen == gen && kept.day == day {
		answer := kept.answer
		// Whether the device is still measuring changes on its own.
		return &pb.Memories{Memories: answer.Memories, HiddenCount: answer.HiddenCount, Measuring: s.measuring.Load()}, nil
	}
	answer, err := mg.chooseMemories(ses, today, req.Period, loc, faces, excluded)
	if err != nil {
		return nil, err
	}
	s.cacheMu.Lock()
	if s.cache == nil || len(s.cache) > 64 {
		s.cache = map[string]memoriesKept{}
	}
	s.cache[key] = memoriesKept{gen: gen, day: day, answer: answer}
	s.cacheMu.Unlock()
	answer.Measuring = s.measuring.Load()
	return answer, nil
}

// faceRecognitionOn is whether face recognition is on (people count in
// the choice, and are named): off when it can't be read.
func (mg *Manager) faceRecognitionOn() bool {
	on, err := mg.dao.GetFaceRecognitionEnabled()
	return err == nil && on
}

// chooseMemories reads what the choice needs and makes it.
func (mg *Manager) chooseMemories(ses *session.Session, today time.Time, period pb.MemoryPeriod, loc *time.Location, faces bool, excluded []string) (*pb.Memories, error) {
	rows, err := mg.dao.MemoryCandidates(memoryMonthDays(today, period), excluded)
	if err != nil {
		return nil, err
	}
	hidden, err := mg.dao.HiddenMemories()
	if err != nil {
		return nil, err
	}
	photos := make([]memPhoto, 0, len(rows))
	hashes := make([]string, 0, len(rows))
	for _, r := range rows {
		local := r.Created.In(loc)
		year, ok := memoryYear(today, period, local)
		if !ok {
			continue
		}
		photos = append(photos, memPhoto{hash: r.Hash, path: r.Path, mime: r.Mime, created: r.Created, local: local, year: year, quality: r.Quality})
		hashes = append(hashes, r.Hash)
	}
	answer := &pb.Memories{HiddenCount: int32(len(hidden))}
	if len(photos) == 0 {
		return answer, nil
	}
	tags, err := mg.dao.TagsOfHashes(hashes)
	if err != nil {
		return nil, err
	}
	var people map[string][]dao.MemoryFace
	if faces {
		if people, err = mg.dao.FacesOfHashes(hashes); err != nil {
			return nil, err
		}
	}
	collected, err := mg.dao.CollectedHashes(hashes)
	if err != nil {
		return nil, err
	}
	posted, err := mg.dao.PostedHashes(hashes)
	if err != nil {
		// A database without the social tables' newer columns still has
		// memories, without that signal.
		log.Debug("on this day: could not read the posts of the photos:", err)
		posted = nil
	}
	for i := range photos {
		p := &photos[i]
		p.tags = tags[p.hash]
		p.people = people[p.hash]
		p.collected = collected[p.hash]
		p.posts = posted[p.hash]
	}
	home := mg.homePlaces()
	opts := memoryOptions{period: period, today: today, faces: faces, home: home, hidden: hiddenSets(hidden), similar: mg.smartSimilarity(ses)}
	thumbs := map[string]uint64{}
	dhash := func(hash string) (uint64, bool) {
		if h, ok := thumbs[hash]; ok {
			return h, true
		}
		h, ok := mg.thumbnailDHash(ses, hash)
		if ok {
			thumbs[hash] = h
		}
		return h, ok
	}
	for _, c := range chooseFromPhotos(photos, opts, dhash) {
		m := &pb.Memory{
			Key:        c.key,
			YearsAgo:   int32(today.Year() - c.year),
			StartDate:  c.start.Format(time.DateOnly),
			EndDate:    c.end.Format(time.DateOnly),
			Place:      c.place,
			People:     c.people,
			PhotoCount: int32(c.photoCount),
		}
		for _, i := range c.picks {
			m.Files = append(m.Files, memoryFile(photos[i]))
		}
		m.Cover = memoryFile(photos[c.cover])
		answer.Memories = append(answer.Memories, m)
	}
	return answer, nil
}

// memoryFile is a chosen photo as an answer carries it: no content.
func memoryFile(p memPhoto) *pb.File {
	f := &pb.File{Hash: p.hash, Path: p.path, Mime: p.mime, Created: timestamppb.New(p.created), Modified: timestamppb.New(p.created)}
	return f
}

// thumbnailDHash is a photo's difference hash, from its small thumbnail
// (its big one when it has none): two shots of one burst are a few bits
// apart.
func (mg *Manager) thumbnailDHash(ses *session.Session, hash string) (uint64, bool) {
	if ses == nil {
		return 0, false
	}
	data, err := readThumbnailBytes(ses, hash)
	if err != nil {
		return 0, false
	}
	img, err := decodeThumbnail(data)
	if err != nil {
		return 0, false
	}
	return imagesquality.DHash(img), true
}

// homePlaces is where most of the library's photos were taken - the
// commonest city and country among the photos' own place tags - kept for
// an hour: a photo elsewhere is a trip.
func (mg *Manager) homePlaces() homePlaces {
	homeMu.Lock()
	defer homeMu.Unlock()
	if time.Since(homeAt) < time.Hour && homeFor == mg {
		return homeKept
	}
	counts, err := mg.dao.PlaceTagCounts(400)
	if err != nil {
		log.Debug("on this day: could not read the commonest places:", err)
		return homePlaces{}
	}
	var h homePlaces
	for _, c := range counts {
		if h.city == "" && geotag.IsCity(c.Tag) {
			h.city = c.Tag
		}
		if h.country == "" && geotag.IsCountry(c.Tag) {
			h.country = c.Tag
		}
	}
	homeKept, homeAt, homeFor = h, time.Now(), mg
	return h
}

var (
	homeMu   sync.Mutex
	homeKept homePlaces
	homeAt   time.Time
	homeFor  *Manager
)

// ---------- the choice ----------

// memPhoto is one photo taken on the days asked for, with everything the
// choice weighs.
type memPhoto struct {
	hash, path, mime string
	created          time.Time // UTC, as stored
	local            time.Time // in the client's time zone
	year             int       // the earlier year whose days it is in
	quality          *dao.PhotoQuality
	tags             []dao.MemoryTag
	people           []dao.MemoryFace
	collected        bool
	posts            dao.PostStats
}

type homePlaces struct{ city, country string }

type memoryOptions struct {
	period pb.MemoryPeriod
	today  time.Time
	faces  bool
	home   homePlaces
	hidden hidden
	// similar is how alike two photos are by Smart search's vectors
	// (smart_search.go), false when either has none; nil while Smart search
	// is off: bursts are told apart by difference hashes then.
	similar func(a, b string) (float32, bool)
}

type hidden struct {
	photos, days, memories, people map[string]bool
}

func hiddenSets(list []dao.HiddenMemory) hidden {
	h := hidden{photos: map[string]bool{}, days: map[string]bool{}, memories: map[string]bool{}, people: map[string]bool{}}
	for _, x := range list {
		switch x.Kind {
		case dao.HideMemoryPhoto:
			h.photos[x.Value] = true
		case dao.HideMemoryDay:
			h.days[x.Value] = true
		case dao.HideMemoryMemory:
			h.memories[x.Value] = true
		case dao.HideMemoryPerson:
			h.people[x.Value] = true
		}
	}
	return h
}

// memoryChoice is one earlier year's memory: indexes into the photos.
type memoryChoice struct {
	key        string
	year       int
	start, end time.Time
	picks      []int // in the order they were taken
	cover      int
	place      string
	people     []string
	photoCount int
	coverScore float64
}

// The weights of the choice, all in one place. A photo's score is its
// relevance plus its quality; the filters before it take out what is never
// a memory.
var memoryWeights = struct {
	// People (only while face recognition is on): each named person in the
	// photo, at most namedMax of them; anyone at all; three people or more.
	named     float64
	namedMax  int
	anyone    float64
	group     float64
	groupFrom int
	// What the user did with the photo: put it in a collection, posted it,
	// and each like and comment its posts got (at most reactionsMax in all).
	collected    float64
	posted       float64
	reaction     float64
	reactionsMax float64
	// A trip: a city, or a country, other than the library's usual one.
	tripCity    float64
	tripCountry float64
	// What it shows (memoryTagWeights), summed and kept within tagsMin to
	// tagsMax.
	tagsMin, tagsMax float64
	// An event: per doubling of the photos taken in its moment.
	momentDoubling float64
	// Quality: per point of NIMA's score above aestheticMid; how sharp
	// (0 to 1 between sharpLow and sharpHigh, centred); clipped pixels past
	// clippedFree.
	aesthetic    float64
	aestheticMid float64
	sharp        float64
	sharpLow     float64
	sharpHigh    float64
	clipped      float64
	clippedFree  float64
}{
	named: 1.5, namedMax: 3, anyone: 0.5, group: 1, groupFrom: 3,
	collected: 2.5, posted: 3, reaction: 0.3, reactionsMax: 2,
	tripCity: 1.5, tripCountry: 1,
	tagsMin: -2, tagsMax: 3,
	momentDoubling: 0.5,
	aesthetic:      1.5, aestheticMid: 5, sharp: 1, sharpLow: 15, sharpHigh: 400, clipped: 4, clippedFree: 0.2,
}

// memoryTagWeights is what the tagging model's words are worth (RAM++'s
// vocabulary: tag_list_4585.txt). Words not here count nothing.
var memoryTagWeights = map[string]float64{
	"birthday": 1.5, "birthday cake": 1.5, "cake": 0.7, "wedding": 1.5, "bride": 1.2, "groom": 1.2,
	"celebration": 1, "party": 1, "graduation": 1.5, "christmas": 1.2, "christmas tree": 1.2,
	"halloween": 1, "easter": 1, "thanksgiving": 1, "carnival": 1, "firework": 1.2, "costume": 0.6, "gift": 0.5, "balloon": 0.5,
	"beach": 1, "sea": 0.8, "mountain": 1, "snow": 0.8, "sunset": 1, "sunrise": 1, "lake": 0.8, "waterfall": 1,
	"forest": 0.5, "hike": 0.8, "ski": 1, "pool": 0.5, "picnic": 0.8, "camping": 1, "vacation": 1, "travel": 0.8,
	"landmark": 0.8, "castle": 0.8, "eiffel tower": 1, "cathedral": 0.6, "amusement park": 1, "zoo": 1, "playground": 0.6,
	"concert": 1, "museum": 0.4, "garden": 0.3, "flower": 0.3,
	"dog": 0.8, "cat": 0.8, "baby": 1.2, "newborn": 1.5, "toddler": 1, "child": 0.6, "family": 1, "hug": 0.8, "kiss": 0.6,
	"smile": 0.4, "couple": 0.5, "grandparent": 0.8, "selfie": 0.2, "portrait": 0.3, "group": 0.4,
	"car interior": -1, "parking lot": -1, "room": -0.3, "floor": -0.5, "ceiling": -0.8, "wall": -0.3, "office": -0.6,
	"shelf": -0.5, "kitchen": -0.2, "laptop": -0.8, "computer": -0.8, "keyboard": -0.8, "monitor": -0.8,
	"plate": -0.2, "food": -0.2, "meal": -0.1, "drink": -0.2, "coffee": -0.2, "car": -0.3,
}

// Tags that make a photo a picture of a screen or a document: never a
// memory. documentTagsWeak only when nothing else says it is a photo of a
// moment - a camera named in its metadata, or people in it.
var (
	documentTags     = map[string]bool{"screenshot": true, "document": true, "receipt": true, "whiteboard": true, "handwriting": true, "business card": true, "bill": true, "credit card": true, "passport": true, "computer screen": true, "website": true}
	documentTagsWeak = map[string]bool{"text": true, "paper": true, "menu": true, "screen": true, "note": true, "label": true, "logo": true, "ticket": true, "map": true}
)

// screenshotNames are how phones and computers name screenshots, lower
// case, in the languages the apps speak and a few more.
var screenshotNames = []string{"screenshot", "screen shot", "screen_shot", "captura de pantalla", "captura de ecrã", "captura de tela", "bildschirmfoto", "capture d", "schermata", "schermafbeelding", "skärmavbild", "zrzut ekranu"} // i18n-ignore: file names matched, never shown

// The filters' limits: sharpness (the thumbnail's Laplacian variance - a
// sharp photo measured 43 at the least, a blurred one 13 at most), exposure
// and size.
const (
	cMemoryMinSharpness  = 15.0
	cMemoryMaxClipped    = 0.6
	cMemoryMinBrightness = 20.0
	cMemoryMaxBrightness = 240.0
	cMemoryMinSide       = 400
	cMemoryTallAspect    = 2.0
)

// How a year's photos make a memory.
const (
	// cMomentGap splits a year's photos into moments.
	cMomentGap = 2 * time.Hour
	// A burst: shots this close in time whose difference hashes are within
	// cBurstBits, or this close whatever their hashes.
	cBurstWindow = 2 * time.Minute
	cBurstBits   = 10
	cBurstSame   = 3 * time.Second
	// cNotable is a score that makes a single photo a day's memory on its
	// own (a named person, a collection, a post).
	cNotable = 2.0
)

// memoryLimits is how many photos a memory shows at most, and how many good
// ones a year needs to have one.
func memoryLimits(period pb.MemoryPeriod) (most, least int) {
	switch period {
	case pb.MemoryPeriod_MemoryWeek:
		return 10, 3
	case pb.MemoryPeriod_MemoryMonth:
		return 12, 3
	}
	return 8, 2
}

// isLeap is whether year has a 29 February.
func isLeap(year int) bool {
	return year%4 == 0 && (year%100 != 0 || year%400 == 0)
}

// inYear is the date of today's month and day in year: a 29 February is
// the 28th in a year without one.
func inYear(today time.Time, year int) time.Time {
	m, d := today.Month(), today.Day()
	if m == time.February && d == 29 && !isLeap(year) {
		d = 28
	}
	return time.Date(year, m, d, 0, 0, 0, 0, time.UTC)
}

// memoryWindow is the days of year a memory covers.
func memoryWindow(today time.Time, period pb.MemoryPeriod, year int) (start, end time.Time) {
	c := inYear(today, year)
	switch period {
	case pb.MemoryPeriod_MemoryWeek:
		return c.AddDate(0, 0, -3), c.AddDate(0, 0, 3)
	case pb.MemoryPeriod_MemoryMonth:
		first := time.Date(year, today.Month(), 1, 0, 0, 0, 0, time.UTC)
		return first, first.AddDate(0, 1, -1)
	}
	if c.Month() == time.February && c.Day() == 28 && !isLeap(today.Year()) && isLeap(year) {
		// Today is 28 February in a year without a 29th: that year's 29th
		// shows today too.
		return c, c.AddDate(0, 0, 1)
	}
	return c, c
}

// dayOf is t's calendar day, as a UTC midnight.
func dayOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// memoryYear is the earlier year whose memory a photo taken at local
// belongs to, if any: a week around New Year spans two calendar years, and
// belongs to the year of its middle day.
func memoryYear(today time.Time, period pb.MemoryPeriod, local time.Time) (int, bool) {
	d := dayOf(local)
	for _, y := range []int{d.Year(), d.Year() + 1, d.Year() - 1} {
		if y >= today.Year() || y < 1970 {
			continue
		}
		start, end := memoryWindow(today, period, y)
		if !d.Before(start) && !d.After(end) {
			return y, true
		}
	}
	return 0, false
}

// memoryMonthDays is every "%m-%d" a photo in one of the windows can carry
// in UTC: the windows' days and one either side, for the time zones.
func memoryMonthDays(today time.Time, period pb.MemoryPeriod) []string {
	set := map[string]bool{}
	// A leap year and the one before it cover every month and day a
	// window can take, 29 February included.
	for _, y := range []int{2020, 2019} {
		start, end := memoryWindow(today, period, y)
		for d := start.AddDate(0, 0, -1); !d.After(end.AddDate(0, 0, 1)); d = d.AddDate(0, 0, 1) {
			set[d.Format("01-02")] = true
		}
	}
	if today.Month() == time.February && today.Day() >= 28 {
		set["02-29"] = true
	}
	out := make([]string, 0, len(set))
	for md := range set {
		out = append(out, md)
	}
	sort.Strings(out)
	return out
}

// hasTag is whether p has one of set.
func hasTag(p memPhoto, set map[string]bool) bool {
	for _, t := range p.tags {
		if set[t.Name] {
			return true
		}
	}
	return false
}

// camera is whether p's metadata names a camera; unknown counts as no.
func (p memPhoto) camera() bool {
	return p.quality != nil && p.quality.Camera != nil && *p.quality.Camera
}

// leftOut is why p is never a memory ("" when it may be one): what the
// user hid, screenshots and documents, and pictures too blurred, badly
// exposed or small to show.
func leftOut(p memPhoto, o memoryOptions) string {
	if o.hidden.photos[p.hash] || o.hidden.days[p.local.Format(time.DateOnly)] {
		return "hidden"
	}
	for _, f := range p.people {
		if o.hidden.people[f.PersonID] {
			return "hidden"
		}
	}
	name := strings.ToLower(path.Base(p.path))
	for _, s := range screenshotNames {
		if strings.Contains(name, s) {
			return "screenshot"
		}
	}
	if hasTag(p, documentTags) {
		return "document"
	}
	moment := p.camera() || (o.faces && len(p.people) > 0)
	if !moment && hasTag(p, documentTagsWeak) {
		return "document"
	}
	if !p.camera() && (p.mime == "image/png" || p.mime == "image/gif" || p.mime == "image/webp") {
		return "screenshot"
	}
	q := p.quality
	if q == nil {
		return ""
	}
	if !p.camera() && q.Width > 0 && q.Height > 0 && float64(max(q.Width, q.Height))/float64(min(q.Width, q.Height)) >= cMemoryTallAspect {
		return "screenshot"
	}
	if q.Sharpness < cMemoryMinSharpness {
		return "blurred"
	}
	if q.Dark > cMemoryMaxClipped || q.Bright > cMemoryMaxClipped || q.Brightness < cMemoryMinBrightness || q.Brightness > cMemoryMaxBrightness {
		return "exposure"
	}
	if q.Width > 0 && max(q.Width, q.Height) < cMemoryMinSide {
		return "small"
	}
	return ""
}

// placeOf is p's city and country, from its own location data's tags.
func placeOf(p memPhoto) (city, country string) {
	for _, t := range p.tags {
		if t.Score < 1 {
			continue
		}
		if city == "" && geotag.IsCity(t.Name) {
			city = t.Name
		} else if country == "" && geotag.IsCountry(t.Name) {
			country = t.Name
		}
	}
	return city, country
}

// relevance is how much p is worth showing again, before how it looks.
func relevance(p memPhoto, o memoryOptions, momentSize int) float64 {
	w := memoryWeights
	r := 0.0
	if o.faces && len(p.people) > 0 {
		named := 0
		for _, f := range p.people {
			if f.Name != "" {
				named++
			}
		}
		r += w.named*float64(min(named, w.namedMax)) + w.anyone
		if len(p.people) >= w.groupFrom {
			r += w.group
		}
	}
	if p.collected {
		r += w.collected
	}
	if p.posts.Posts > 0 {
		r += w.posted + math.Min(w.reactionsMax, w.reaction*float64(p.posts.Likes+p.posts.Comments))
	}
	city, country := placeOf(p)
	if city != "" && o.home.city != "" && city != o.home.city {
		r += w.tripCity
	}
	if country != "" && o.home.country != "" && country != o.home.country {
		r += w.tripCountry
	}
	tags := 0.0
	for _, t := range p.tags {
		tags += memoryTagWeights[t.Name]
	}
	r += math.Max(w.tagsMin, math.Min(w.tagsMax, tags))
	if momentSize > 1 {
		r += w.momentDoubling * math.Log2(float64(momentSize))
	}
	return r
}

// quality is how well p looks: NIMA's score, sharpness and clipping. A
// photo not measured yet counts as average.
func quality(p memPhoto) float64 {
	w := memoryWeights
	q := p.quality
	if q == nil {
		return 0
	}
	s := 0.0
	if q.Aesthetic != nil {
		s += w.aesthetic * (float64(*q.Aesthetic) - w.aestheticMid)
	}
	if q.Sharpness > 0 {
		f := (math.Log(q.Sharpness) - math.Log(w.sharpLow)) / (math.Log(w.sharpHigh) - math.Log(w.sharpLow))
		s += w.sharp * (2*math.Max(0, math.Min(1, f)) - 1)
	}
	s -= w.clipped * (math.Max(0, q.Dark-w.clippedFree) + math.Max(0, q.Bright-w.clippedFree))
	return s
}

// chooseFromPhotos makes the memories of photos, the most recent year
// first. dhash gives a photo's difference hash (false: unknown), asked only
// for shots close in time.
func chooseFromPhotos(photos []memPhoto, o memoryOptions, dhash func(hash string) (uint64, bool)) []memoryChoice {
	byYear := map[int][]int{}
	counts := map[int]int{}
	for i, p := range photos {
		if o.hidden.photos[p.hash] || o.hidden.days[p.local.Format(time.DateOnly)] {
			continue
		}
		counts[p.year]++
		if leftOut(p, o) != "" {
			continue
		}
		byYear[p.year] = append(byYear[p.year], i)
	}
	years := make([]int, 0, len(byYear))
	for y := range byYear {
		years = append(years, y)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(years)))
	most, least := memoryLimits(o.period)
	var out []memoryChoice
	for _, y := range years {
		start, end := memoryWindow(o.today, o.period, y)
		key := memoryKey(o.period, start)
		if o.hidden.memories[key] {
			continue
		}
		c, ok := chooseYear(photos, byYear[y], o, most, least, dhash)
		if !ok {
			continue
		}
		c.key, c.year, c.start, c.end, c.photoCount = key, y, start, end, counts[y]
		if o.period == pb.MemoryPeriod_MemoryDay {
			// A day's memory is the day its photos were taken (28 February
			// can show a 29th).
			c.start, c.end = dayOf(photos[c.picks[0]].local), dayOf(photos[c.picks[len(c.picks)-1]].local)
		}
		out = append(out, c)
	}
	return out
}

// chooseYear picks a year's photos: scored, bursts counted once, spread
// over its moments, then in the order they were taken.
func chooseYear(photos []memPhoto, idx []int, o memoryOptions, most, least int, dhash func(string) (uint64, bool)) (memoryChoice, bool) {
	sort.Slice(idx, func(a, b int) bool { return photos[idx[a]].local.Before(photos[idx[b]].local) })
	// Moments: a new one after cMomentGap, or where the city changes.
	moment := make(map[int]int, len(idx))
	sizes := []int{}
	prevCity := ""
	for n, i := range idx {
		city, _ := placeOf(photos[i])
		if n == 0 || photos[i].local.Sub(photos[idx[n-1]].local) > cMomentGap || (city != "" && prevCity != "" && city != prevCity) {
			sizes = append(sizes, 0)
		}
		if city != "" {
			prevCity = city
		}
		moment[i] = len(sizes) - 1
		sizes[len(sizes)-1]++
	}
	score := make(map[int]float64, len(idx))
	ranked := append([]int(nil), idx...)
	for _, i := range idx {
		score[i] = relevance(photos[i], o, sizes[moment[i]]) + quality(photos[i])
	}
	sort.SliceStable(ranked, func(a, b int) bool { return score[ranked[a]] > score[ranked[b]] })

	perMoment := max(2, (most+len(sizes)-1)/len(sizes)+1)
	taken := map[int]int{}
	var picks []int
	for _, i := range ranked {
		if len(picks) == most {
			break
		}
		if taken[moment[i]] >= perMoment || burstOf(photos, picks, i, dhash, o.similar) {
			continue
		}
		picks = append(picks, i)
		taken[moment[i]]++
	}
	if len(picks) == 0 {
		return memoryChoice{}, false
	}
	if len(picks) < least && !(o.period == pb.MemoryPeriod_MemoryDay && score[picks[0]] >= cNotable) {
		return memoryChoice{}, false
	}
	cover := picks[0]
	sort.Slice(picks, func(a, b int) bool { return photos[picks[a]].local.Before(photos[picks[b]].local) })
	c := memoryChoice{picks: picks, cover: cover, coverScore: score[cover]}
	c.place, c.people = memoryPlaceAndPeople(photos, picks, o)
	return c, true
}

// burstOf is whether photo i is another shot of a burst one of picks
// already holds: taken within cBurstSame of it; or, by Smart search's
// vectors when both have one (similar), the same photo (cSameCos) whenever
// taken or as alike as a burst's shots (cBurstCos) within
// cEmbedBurstWindow; else within cBurstWindow and a few bits of its
// difference hash.
func burstOf(photos []memPhoto, picks []int, i int, dhash func(string) (uint64, bool), similar func(a, b string) (float32, bool)) bool {
	for _, j := range picks {
		d := photos[i].local.Sub(photos[j].local)
		if d < 0 {
			d = -d
		}
		if d <= cBurstSame {
			return true
		}
		if similar != nil {
			if c, ok := similar(photos[i].hash, photos[j].hash); ok {
				if c >= cSameCos || (c >= cBurstCos && d <= cEmbedBurstWindow) {
					return true
				}
				continue
			}
		}
		if d > cBurstWindow {
			continue
		}
		a, okA := dhash(photos[i].hash)
		b, okB := dhash(photos[j].hash)
		if okA && okB && imagesquality.Distance(a, b) <= cBurstBits {
			return true
		}
	}
	return false
}

// memoryPlaceAndPeople is where most of picks were taken (a city, else a
// country) and the named people in them, the one in most first.
func memoryPlaceAndPeople(photos []memPhoto, picks []int, o memoryOptions) (string, []string) {
	cities, countries, named := map[string]int{}, map[string]int{}, map[string]int{}
	for _, i := range picks {
		city, country := placeOf(photos[i])
		if city != "" {
			cities[city]++
		}
		if country != "" {
			countries[country]++
		}
		if o.faces {
			for _, f := range photos[i].people {
				if f.Name != "" && !o.hidden.people[f.PersonID] {
					named[f.Name]++
				}
			}
		}
	}
	place := commonest(cities)
	if place == "" {
		place = commonest(countries)
	}
	people := make([]string, 0, len(named))
	for n := range named {
		people = append(people, n)
	}
	sort.Slice(people, func(a, b int) bool {
		if named[people[a]] != named[people[b]] {
			return named[people[a]] > named[people[b]]
		}
		return people[a] < people[b]
	})
	return place, people
}

// commonest is counts' most frequent key, the first by name on a tie; ""
// when empty.
func commonest(counts map[string]int) string {
	best, n := "", 0
	for k, c := range counts {
		if c > n || (c == n && k < best) {
			best, n = k, c
		}
	}
	return best
}
