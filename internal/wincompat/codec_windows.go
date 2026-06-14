package wincompat

import (
	"bytes"
	"io"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// DecodeOutput converts Windows console output to UTF-8.
// WinPTY may already emit UTF-8, while pipe fallback and legacy console tools
// commonly emit CP936/GBK/GB18030. Prefer UTF-8 when the byte stream is valid;
// otherwise decode it as GB18030.
func DecodeOutput(r io.Reader) io.Reader {
	return &autoDecodeReader{r: r}
}

type autoDecodeReader struct {
	r   io.Reader
	buf bytes.Buffer
}

func (r *autoDecodeReader) Read(p []byte) (int, error) {
	if r.buf.Len() > 0 {
		return r.buf.Read(p)
	}
	raw := make([]byte, max(len(p), 4096))
	n, err := r.r.Read(raw)
	if n > 0 {
		data := raw[:n]
		if utf8.Valid(data) || endsWithPartialUTF8(data) {
			r.buf.Write(data)
		} else if decoded, _, decErr := transform.Bytes(simplifiedchinese.GB18030.NewDecoder(), data); decErr == nil {
			r.buf.Write(decoded)
		} else {
			r.buf.Write(data)
		}
		return r.buf.Read(p)
	}
	return 0, err
}

func endsWithPartialUTF8(data []byte) bool {
	for i := len(data) - 1; i >= 0 && i >= len(data)-4; i-- {
		if data[i] < utf8.RuneSelf {
			return false
		}
		if data[i]&0xC0 == 0xC0 {
			return !utf8.Valid(data[i:])
		}
	}
	return false
}

// EncodeInput converts UTF-8 terminal input into the legacy console code page.
func EncodeInput(w io.Writer) io.WriteCloser {
	return &crlfWriter{w: transform.NewWriter(w, simplifiedchinese.GB18030.NewEncoder())}
}

type crlfWriter struct {
	w      io.WriteCloser
	skipLF bool
}

func (w *crlfWriter) Write(p []byte) (int, error) {
	var out bytes.Buffer
	for _, b := range p {
		if w.skipLF {
			w.skipLF = false
			if b == '\n' {
				continue
			}
		}
		if b == '\r' {
			out.Write([]byte{'\r', '\n'})
			w.skipLF = true
			continue
		}
		out.WriteByte(b)
	}
	if out.Len() > 0 {
		if _, err := w.w.Write(out.Bytes()); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (w *crlfWriter) Close() error {
	return w.w.Close()
}
