package server

import (
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Keep this in sync with the explicit eight-character hashes in vite.config.ts.
// HTML and installer scripts are deliberately never immutable.
var immutableStaticName = regexp.MustCompile(`^assets/(?:[^/]+/)*[^/]+-[A-Za-z0-9_-]{8}\.(?:js|mjs|css|woff2?|ttf|otf|svg|png|jpe?g|gif|webp|avif|ico|wasm)$`)

// serveStatic serves build-time .br/.gz siblings, never compressing on a request.
// Ranges address bytes of the selected representation, including encoded bytes.
func serveStatic(w http.ResponseWriter, r *http.Request, files fs.FS, name string) {
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Add("Vary", "Accept-Encoding")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		staticError(w, r, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !fs.ValidPath(name) || strings.HasSuffix(name, ".br") || strings.HasSuffix(name, ".gz") {
		staticError(w, r, "404 page not found", http.StatusNotFound)
		return
	}
	var original fs.File
	var originalInfo fs.FileInfo
	if file, err := files.Open(name); err == nil {
		if info, statErr := file.Stat(); statErr == nil && !info.IsDir() {
			original, originalInfo = file, info
		} else {
			file.Close()
		}
	}
	if original != nil {
		defer original.Close()
	}
	gzipPath := name + ".gz"
	if original == nil {
		// Packed builds ship only the gzip representation; a stray .br without
		// either is an orphan and stays a 404.
		file, err := files.Open(gzipPath)
		if err != nil {
			staticError(w, r, "404 page not found", http.StatusNotFound)
			return
		}
		info, statErr := file.Stat()
		file.Close()
		if statErr != nil || info.IsDir() {
			staticError(w, r, "404 page not found", http.StatusNotFound)
			return
		}
	}

	qualities := staticEncodingQualities(r.Header.Values("Accept-Encoding"))
	var selected fs.File
	var info fs.FileInfo
	encoding := ""
	best := qualities[2]
	// An implicit identity is a fallback, not a preference over requested coding.
	if best < 0 {
		best = 0
	}
	candidates := [2]struct {
		coding, suffix string
		quality        float64
	}{{"br", ".br", qualities[0]}, {"gzip", ".gz", qualities[1]}}
	if candidates[1].quality > candidates[0].quality {
		candidates[0], candidates[1] = candidates[1], candidates[0]
	}
	for _, candidate := range candidates {
		if candidate.quality <= 0 || candidate.quality < best {
			continue
		}
		file, err := files.Open(name + candidate.suffix)
		if err != nil {
			continue
		}
		stat, err := file.Stat()
		if err != nil || stat.IsDir() {
			file.Close()
			continue
		}
		selected, info, encoding = file, stat, candidate.coding
		break
	}
	if selected == nil && original != nil {
		selected, info = original, originalInfo
	}
	if selected != nil {
		defer selected.Close()
	}

	contentType := mime.TypeByExtension(path.Ext(name))
	var decompressed []byte
	if contentType == "" {
		if original != nil {
			var prefix [512]byte
			n, _ := original.Read(prefix[:])
			contentType = http.DetectContentType(prefix[:n])
			if seeker, ok := original.(io.Seeker); ok {
				if _, err := seeker.Seek(0, io.SeekStart); err != nil {
					staticError(w, r, "cannot seek static file", http.StatusInternalServerError)
					return
				}
			}
		} else {
			body, err := decompressStaticFile(files, gzipPath)
			if err != nil {
				staticError(w, r, "static file is corrupt", http.StatusInternalServerError)
				return
			}
			decompressed = body
			contentType = http.DetectContentType(body)
		}
	}

	if selected == nil {
		// Identity request against a packed tree: rebuild the body from gzip.
		if decompressed == nil {
			body, err := decompressStaticFile(files, gzipPath)
			if err != nil {
				staticError(w, r, "static file is corrupt", http.StatusInternalServerError)
				return
			}
			decompressed = body
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Length", strconv.Itoa(len(decompressed)))
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(decompressed))
		return
	}
	if encoding == "" && qualities[2] == 0 {
		staticError(w, r, "no acceptable content encoding", http.StatusNotAcceptable)
		return
	}
	content, ok := selected.(io.ReadSeeker)
	if !ok {
		staticError(w, r, "static file is not seekable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	if encoding != "" {
		w.Header().Set("Content-Encoding", encoding)
	}
	if immutableStaticName.MatchString(name) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	http.ServeContent(w, r, name, info.ModTime(), content)
}

// decompressStaticFile expands an embedded gzip representation; packed static
// trees keep only the compressed form, so identity requests rebuild the body.
func decompressStaticFile(files fs.FS, name string) ([]byte, error) {
	file, err := files.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	var body bytes.Buffer
	if _, err := io.Copy(&body, reader); err != nil {
		return nil, err
	}
	return body.Bytes(), nil
}

// A negative identity quality means it was not explicitly ranked. RFC 9110
// permits identity unless identity;q=0 or an otherwise unmatched *;q=0 forbids it.
func staticEncodingQualities(headers []string) [3]float64 {
	// br, gzip, identity, wildcard; -1 marks an absent declaration.
	qualities := [4]float64{-1, -1, -1, -1}
	for _, header := range headers {
		for header != "" {
			var item string
			item, header, _ = strings.Cut(header, ",")
			coding, parameters, _ := strings.Cut(item, ";")
			coding = strings.TrimSpace(coding)
			index := -1
			switch {
			case strings.EqualFold(coding, "br"):
				index = 0
			case strings.EqualFold(coding, "gzip"):
				index = 1
			case strings.EqualFold(coding, "identity"):
				index = 2
			case coding == "*":
				index = 3
			}
			if index < 0 {
				continue
			}
			quality := 1.0
			for parameters != "" {
				var parameter string
				parameter, parameters, _ = strings.Cut(parameters, ";")
				key, value, ok := strings.Cut(strings.TrimSpace(parameter), "=")
				if !strings.EqualFold(strings.TrimSpace(key), "q") {
					continue
				}
				q, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
				if !ok || err != nil || !(q >= 0 && q <= 1) {
					quality = 0
				} else if q < quality {
					quality = q
				}
			}
			// A duplicate must not resurrect an explicitly refused coding.
			if qualities[index] < 0 || quality < qualities[index] {
				qualities[index] = quality
			}
		}
	}
	for index := range 2 {
		if qualities[index] < 0 {
			qualities[index] = qualities[3]
		}
	}
	if qualities[2] < 0 && qualities[3] == 0 {
		qualities[2] = 0
	}
	return [3]float64{qualities[0], qualities[1], qualities[2]}
}

func staticError(w http.ResponseWriter, r *http.Request, message string, status int) {
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(status)
		return
	}
	http.Error(w, message, status)
}
