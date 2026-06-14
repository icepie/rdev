package wincompat

import (
	"bytes"
	"io"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// DecodeOutput converts legacy Windows console output (CP936/GBK/GB18030) to UTF-8.
// Win7 cmd.exe commonly uses an OEM code page instead of UTF-8, which otherwise
// shows up as mojibake in SSH/Web terminals.
func DecodeOutput(r io.Reader) io.Reader {
	return transform.NewReader(r, simplifiedchinese.GB18030.NewDecoder())
}

// EncodeInput converts UTF-8 terminal input into the legacy console code page.
func EncodeInput(w io.Writer) io.WriteCloser {
	return &crlfWriter{w: transform.NewWriter(w, simplifiedchinese.GB18030.NewEncoder())}
}

type crlfWriter struct {
	w       io.WriteCloser
	pending bool
}

func (w *crlfWriter) Write(p []byte) (int, error) {
	var out bytes.Buffer
	for _, b := range p {
		if w.pending {
			if b == '\n' {
				out.WriteByte('\n')
			} else {
				out.WriteByte('\n')
				if b == '\r' {
					out.WriteByte('\r')
				} else {
					out.WriteByte(b)
				}
			}
			w.pending = false
			continue
		}
		if b == '\r' {
			out.WriteByte('\r')
			w.pending = true
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
	if w.pending {
		_, _ = w.w.Write([]byte{'\n'})
		w.pending = false
	}
	return w.w.Close()
}
