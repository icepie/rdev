package server

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

const testStaticAsset = "assets/app-aB_12-xy.js"

func staticTestFiles(t *testing.T) (fstest.MapFS, map[string][]byte) {
	t.Helper()
	plain := []byte(strings.Repeat("console.log(\"static asset\");\n", 20))
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	// Brotli encoding of plain, generated with node:zlib.brotliCompressSync.
	brotli := []byte{
		27, 67, 2, 248, 157, 7, 182, 205, 184, 188, 59, 118, 177, 96, 60, 98,
		173, 144, 178, 19, 166, 69, 80, 202, 230, 20, 219, 91, 186, 156, 209,
		101, 33, 180, 240, 150, 44, 240, 194, 142, 46, 76, 46, 60, 252, 111, 12,
	}
	encoded := map[string][]byte{"": plain, "gzip": compressed.Bytes(), "br": brotli}
	files := fstest.MapFS{
		testStaticAsset:                {Data: plain},
		testStaticAsset + ".gz":        {Data: encoded["gzip"]},
		testStaticAsset + ".br":        {Data: brotli},
		"app.html":                     {Data: []byte("<!doctype html><title>fresh</title>")},
		"assets/plain.js":              {Data: plain},
		"assets/orphan-12345678.js.br": {Data: brotli},
	}
	return files, encoded
}

func TestStaticEncodingNegotiation(t *testing.T) {
	files, encoded := staticTestFiles(t)
	for _, tc := range []struct {
		name, accept, encoding string
		status                 int
	}{
		{"absent", "", "", 200},
		{"gzip", "gzip", "gzip", 200},
		{"brotli preferred on tie", "gzip, br", "br", 200},
		{"weighted gzip", "br;q=0.2, gzip;q=0.8", "gzip", 200},
		{"implicit identity fallback", "gzip;q=0.1", "gzip", 200},
		{"explicit identity preference", "identity;q=1, gzip;q=0.8", "", 200},
		{"brotli refused", "br;q=0, gzip", "gzip", 200},
		{"all codings refused", "br;q=0, gzip;q=0", "", 200},
		{"wildcard", "*", "br", 200},
		{"explicit wildcard override", "br;q=0, *;q=0.5", "gzip", 200},
		{"only gzip permitted", "*;q=0, gzip;q=1", "gzip", 200},
		{"none acceptable", "*;q=0", "", 406},
		{"identity refused", "identity;q=0", "", 406},
		{"identity restores wildcard", "*;q=0, identity;q=0.2", "", 200},
		{"case and spaces", "BR; Q = 1, gzip;q=0", "br", 200},
		{"unknown coding", "deflate", "", 200},
		{"invalid quality", "br;q=NaN, gzip;q=invalid", "", 200},
		{"out of range quality", "br;q=2, gzip;q=-1", "", 200},
		{"duplicate refusal", "gzip;q=0, gzip", "", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/"+testStaticAsset, nil)
			request.Header.Set("Accept-Encoding", tc.accept)
			response := httptest.NewRecorder()
			serveStatic(response, request, files, testStaticAsset)
			if response.Code != tc.status || response.Header().Get("Content-Encoding") != tc.encoding {
				t.Fatalf("status=%d encoding=%q, want %d %q", response.Code, response.Header().Get("Content-Encoding"), tc.status, tc.encoding)
			}
			if response.Header().Get("Vary") != "Accept-Encoding" {
				t.Fatalf("missing negotiation Vary: %v", response.Header())
			}
			if tc.status != http.StatusOK {
				if strings.Contains(response.Header().Get("Cache-Control"), "immutable") {
					t.Fatal("negotiation error must not be immutable")
				}
				return
			}
			if !bytes.Equal(response.Body.Bytes(), encoded[tc.encoding]) {
				t.Fatal("response differs from selected precompressed representation")
			}
			if response.Header().Get("Content-Length") != strconv.Itoa(len(encoded[tc.encoding])) {
				t.Fatal("incorrect encoded content length")
			}
			if !strings.Contains(response.Header().Get("Content-Type"), "javascript") {
				t.Fatalf("wrong original content type: %q", response.Header().Get("Content-Type"))
			}
			if response.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
				t.Fatal("hashed asset is not immutable")
			}
			if tc.encoding == "gzip" {
				reader, err := gzip.NewReader(bytes.NewReader(response.Body.Bytes()))
				if err != nil {
					t.Fatal(err)
				}
				defer reader.Close()
				decoded, err := io.ReadAll(reader)
				if err != nil || !bytes.Equal(decoded, encoded[""]) {
					t.Fatalf("gzip decoding mismatch: %v", err)
				}
			}
		})
	}
}

func TestStaticHeadRangesAndErrors(t *testing.T) {
	files, encoded := staticTestFiles(t)
	for _, coding := range []string{"", "gzip", "br"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			for _, rangeHeader := range []string{"", "bytes=2-6"} {
				t.Run(coding+method+rangeHeader, func(t *testing.T) {
					request := httptest.NewRequest(method, "/"+testStaticAsset, nil)
					request.Header.Set("Accept-Encoding", coding)
					request.Header.Set("Range", rangeHeader)
					response := httptest.NewRecorder()
					serveStatic(response, request, files, testStaticAsset)
					want := encoded[coding]
					status := http.StatusOK
					if rangeHeader != "" {
						want = want[2:7]
						status = http.StatusPartialContent
						if response.Header().Get("Content-Range") != "bytes 2-6/"+strconv.Itoa(len(encoded[coding])) {
							t.Fatalf("incorrect encoded range: %v", response.Header())
						}
					}
					if response.Code != status || response.Header().Get("Content-Length") != strconv.Itoa(len(want)) || response.Header().Get("Content-Encoding") != coding {
						t.Fatalf("status=%d headers=%v", response.Code, response.Header())
					}
					if method == http.MethodHead {
						want = nil
					}
					if !bytes.Equal(response.Body.Bytes(), want) {
						t.Fatal("wrong response body")
					}
				})
			}
		}
	}
	for _, tc := range []struct {
		name, method, rangeHeader string
		status                    int
	}{
		{"missing.js", "GET", "", 404},
		{"missing.js", "HEAD", "", 404},
		{"assets/orphan-12345678.js", "GET", "", 404},
		{testStaticAsset + ".br", "GET", "", 404},
		{testStaticAsset + ".gz", "GET", "", 404},
		{"assets", "GET", "", 404},
		{"../app.html", "GET", "", 404},
		{testStaticAsset, "POST", "", 405},
		{testStaticAsset, "GET", "bytes=99999-", 416},
	} {
		t.Run(tc.name+tc.method+tc.rangeHeader, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, "/asset", nil)
			request.Header.Set("Accept-Encoding", "br, gzip")
			request.Header.Set("Range", tc.rangeHeader)
			response := httptest.NewRecorder()
			serveStatic(response, request, files, tc.name)
			if response.Code != tc.status {
				t.Fatalf("status=%d, want %d", response.Code, tc.status)
			}
			if tc.method == http.MethodHead && response.Body.Len() != 0 {
				t.Fatal("HEAD error has a body")
			}
			if tc.status == 405 && response.Header().Get("Allow") != "GET, HEAD" {
				t.Fatal("missing allowed methods")
			}
		})
	}
}

func TestStaticFreshnessAndMissingSidecars(t *testing.T) {
	files, _ := staticTestFiles(t)
	for _, name := range []string{"app.html", "assets/plain.js", "assets/page-12345678.html", "assets/run-12345678.sh", "run.ps1"} {
		files[name] = &fstest.MapFile{Data: []byte("<!doctype html><title>fresh</title>")}
		request := httptest.NewRequest(http.MethodGet, "/page", nil)
		request.Header.Set("Accept-Encoding", "gzip, br")
		response := httptest.NewRecorder()
		serveStatic(response, request, files, name)
		if response.Code != 200 || response.Header().Get("Cache-Control") != "no-cache" || response.Header().Get("Content-Encoding") != "" {
			t.Fatalf("%s: status=%d headers=%v", name, response.Code, response.Header())
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/plain", nil)
	request.Header.Set("Accept-Encoding", "br, identity;q=0")
	response := httptest.NewRecorder()
	serveStatic(response, request, files, "assets/plain.js")
	if response.Code != http.StatusNotAcceptable {
		t.Fatal("missing sidecar must not silently ignore identity;q=0")
	}
	delete(files, testStaticAsset+".br")
	request.Header["Accept-Encoding"] = []string{"br", "gzip"}
	response = httptest.NewRecorder()
	serveStatic(response, request, files, testStaticAsset)
	if response.Code != 200 || response.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("missing Brotli must fall back to acceptable gzip across header lines")
	}
}

func TestStaticPageHandlerFreshHTML(t *testing.T) {
	server := &Server{}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		response := httptest.NewRecorder()
		server.StaticPageHandler("app.html")(response, httptest.NewRequest(method, "/terminal", nil))
		if response.Code != 200 || response.Header().Get("Cache-Control") != "no-cache" || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("status=%d headers=%v", response.Code, response.Header())
		}
		if method == http.MethodHead && response.Body.Len() != 0 {
			t.Fatal("HEAD page has a body")
		}
	}
}

func TestStaticCompressedHTMLAndSniffedTypes(t *testing.T) {
	plain := []byte("<!doctype html><html><title>Fresh page</title></html>")
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"app.html", "page-without-extension"} {
		files := fstest.MapFS{
			name:         {Data: plain},
			name + ".gz": {Data: compressed.Bytes()},
		}
		for _, coding := range []string{"", "gzip"} {
			request := httptest.NewRequest(http.MethodGet, "/terminal", nil)
			request.Header.Set("Accept-Encoding", coding)
			response := httptest.NewRecorder()
			serveStatic(response, request, files, name)
			if response.Code != 200 || response.Header().Get("Cache-Control") != "no-cache" || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/html") || response.Header().Get("Content-Encoding") != coding {
				t.Fatalf("%s/%s: status=%d headers=%v", name, coding, response.Code, response.Header())
			}
			want := plain
			if coding == "gzip" {
				want = compressed.Bytes()
			}
			if !bytes.Equal(response.Body.Bytes(), want) {
				t.Fatalf("%s/%s: MIME sniffing corrupted the representation", name, coding)
			}
		}
	}
}

func TestStaticPackedTreeWithoutOriginals(t *testing.T) {
	plain := []byte("<!doctype html><title>packed</title>" + strings.Repeat(" body", 40))
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{
		"packed.html.gz": {Data: compressed.Bytes()},
	}
	for _, accept := range []string{"gzip", "identity", ""} {
		t.Run("accept="+accept, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/packed.html", nil)
			if accept != "" {
				request.Header.Set("Accept-Encoding", accept)
			}
			response := httptest.NewRecorder()
			serveStatic(response, request, files, "packed.html")
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d", response.Code)
			}
			if response.Header().Get("Content-Type") != "text/html; charset=utf-8" {
				t.Fatalf("content type=%q", response.Header().Get("Content-Type"))
			}
			if accept == "gzip" {
				if response.Header().Get("Content-Encoding") != "gzip" || !bytes.Equal(response.Body.Bytes(), compressed.Bytes()) {
					t.Fatal("gzip passthrough failed")
				}
				return
			}
			if response.Header().Get("Content-Encoding") != "" {
				t.Fatal("identity response was encoded")
			}
			if !bytes.Equal(response.Body.Bytes(), plain) {
				t.Fatal("identity body mismatch")
			}
			if response.Header().Get("Content-Length") != strconv.Itoa(len(plain)) {
				t.Fatalf("length=%q", response.Header().Get("Content-Length"))
			}
		})
	}

	rangeRequest := httptest.NewRequest(http.MethodGet, "/packed.html", nil)
	rangeRequest.Header.Set("Range", "bytes=10-19")
	rangeResponse := httptest.NewRecorder()
	serveStatic(rangeResponse, rangeRequest, files, "packed.html")
	if rangeResponse.Code != http.StatusPartialContent || !bytes.Equal(rangeResponse.Body.Bytes(), plain[10:20]) {
		t.Fatalf("range status=%d body=%q", rangeResponse.Code, rangeResponse.Body.String())
	}
}
