package slim

import (
	"bytes"
	"context"
	"debug/macho"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"howett.net/plist"
)

//go:embed appmap.json
var appMapJSON []byte

// AppInfo is what ScanApp found in a built .app bundle.
type AppInfo struct {
	Path              string
	BundleID          string
	Name              string
	BackgroundModes   []string
	UsageDescriptions []string       // NS*UsageDescription keys present, sorted
	Entitlements      map[string]any // merged from the main binary and appex binaries
	Frameworks        []string       // linked framework names, sorted, unique
	Extensions        []string       // appex bundle ids and NSExtensionPointIdentifier values
	// Sources maps a signal ("framework HealthKit", "entitlement aps-environment",
	// "background-mode location", "usage NSHealthShareUsageDescription",
	// "extension-point com.apple.widgetkit-extension") to where it was seen
	// ("app", "PlugIns/Foo.appex", "Frameworks/Bar.framework").
	Sources map[string][]string
}

var usageKeyRe = regexp.MustCompile(`^NS.*UsageDescription$`)

type scanner struct {
	info    AppInfo
	fws     map[string]bool
	exts    map[string]bool
	usage   map[string]bool
	bgModes map[string]bool
}

func (s *scanner) source(signal, where string) {
	for _, w := range s.info.Sources[signal] {
		if w == where {
			return
		}
	}
	s.info.Sources[signal] = append(s.info.Sources[signal], where)
}

// ScanApp inspects a built iOS .app bundle (Info.plist, linked frameworks,
// embedded entitlements, app extensions, embedded frameworks).
func ScanApp(appPath string) (AppInfo, error) {
	st, err := os.Stat(appPath)
	if err != nil {
		return AppInfo{}, fmt.Errorf("slim: app path: %w", err)
	}
	if !st.IsDir() || !strings.HasSuffix(strings.TrimRight(appPath, "/\\"), ".app") {
		return AppInfo{}, fmt.Errorf("slim: %q is not an .app bundle directory", appPath)
	}
	appPath = filepath.Clean(appPath)
	s := &scanner{
		info:    AppInfo{Path: appPath, Entitlements: map[string]any{}, Sources: map[string][]string{}},
		fws:     map[string]bool{},
		exts:    map[string]bool{},
		usage:   map[string]bool{},
		bgModes: map[string]bool{},
	}
	main, err := readInfoPlist(appPath)
	if err != nil {
		return AppInfo{}, err
	}
	s.info.BundleID, _ = main["CFBundleIdentifier"].(string)
	if n, _ := main["CFBundleDisplayName"].(string); n != "" {
		s.info.Name = n
	} else {
		s.info.Name, _ = main["CFBundleName"].(string)
	}
	s.plistSignals(main, "app")
	s.binary(appPath, main, "app", true)
	s.entitlementsFile(filepath.Join(appPath, "archived-expanded-entitlements.xcent"), "app")

	appexes, _ := filepath.Glob(filepath.Join(appPath, "PlugIns", "*.appex"))
	sort.Strings(appexes)
	for _, ap := range appexes {
		where := "PlugIns/" + filepath.Base(ap)
		ip, err := readInfoPlist(ap)
		if err != nil {
			continue
		}
		if id, _ := ip["CFBundleIdentifier"].(string); id != "" {
			s.exts[id] = true
		}
		if ext, ok := ip["NSExtension"].(map[string]any); ok {
			if pt, _ := ext["NSExtensionPointIdentifier"].(string); pt != "" {
				s.exts[pt] = true
				s.source("extension-point "+pt, where)
			}
		}
		s.plistSignals(ip, where)
		s.binary(ap, ip, where, true)
		s.entitlementsFile(filepath.Join(ap, "archived-expanded-entitlements.xcent"), where)
	}

	fwDirs, _ := filepath.Glob(filepath.Join(appPath, "Frameworks", "*.framework"))
	sort.Strings(fwDirs)
	for _, fd := range fwDirs {
		ip, _ := readInfoPlist(fd)
		if ip == nil {
			ip = map[string]any{}
		}
		s.binary(fd, ip, "Frameworks/"+filepath.Base(fd), false)
	}

	s.info.Frameworks = sortedKeys(s.fws)
	s.info.Extensions = sortedKeys(s.exts)
	s.info.UsageDescriptions = sortedKeys(s.usage)
	s.info.BackgroundModes = sortedKeys(s.bgModes)
	return s.info, nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func readInfoPlist(dir string) (map[string]any, error) {
	b, err := os.ReadFile(filepath.Join(dir, "Info.plist"))
	if err != nil {
		return nil, fmt.Errorf("slim: %s: %w", filepath.Base(dir), err)
	}
	var m map[string]any
	if _, err := plist.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("slim: %s/Info.plist: %w", filepath.Base(dir), err)
	}
	return m, nil
}

func (s *scanner) plistSignals(p map[string]any, where string) {
	if modes, ok := p["UIBackgroundModes"].([]any); ok {
		for _, m := range modes {
			if str, ok := m.(string); ok && str != "" {
				s.bgModes[str] = true
				s.source("background-mode "+str, where)
			}
		}
	}
	for k := range p {
		if usageKeyRe.MatchString(k) {
			s.usage[k] = true
			s.source("usage "+k, where)
		}
	}
}

// binary scans the executable of the bundle at dir: linked frameworks and (when
// wantEnts) embedded entitlements.
func (s *scanner) binary(dir string, info map[string]any, where string, wantEnts bool) {
	exe, _ := info["CFBundleExecutable"].(string)
	if exe == "" && strings.HasSuffix(dir, ".framework") {
		exe = strings.TrimSuffix(filepath.Base(dir), ".framework")
	}
	if exe == "" {
		return
	}
	path := filepath.Join(dir, exe)
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return
	}
	mi, err := openMachO(path)
	if err != nil {
		return // not a Mach-O (or unreadable): tolerated
	}
	defer mi.close()
	for _, fw := range mi.frameworks() {
		s.fws[fw] = true
		s.source("framework "+fw, where)
	}
	if !wantEnts {
		return
	}
	var ents map[string]any
	if sec := mi.file.Section("__entitlements"); sec != nil {
		if data, err := sec.Data(); err == nil {
			ents = mergeEnts(ents, parseXMLPlist(data))
		}
	}
	ents = mergeEnts(ents, mi.signatureEntitlements())
	if len(ents) == 0 {
		ents = codesignEntitlements(path)
	}
	s.mergeEntitlements(ents, where)
}

func (s *scanner) entitlementsFile(path, where string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	s.mergeEntitlements(parseXMLPlist(b), where)
}

func (s *scanner) mergeEntitlements(ents map[string]any, where string) {
	for k, v := range ents {
		s.source("entitlement "+k, where)
		s.info.Entitlements = mergeEnts(s.info.Entitlements, map[string]any{k: v})
	}
}

// mergeEnts merges src into dst (allocating dst): existing scalar values win,
// arrays are unioned.
func mergeEnts(dst, src map[string]any) map[string]any {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = map[string]any{}
	}
	for k, v := range src {
		old, ok := dst[k]
		if !ok {
			dst[k] = v
			continue
		}
		oa, ok1 := old.([]any)
		na, ok2 := v.([]any)
		if ok1 && ok2 {
			for _, x := range na {
				dup := false
				for _, y := range oa {
					if fmt.Sprint(x) == fmt.Sprint(y) {
						dup = true
						break
					}
				}
				if !dup {
					oa = append(oa, x)
				}
			}
			dst[k] = oa
		}
	}
	return dst
}

func parseXMLPlist(b []byte) map[string]any {
	if i := bytes.Index(b, []byte("<?xml")); i > 0 {
		b = b[i:]
	}
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return nil
	}
	var m map[string]any
	if _, err := plist.Unmarshal(b, &m); err != nil {
		return nil
	}
	return m
}

// codesignEntitlements falls back to the codesign tool; every failure (tool
// missing, unsigned binary, timeout) yields nil.
func codesignEntitlements(path string) map[string]any {
	bin, err := exec.LookPath("codesign")
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "-d", "--entitlements", ":-", path).Output()
	if err != nil {
		return nil
	}
	return parseXMLPlist(out)
}

// --- Mach-O ---------------------------------------------------------------

type machoInfo struct {
	file   *macho.File
	slice  io.ReaderAt // reader positioned at the start of the chosen arch slice
	closer io.Closer
}

func (m *machoInfo) close() {
	if m.file != nil {
		m.file.Close()
	}
	if m.closer != nil {
		m.closer.Close()
	}
}

// openMachO opens a thin or universal binary (first arch of a fat file).
func openMachO(path string) (*machoInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if ff, err := macho.NewFatFile(f); err == nil {
		if len(ff.Arches) == 0 {
			f.Close()
			return nil, errors.New("empty fat binary")
		}
		a := ff.Arches[0]
		sr := io.NewSectionReader(f, int64(a.Offset), int64(a.Size))
		return &machoInfo{file: a.File, slice: sr, closer: f}, nil
	}
	mf, err := macho.NewFile(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return &machoInfo{file: mf, slice: f, closer: f}, nil
}

const (
	lcLoadDylib      = 0x0c
	lcLoadWeakDylib  = 0x80000018
	lcReexportDylib  = 0x8000001f
	lcLoadUpDylib    = 0x80000023
	lcLazyLoadDylib  = 0x20
	lcCodeSignature  = 0x1d
	maxSignatureSize = 32 << 20
)

var frameworkPrefixes = []string{
	"/System/Library/Frameworks/", "/System/Library/PrivateFrameworks/",
	"@rpath/", "@executable_path/Frameworks/", "@loader_path/Frameworks/",
}

var frameworkNameRe = regexp.MustCompile(`^([^/]+)\.framework/`)

func frameworkFromPath(p string) string {
	for _, pre := range frameworkPrefixes {
		if rest, ok := strings.CutPrefix(p, pre); ok {
			if m := frameworkNameRe.FindStringSubmatch(rest); m != nil {
				return m[1]
			}
		}
	}
	return ""
}

// frameworks returns the linked framework names (LC_LOAD_DYLIB plus the weak,
// re-export, upward and lazy variants the macho package leaves as LoadBytes).
func (m *machoInfo) frameworks() []string {
	set := map[string]bool{}
	for _, l := range m.file.Loads {
		var name string
		switch v := l.(type) {
		case *macho.Dylib:
			name = v.Name
		case macho.LoadBytes:
			b := []byte(v)
			if len(b) < 24 {
				continue
			}
			bo := m.file.ByteOrder
			switch bo.Uint32(b[0:4]) {
			case lcLoadDylib, lcLoadWeakDylib, lcReexportDylib, lcLoadUpDylib, lcLazyLoadDylib:
			default:
				continue
			}
			off := bo.Uint32(b[8:12])
			if int(off) >= len(b) {
				continue
			}
			rest := b[off:]
			if i := bytes.IndexByte(rest, 0); i >= 0 {
				rest = rest[:i]
			}
			name = string(rest)
		default:
			continue
		}
		if fw := frameworkFromPath(name); fw != "" {
			set[fw] = true
		}
	}
	return sortedKeys(set)
}

// signatureEntitlements reads the entitlements from LC_CODE_SIGNATURE.
func (m *machoInfo) signatureEntitlements() map[string]any {
	for _, l := range m.file.Loads {
		b, ok := l.(macho.LoadBytes)
		if !ok || len(b) < 16 {
			continue
		}
		bo := m.file.ByteOrder
		if bo.Uint32(b[0:4]) != lcCodeSignature {
			continue
		}
		off, size := int64(bo.Uint32(b[8:12])), int64(bo.Uint32(b[12:16]))
		if size <= 0 || size > maxSignatureSize {
			return nil
		}
		buf := make([]byte, size)
		if n, err := m.slice.ReadAt(buf, off); err != nil && n != len(buf) {
			return nil
		}
		return parseEntitlementsBlob(buf)
	}
	return nil
}

const (
	magicEmbeddedSignature = 0xfade0cc0
	magicEntitlements      = 0xfade7171
	slotEntitlements       = 5
)

// parseEntitlementsBlob parses a code-signature SuperBlob (big-endian) and
// returns the XML entitlements (slot 5, blob magic 0xfade7171). DER
// entitlements (0xfade7172) are ignored. Returns nil when nothing is found.
func parseEntitlementsBlob(data []byte) map[string]any {
	be := binary.BigEndian
	if len(data) < 12 || be.Uint32(data[0:4]) != magicEmbeddedSignature {
		return nil
	}
	count := be.Uint32(data[8:12])
	if uint64(count)*8+12 > uint64(len(data)) {
		return nil
	}
	for i := uint32(0); i < count; i++ {
		idx := 12 + i*8
		typ, off := be.Uint32(data[idx:idx+4]), uint64(be.Uint32(data[idx+4:idx+8]))
		if typ != slotEntitlements || off+8 > uint64(len(data)) {
			continue
		}
		blob := data[off:]
		if be.Uint32(blob[0:4]) != magicEntitlements {
			continue
		}
		length := uint64(be.Uint32(blob[4:8]))
		if length < 8 || length > uint64(len(blob)) {
			continue
		}
		if m := parseXMLPlist(blob[8:length]); m != nil {
			return m
		}
	}
	return nil
}

// --- app map --------------------------------------------------------------

// Signal kinds in appmap.json.
const (
	KindFramework      = "framework"
	KindEntitlement    = "entitlement"
	KindBackgroundMode = "background-mode"
	KindUsage          = "usage"
	KindExtensionPoint = "extension-point"
)

// MapEntry maps one signal to the groups the app needs. Match is exact, or
// "prefix:<p>". An entry with no groups is informational only.
type MapEntry struct {
	Kind   string   `json:"kind"`
	Match  string   `json:"match"`
	Groups []string `json:"groups"`
	Reason string   `json:"reason"`
}

// AppMap is the embedded, versioned signal-to-group mapping.
type AppMap struct {
	Version     int        `json:"version"`
	Description string     `json:"description,omitempty"`
	Entries     []MapEntry `json:"entries"`
}

// LoadAppMap parses the embedded appmap.json.
func LoadAppMap() (*AppMap, error) {
	var m AppMap
	if err := json.Unmarshal(appMapJSON, &m); err != nil {
		return nil, fmt.Errorf("slim appmap: %w", err)
	}
	return &m, nil
}

func (e MapEntry) matches(kind, value string) bool {
	if e.Kind != kind {
		return false
	}
	if p, ok := strings.CutPrefix(e.Match, "prefix:"); ok {
		return strings.HasPrefix(value, p)
	}
	return e.Match == value
}

// KeepReason says why a group must stay enabled ("kept because"). Group is
// empty for informational reasons.
type KeepReason struct {
	Group  string
	Signal string // e.g. "framework HealthKit"
	Reason string
	Where  []string
}

// String renders "healthkit: framework HealthKit (HealthKit needs healthd) [app]".
func (r KeepReason) String() string {
	var sb strings.Builder
	if r.Group != "" {
		sb.WriteString(r.Group + ": ")
	}
	sb.WriteString(r.Signal)
	if r.Reason != "" {
		sb.WriteString(" (" + r.Reason + ")")
	}
	if len(r.Where) > 0 {
		sb.WriteString(" [" + strings.Join(r.Where, ", ") + "]")
	}
	return sb.String()
}

type signal struct{ kind, value string }

func (info AppInfo) signals() []signal {
	var out []signal
	for _, f := range info.Frameworks {
		out = append(out, signal{KindFramework, f})
	}
	ents := make([]string, 0, len(info.Entitlements))
	for k := range info.Entitlements {
		ents = append(ents, k)
	}
	sort.Strings(ents)
	for _, k := range ents {
		out = append(out, signal{KindEntitlement, k})
	}
	for _, b := range info.BackgroundModes {
		out = append(out, signal{KindBackgroundMode, b})
	}
	for _, u := range info.UsageDescriptions {
		out = append(out, signal{KindUsage, u})
	}
	for _, e := range info.Extensions {
		out = append(out, signal{KindExtensionPoint, e})
	}
	return out
}

// Needed returns the reasons each group must be kept (sorted by group, then
// signal; one reason per group+signal, reasons of several entries merged) and
// the informational signals that matched entries without groups (push etc.).
// Groups absent from knownGroups are ignored when knownGroups is non-nil.
func (m *AppMap) Needed(info AppInfo, knownGroups map[string]string) (reasons []KeepReason, informational []KeepReason) {
	type key struct{ group, signal string }
	merged := map[key]*KeepReason{}
	infoSeen := map[string]*KeepReason{}
	addReason := func(into *KeepReason, reason string) {
		if reason == "" || strings.Contains(into.Reason, reason) {
			return
		}
		if into.Reason != "" {
			into.Reason += "; "
		}
		into.Reason += reason
	}
	for _, sg := range info.signals() {
		sigName := sg.kind + " " + sg.value
		where := append([]string(nil), info.Sources[sigName]...)
		sort.Strings(where)
		for _, e := range m.Entries {
			if !e.matches(sg.kind, sg.value) {
				continue
			}
			if len(e.Groups) == 0 {
				r := infoSeen[sigName]
				if r == nil {
					r = &KeepReason{Signal: sigName, Where: where}
					infoSeen[sigName] = r
				}
				addReason(r, e.Reason)
				continue
			}
			for _, g := range e.Groups {
				if knownGroups != nil {
					if _, ok := knownGroups[g]; !ok {
						continue
					}
				}
				k := key{g, sigName}
				r := merged[k]
				if r == nil {
					r = &KeepReason{Group: g, Signal: sigName, Where: where}
					merged[k] = r
				}
				addReason(r, e.Reason)
			}
		}
	}
	for _, r := range merged {
		reasons = append(reasons, *r)
	}
	sort.Slice(reasons, func(i, j int) bool {
		if reasons[i].Group != reasons[j].Group {
			return reasons[i].Group < reasons[j].Group
		}
		return reasons[i].Signal < reasons[j].Signal
	})
	for _, r := range infoSeen {
		informational = append(informational, *r)
	}
	sort.Slice(informational, func(i, j int) bool { return informational[i].Signal < informational[j].Signal })
	return reasons, informational
}

// AppGroups returns the aggressive groups minus those with a keep reason,
// preserving the order of aggressive.
func AppGroups(aggressive []string, reasons []KeepReason) []string {
	keep := map[string]bool{}
	for _, r := range reasons {
		keep[r.Group] = true
	}
	out := make([]string, 0, len(aggressive))
	for _, g := range aggressive {
		if !keep[g] {
			out = append(out, g)
		}
	}
	return out
}
