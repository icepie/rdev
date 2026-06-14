//go:build !windows

package wincompat

import "io"

func DecodeOutput(r io.Reader) io.Reader { return r }
func EncodeInput(w io.Writer) io.WriteCloser {
	return NormalizeLineEndings(w)
}

func NormalizeLineEndings(w io.Writer) io.WriteCloser {
	if wc, ok := w.(io.WriteCloser); ok {
		return wc
	}
	return nopWriteCloser{Writer: w}
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
