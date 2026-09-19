package modproxy

import (
	"regexp"
	"strings"
)

// kind is the sort of GOPROXY resource a request addresses.
type kind int

const (
	kindUnknown kind = iota
	kindInfo
	kindMod
	kindZip
)

func (k kind) contentType() string {
	if k == kindZip {
		return "application/zip"
	}

	return "text/plain; charset=UTF-8"
}

// request is a classified, cacheable GOPROXY request.
type request struct {
	kind kind
	// path is the request path without its leading slash. It is used verbatim as
	// the cache index key and as the upstream path, because the go command
	// already escapes module paths (module.EscapePath) before asking.
	path       string
	modulePath string
	version    string
}

// canonicalVersion matches semver as the go command canonicalises it, including
// pseudo-versions (the timestamp-and-hash form is an ordinary prerelease) and
// "+incompatible" build metadata. Leading zeroes are rejected, which is what
// keeps "v1.02.3" out.
var canonicalVersion = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

// uncachedPrefixes are module paths gocica deliberately refuses to serve.
// golang.org/toolchain distributes the Go toolchain itself: the archives are
// ~70 MB, platform specific, and the go command insists on checksum database
// verification for them. Caching them buys little and costs a lot.
var uncachedPrefixes = []string{"golang.org/toolchain/"}

// classify decides whether a request path is something gocica may cache.
//
// Everything it does not recognise gets a 404, which the go command treats as
// fs.ErrNotExist and walks past to the next GOPROXY entry. That is deliberate for
// the mutable endpoints too:
//
//   - "@v/list" and "@latest" are version queries whose answers change,
//   - "/sumdb/<name>/supported" must 404, because answering 200 binds the go
//     command to us as its checksum database proxy for the rest of the process
//     with no fallback path.
func classify(urlPath string) (request, bool) {
	p := strings.TrimPrefix(urlPath, "/")

	modulePath, rest, ok := strings.Cut(p, "/@v/")
	if !ok {
		return request{}, false
	}

	// Cut at the last dot: versions are full of dots ("v1.2.3.zip").
	before, after, ok := strings.CutLast(rest, ".")
	if !ok {
		return request{}, false
	}
	base, ext := before, after

	var k kind
	switch ext {
	case "info":
		k = kindInfo
	case "mod":
		k = kindMod
	case "zip":
		k = kindZip
	default:
		return request{}, false
	}

	if !validEscapedModulePath(modulePath) {
		return request{}, false
	}

	for _, prefix := range uncachedPrefixes {
		if strings.HasPrefix(modulePath+"/", prefix) {
			return request{}, false
		}
	}

	// Only canonical versions are immutable. "@v/master.info", "@v/v1.info" and
	// "@v/<short-sha>.info" are resolution queries; caching them would pin a
	// moving branch to whatever it pointed at the first time.
	version := unescapeUpper(base)
	if !canonicalVersion.MatchString(version) {
		return request{}, false
	}

	return request{kind: k, path: p, modulePath: modulePath, version: version}, true
}

// validEscapedModulePath checks the escaped form the go command sends: lowercase
// only, with uppercase letters encoded as "!" + the lowercase letter.
func validEscapedModulePath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") || strings.Contains(p, "//") {
		return false
	}

	for elem := range strings.SplitSeq(p, "/") {
		if elem == "." || elem == ".." {
			return false
		}
	}

	for i := range len(p) {
		c := p[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '!' || c == '-' || c == '.' || c == '_' || c == '~' || c == '/' || c == '+':
		default:
			return false
		}
	}

	// A trailing "!" has nothing to escape.
	return !strings.HasSuffix(p, "!")
}

// unescapeUpper reverses the "!" + lowercase escaping used for uppercase letters.
func unescapeUpper(s string) string {
	if !strings.Contains(s, "!") {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '!' && i+1 < len(s) {
			i++
			c := s[i]
			if c >= 'a' && c <= 'z' {
				c -= 'a' - 'A'
			}
			b.WriteByte(c)

			continue
		}
		b.WriteByte(s[i])
	}

	return b.String()
}
