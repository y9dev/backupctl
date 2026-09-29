package backup

import (
	"io"

	"github.com/klauspost/compress/zstd"
)

func decompressZstdToWriter(r io.Reader, w io.Writer) error {
	dec, err := zstd.NewReader(r)
	if err != nil {
		return err
	}
	defer dec.Close()
	_, err = io.Copy(w, dec)
	return err
}
